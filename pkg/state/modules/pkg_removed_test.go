package modules

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/state"
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

// Revert deliberately no longer reinstalls: no phase records the removed
// version and it cannot be re-derived, so the old reinstall-latest behavior
// (driven by a never-written savedVersion memo) was a guess, not Apply's
// inverse. A fresh instance's Revert must be an explicit clean no-op.
func TestPkgRemovedRevertFreshInstanceIsCleanNoOp(t *testing.T) {
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
	if ar.Changed {
		t.Error("expected Changed=false: revert has nothing recorded to restore")
	}
	if !strings.Contains(ar.Diff, "nothing to revert") {
		t.Errorf("expected an honest no-op diff, got %q", ar.Diff)
	}
	if fakePkg.IsInstalledSync("nginx") {
		t.Error("revert must not install anything")
	}
}

func TestPkgRemovedRevertAfterApplyIsCleanNoOp(t *testing.T) {
	// Even same-instance revert cannot reconstruct the removed version, so
	// it stays a no-op rather than reinstalling an arbitrary candidate.
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "1.20.1-1")
	mctx := testPkgRemovedMctx(fakePkg)
	builder := NewPkgRemovedBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected Changed=false from revert")
	}
	if fakePkg.IsInstalledSync("nginx") {
		t.Error("revert must not reinstall the package")
	}
}

func TestPkgRemovedConvergesAfterApply(t *testing.T) {
	// Apply(remove) -> Check must observe convergence: the provider's
	// installed-probe reports the package gone, so the state stops
	// re-applying (and stops firing watch/onchanges dependents every run).
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "1.20.1-1")
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
		t.Fatal("precondition: installed package should need removal")
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	cr, err = s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected convergence after removal, diff %q", cr.Diff)
	}
}

func TestPkgRemovedCheckConvergesOnDebianRcState(t *testing.T) {
	// After `apt-get remove` a conffile-bearing package sits in dpkg 'rc'
	// state ("config-files" status). The status-aware provider probe must
	// report it NOT installed, so pkg.removed converges instead of
	// re-running the removal (Changed=true) on every highstate.
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("dpkg-query", &exec.CommandResult{Stdout: "config-files", ExitCode: 0}, nil)
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Package: exec.NewAptProvider(fakeCmd),
			Command: fakeCmd,
		},
	}
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
		t.Errorf("expected no change for an rc-state package, diff %q", cr.Diff)
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
