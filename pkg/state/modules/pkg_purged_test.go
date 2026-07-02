package modules

import (
	"context"
	"errors"
	"testing"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/exec/exectest"
	"github.com/ptorbus/zester/pkg/state"
)

func testPkgPurgedMctx(fakePkg *exectest.FakePackageExec, fakeCmd *exectest.FakeCommandExec, family string) *exec.ModuleContext {
	facts := map[string]any{}
	if family != "" {
		facts["os"] = map[string]any{"family": family}
	}
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Package: fakePkg,
			Command: fakeCmd,
			File:    exectest.NewFakeFileExec(),
		},
		Facts: facts,
	}
}

func TestPkgPurgedName(t *testing.T) {
	mctx := testPkgPurgedMctx(exectest.NewFakePackageExec("apt"), exectest.NewFakeCommandExec(), "debian")
	builder := NewPkgPurgedBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "pkg.purged:nginx" {
		t.Errorf("Name: got %q, want %q", s.Name(), "pkg.purged:nginx")
	}
}

func TestPkgPurgedPrimaryParamDefault(t *testing.T) {
	mctx := testPkgPurgedMctx(exectest.NewFakePackageExec("apt"), exectest.NewFakeCommandExec(), "debian")
	builder := NewPkgPurgedBuilder(mctx)
	s, err := builder("remove-it", map[string]any{"name": "wget"})
	if err != nil {
		t.Fatal(err)
	}
	p := s.(*PkgPurged)
	if p.Package != "wget" {
		t.Errorf("Package: got %q, want wget", p.Package)
	}
}

func TestPkgPurgedRequisites(t *testing.T) {
	mctx := testPkgPurgedMctx(exectest.NewFakePackageExec("apt"), exectest.NewFakeCommandExec(), "debian")
	builder := NewPkgPurgedBuilder(mctx)
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

func TestPkgPurgedCheckNotInstalled(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	mctx := testPkgPurgedMctx(fakePkg, exectest.NewFakeCommandExec(), "debian")
	builder := NewPkgPurgedBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when package is not installed")
	}
}

func TestPkgPurgedCheckInstalled(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "")
	mctx := testPkgPurgedMctx(fakePkg, exectest.NewFakeCommandExec(), "debian")
	builder := NewPkgPurgedBuilder(mctx)
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

func TestPkgPurgedApplyDebian(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "")
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPkgPurgedMctx(fakePkg, fakeCmd, "debian")
	builder := NewPkgPurgedBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after purge")
	}
	if ar.Details["manager"] != "apt-get" {
		t.Errorf("manager: got %q, want apt-get", ar.Details["manager"])
	}
	calls := fakeCmd.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected one command call, got %d", len(calls))
	}
	if calls[0].Command != "apt-get" || len(calls[0].Args) < 1 || calls[0].Args[0] != "purge" {
		t.Errorf("expected apt-get purge, got %s %v", calls[0].Command, calls[0].Args)
	}
}

func TestPkgPurgedApplyRedhat(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("dnf")
	fakePkg.PreInstall("httpd", "")
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPkgPurgedMctx(fakePkg, fakeCmd, "redhat")
	builder := NewPkgPurgedBuilder(mctx)
	s, err := builder("httpd", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := fakeCmd.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected one command call, got %d", len(calls))
	}
	if calls[0].Command != "dnf" || calls[0].Args[0] != "remove" {
		t.Errorf("expected dnf remove, got %s %v", calls[0].Command, calls[0].Args)
	}
}

func TestPkgPurgedApplyError(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "")
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetError("apt-get", errors.New("dpkg locked"))
	mctx := testPkgPurgedMctx(fakePkg, fakeCmd, "debian")
	builder := NewPkgPurgedBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err == nil {
		t.Error("expected error from failed purge")
	}
}

func TestPkgPurgedRevert(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	mctx := testPkgPurgedMctx(fakePkg, exectest.NewFakeCommandExec(), "debian")
	builder := NewPkgPurgedBuilder(mctx)
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
		t.Error("expected nginx to be reinstalled")
	}
}

func TestPkgPurgedNoCommandProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Package: exectest.NewFakePackageExec("apt")}}
	builder := NewPkgPurgedBuilder(mctx)
	if _, err := builder("nginx", map[string]any{}); err == nil {
		t.Error("expected error when no command provider is set")
	}
}

var _ state.State = (*PkgPurged)(nil)
