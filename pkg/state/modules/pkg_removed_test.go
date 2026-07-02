package modules

import (
	"context"
	"fmt"
	"testing"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/exec/exectest"
	"github.com/ptorbus/zester/pkg/state"
)

func testPkgRemovedMctx(fakePkg *exectest.FakePackageExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Package: fakePkg,
		},
	}
}

func TestPkgRemovedName(t *testing.T) {
	mctx := testPkgRemovedMctx(exectest.NewFakePackageExec("apt"))
	builder := NewPkgRemovedBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "pkg.removed:nginx" {
		t.Errorf("Name: got %q, want %q", s.Name(), "pkg.removed:nginx")
	}
}

func TestPkgRemovedPrimaryParamDefault(t *testing.T) {
	mctx := testPkgRemovedMctx(exectest.NewFakePackageExec("apt"))
	builder := NewPkgRemovedBuilder(mctx)
	s, err := builder("curl", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	pr := s.(*PkgRemoved)
	if pr.Package != "curl" {
		t.Errorf("Package: got %q, want %q", pr.Package, "curl")
	}
}

func TestPkgRemovedNameFromConfig(t *testing.T) {
	mctx := testPkgRemovedMctx(exectest.NewFakePackageExec("apt"))
	builder := NewPkgRemovedBuilder(mctx)
	s, err := builder("remove-old-pkg", map[string]any{"name": "wget"})
	if err != nil {
		t.Fatal(err)
	}
	pr := s.(*PkgRemoved)
	if pr.Package != "wget" {
		t.Errorf("Package: got %q, want %q", pr.Package, "wget")
	}
}

func TestPkgRemovedRequisites(t *testing.T) {
	mctx := testPkgRemovedMctx(exectest.NewFakePackageExec("apt"))
	builder := NewPkgRemovedBuilder(mctx)
	s, err := builder("test", map[string]any{
		"require":   []any{"cmd.run:stop"},
		"watch":     []any{"file.managed:/etc/conf"},
		"onchanges": []any{"cmd.run:cleanup"},
		"onfail":    []any{"cmd.run:alert"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "cmd.run:stop" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/conf" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:cleanup" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:alert" {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}

func TestPkgRemovedCheckNotInstalled(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	mctx := testPkgRemovedMctx(fakePkg)
	builder := NewPkgRemovedBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when package not installed")
	}
}

func TestPkgRemovedCheckInstalled(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "")
	mctx := testPkgRemovedMctx(fakePkg)
	builder := NewPkgRemovedBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when package is installed")
	}
}

func TestPkgRemovedApply(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "")
	mctx := testPkgRemovedMctx(fakePkg)
	builder := NewPkgRemovedBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after removal")
	}
	if ar.Details["package"] != "nginx" {
		t.Errorf("Details[package]: got %q", ar.Details["package"])
	}
	if ar.Details["manager"] != "apt" {
		t.Errorf("Details[manager]: got %q", ar.Details["manager"])
	}

	if fakePkg.IsInstalledSync("nginx") {
		t.Error("package should be removed after Apply")
	}
}

func TestPkgRemovedApplyError(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "")
	fakePkg.RemoveErr = fmt.Errorf("permission denied")
	mctx := testPkgRemovedMctx(fakePkg)
	builder := NewPkgRemovedBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from failed removal")
	}
}

func TestPkgRemovedRevert(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	mctx := testPkgRemovedMctx(fakePkg)
	builder := NewPkgRemovedBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after revert (reinstall)")
	}

	if !fakePkg.IsInstalledSync("nginx") {
		t.Error("package should be reinstalled after Revert")
	}
}

func TestPkgRemovedNilProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Package: nil}}
	builder := NewPkgRemovedBuilder(mctx)
	_, err := builder("nginx", map[string]any{})
	if err == nil {
		t.Error("expected error when package provider is nil")
	}
}

var _ state.State = (*PkgRemoved)(nil)
