package modules

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

func testPkgMctx(fakePkg *exectest.FakePackageExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Package: fakePkg,
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
	}
}

func TestPkgInstalledName(t *testing.T) {
	mctx := testPkgMctx(exectest.NewFakePackageExec("apt"))
	builder := NewPkgInstalledBuilder(mctx)
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "pkg.installed:nginx" {
		t.Errorf("Name: got %q, want %q", s.Name(), "pkg.installed:nginx")
	}
}

func TestPkgInstalledNameFromConfig(t *testing.T) {
	mctx := testPkgMctx(exectest.NewFakePackageExec("apt"))
	builder := NewPkgInstalledBuilder(mctx)
	s, err := builder("web-server", map[string]any{
		"name": "nginx",
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := s.(*PkgInstalled)
	if pkg.Package != "nginx" {
		t.Errorf("Package: got %q, want %q", pkg.Package, "nginx")
	}
}

func TestPkgInstalledRequires(t *testing.T) {
	mctx := testPkgMctx(exectest.NewFakePackageExec("apt"))
	builder := NewPkgInstalledBuilder(mctx)
	s, err := builder("test", map[string]any{
		"require": []any{"cmd.run:update-repos"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "cmd.run:update-repos" {
		t.Errorf("Reqs().Require: got %v", reqs.Require)
	}
}

func TestPkgInstalledRequisites(t *testing.T) {
	mctx := testPkgMctx(exectest.NewFakePackageExec("apt"))
	builder := NewPkgInstalledBuilder(mctx)
	s, err := builder("test", map[string]any{
		"require": []any{"cmd.run:update-repos"},
		"onfail":  []any{"cmd.run:fallback-install"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "cmd.run:update-repos" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:fallback-install" {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}

func TestPkgInstalledCheckAlreadyInstalled(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "")
	mctx := testPkgMctx(fakePkg)
	builder := NewPkgInstalledBuilder(mctx)

	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change for already installed package")
	}
}

func TestPkgInstalledCheckNotInstalled(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	mctx := testPkgMctx(fakePkg)
	builder := NewPkgInstalledBuilder(mctx)

	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for missing package")
	}
}

func TestPkgInstalledCheckVersionMismatch(t *testing.T) {
	// A declared version pin is part of the desired state: an installed
	// package at any OTHER version needs a change (upgrade AND downgrade
	// directions — the comparison is symmetric).
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "1.20.1-1")
	mctx := testPkgMctx(fakePkg)
	builder := NewPkgInstalledBuilder(mctx)

	s, err := builder("nginx", map[string]any{"version": "1.24.0-1"})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange when the installed version differs from the declared pin")
	}
	if !strings.Contains(cr.Diff, `got "1.20.1-1"`) || !strings.Contains(cr.Diff, `want "1.24.0-1"`) {
		t.Errorf("diff should carry got/want versions, got %q", cr.Diff)
	}
}

func TestPkgInstalledCheckVersionMatch(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "1.24.0-1")
	mctx := testPkgMctx(fakePkg)
	builder := NewPkgInstalledBuilder(mctx)

	s, err := builder("nginx", map[string]any{"version": "1.24.0-1"})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change when the installed version matches the pin, diff %q", cr.Diff)
	}
}

func TestPkgInstalledCheckNoVersionDeclaredIgnoresInstalledVersion(t *testing.T) {
	// Undeclared facet must never churn: without a version pin, any
	// installed version satisfies the state.
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "1.20.1-1")
	mctx := testPkgMctx(fakePkg)
	builder := NewPkgInstalledBuilder(mctx)

	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change without a declared version pin, diff %q", cr.Diff)
	}
}

func TestPkgInstalledCheckVersionDeclaredButAbsent(t *testing.T) {
	// The absent => install path is unchanged by the pin comparison.
	fakePkg := exectest.NewFakePackageExec("apt")
	mctx := testPkgMctx(fakePkg)
	builder := NewPkgInstalledBuilder(mctx)

	s, err := builder("nginx", map[string]any{"version": "1.24.0-1"})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange for a missing package")
	}
	if !strings.Contains(cr.Diff, "needs to be installed") {
		t.Errorf("expected the install diff for an absent package, got %q", cr.Diff)
	}
}

func TestPkgInstalledVersionPinConvergesAfterApply(t *testing.T) {
	// Check(drift) -> Apply(pin) -> Check must converge.
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "1.20.1-1")
	mctx := testPkgMctx(fakePkg)
	builder := NewPkgInstalledBuilder(mctx)

	s, err := builder("nginx", map[string]any{"version": "1.24.0-1"})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("precondition: drifted version should need a change")
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	cr, err = s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected convergence after Apply installed the pinned version, diff %q", cr.Diff)
	}
}

func TestPkgInstalledCheckDebianRcStateNeedsInstall(t *testing.T) {
	// A dpkg 'rc' package (removed, conffiles remain) reports
	// "config-files" for ${db:Status-Status}; the status-aware provider
	// probe must count it as NOT installed so pkg.installed reinstalls it
	// instead of reporting "already installed" forever.
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("dpkg-query", &exec.CommandResult{Stdout: "config-files", ExitCode: 0}, nil)
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Package: exec.NewAptProvider(fakeCmd),
			Command: fakeCmd,
		},
	}
	builder := NewPkgInstalledBuilder(mctx)

	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for a package in dpkg rc state")
	}
}

func TestPkgInstalledApply(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	mctx := testPkgMctx(fakePkg)
	builder := NewPkgInstalledBuilder(mctx)

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
		t.Errorf("manager: got %q, want %q", ar.Details["manager"], "apt")
	}
	if !fakePkg.IsInstalledSync("nginx") {
		t.Error("expected nginx to be installed in fake")
	}
}

func TestPkgInstalledApplyWithRefresh(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("dnf")
	mctx := testPkgMctx(fakePkg)
	builder := NewPkgInstalledBuilder(mctx)

	s, err := builder("curl", map[string]any{
		"refresh": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if fakePkg.RefreshCount() != 1 {
		t.Errorf("RefreshCount: got %d, want 1", fakePkg.RefreshCount())
	}
}

func TestPkgInstalledRevert(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("brew")
	fakePkg.PreInstall("wget", "")
	mctx := testPkgMctx(fakePkg)
	builder := NewPkgInstalledBuilder(mctx)

	s, err := builder("wget", map[string]any{})
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
	if fakePkg.IsInstalledSync("wget") {
		t.Error("expected wget to be removed from fake")
	}
}

func TestPkgInstalledNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
	}
	builder := NewPkgInstalledBuilder(mctx)

	_, err := builder("nginx", map[string]any{})
	if err == nil {
		t.Error("expected error when no package provider is set")
	}
}

func TestPkgInstalledApplyError(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.InstallErr = fmt.Errorf("permission denied")
	mctx := testPkgMctx(fakePkg)
	builder := NewPkgInstalledBuilder(mctx)

	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from failed install")
	}
}
