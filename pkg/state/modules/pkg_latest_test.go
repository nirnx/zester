package modules

import (
	"context"
	"errors"
	"testing"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/exec/exectest"
	"github.com/ptorbus/zester/pkg/state"
)

func testPkgLatestMctx(fakePkg *exectest.FakePackageExec, fakeCmd *exectest.FakeCommandExec, family string) *exec.ModuleContext {
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

func TestPkgLatestName(t *testing.T) {
	mctx := testPkgLatestMctx(exectest.NewFakePackageExec("apt"), exectest.NewFakeCommandExec(), "debian")
	builder := NewPkgLatestBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "pkg.latest:nginx" {
		t.Errorf("Name: got %q, want %q", s.Name(), "pkg.latest:nginx")
	}
}

func TestPkgLatestPrimaryParamDefault(t *testing.T) {
	mctx := testPkgLatestMctx(exectest.NewFakePackageExec("apt"), exectest.NewFakeCommandExec(), "debian")
	builder := NewPkgLatestBuilder(mctx)
	s, err := builder("web-server", map[string]any{"name": "nginx"})
	if err != nil {
		t.Fatal(err)
	}
	p := s.(*PkgLatest)
	if p.Package != "nginx" {
		t.Errorf("Package: got %q, want nginx", p.Package)
	}
	if !p.Refresh {
		t.Error("Refresh should default to true")
	}
}

func TestPkgLatestRequisites(t *testing.T) {
	mctx := testPkgLatestMctx(exectest.NewFakePackageExec("apt"), exectest.NewFakeCommandExec(), "debian")
	builder := NewPkgLatestBuilder(mctx)
	s, err := builder("test", map[string]any{
		"require":   []any{"cmd.run:repos"},
		"watch":     []any{"file.managed:/etc/apt/sources.list"},
		"onchanges": []any{"cmd.run:cleanup"},
		"onfail":    []any{"cmd.run:alert"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "cmd.run:repos" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/apt/sources.list" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:cleanup" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:alert" {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}

func TestPkgLatestCheckNotInstalled(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	mctx := testPkgLatestMctx(fakePkg, exectest.NewFakeCommandExec(), "debian")
	builder := NewPkgLatestBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for a package that is not installed")
	}
}

func TestPkgLatestCheckAptUpgradable(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "")
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("apt-get", &exec.CommandResult{
		Stdout:   "Reading package lists...\nInst nginx [1.0] (1.1 Ubuntu:20.04)\nConf nginx (1.1)",
		ExitCode: 0,
	}, nil)
	mctx := testPkgLatestMctx(fakePkg, fakeCmd, "debian")
	builder := NewPkgLatestBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when apt reports an upgrade")
	}
}

func TestPkgLatestCheckAptUpToDate(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "")
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("apt-get", &exec.CommandResult{
		Stdout:   "Reading package lists...\nnginx is already the newest version (1.1).\n0 upgraded, 0 newly installed",
		ExitCode: 0,
	}, nil)
	mctx := testPkgLatestMctx(fakePkg, fakeCmd, "debian")
	builder := NewPkgLatestBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when apt reports newest version")
	}
}

func TestPkgLatestCheckDnfUpgradable(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("dnf")
	fakePkg.PreInstall("httpd", "")
	fakeCmd := exectest.NewFakeCommandExec()
	// dnf check-update exits 100 when an update is available (and returns an error).
	fakeCmd.SetResult("dnf", &exec.CommandResult{ExitCode: 100}, errors.New("exit 100"))
	mctx := testPkgLatestMctx(fakePkg, fakeCmd, "redhat")
	builder := NewPkgLatestBuilder(mctx)
	s, err := builder("httpd", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when dnf check-update exits 100")
	}
}

func TestPkgLatestCheckDnfUpToDate(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("dnf")
	fakePkg.PreInstall("httpd", "")
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("dnf", &exec.CommandResult{ExitCode: 0}, nil)
	mctx := testPkgLatestMctx(fakePkg, fakeCmd, "redhat")
	builder := NewPkgLatestBuilder(mctx)
	s, err := builder("httpd", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when dnf check-update exits 0")
	}
}

func TestPkgLatestCheckUndeterminedFallsBack(t *testing.T) {
	// No command provider: installed package is treated as satisfied.
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "")
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Package: fakePkg,
		},
		Facts: map[string]any{"os": map[string]any{"family": "debian"}},
	}
	builder := NewPkgLatestBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when upgradability is undetermined and package is installed")
	}
}

func TestPkgLatestApply(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPkgLatestMctx(fakePkg, fakeCmd, "debian")
	builder := NewPkgLatestBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after install")
	}
	if ar.Details["manager"] != "apt" {
		t.Errorf("manager: got %q, want apt", ar.Details["manager"])
	}
	if !fakePkg.IsInstalledSync("nginx") {
		t.Error("expected nginx to be installed in fake")
	}
	if fakePkg.RefreshCount() != 1 {
		t.Errorf("expected one refresh (default), got %d", fakePkg.RefreshCount())
	}
}

func TestPkgLatestApplyNoRefresh(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	mctx := testPkgLatestMctx(fakePkg, exectest.NewFakeCommandExec(), "debian")
	builder := NewPkgLatestBuilder(mctx)
	s, err := builder("nginx", map[string]any{"refresh": false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fakePkg.RefreshCount() != 0 {
		t.Errorf("expected no refresh, got %d", fakePkg.RefreshCount())
	}
}

func TestPkgLatestApplyError(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.InstallErr = errors.New("permission denied")
	mctx := testPkgLatestMctx(fakePkg, exectest.NewFakeCommandExec(), "debian")
	builder := NewPkgLatestBuilder(mctx)
	s, err := builder("nginx", map[string]any{"refresh": false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err == nil {
		t.Error("expected error from failed install")
	}
}

func TestPkgLatestRevert(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "")
	mctx := testPkgLatestMctx(fakePkg, exectest.NewFakeCommandExec(), "debian")
	builder := NewPkgLatestBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after remove")
	}
	if fakePkg.IsInstalledSync("nginx") {
		t.Error("expected nginx to be removed from fake")
	}
}

func TestPkgLatestNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: exectest.NewFakeCommandExec()}}
	builder := NewPkgLatestBuilder(mctx)
	if _, err := builder("nginx", map[string]any{}); err == nil {
		t.Error("expected error when no package provider is set")
	}
}

var _ state.State = (*PkgLatest)(nil)
