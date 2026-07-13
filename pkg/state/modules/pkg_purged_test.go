package modules

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
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
	builder := NewPkgPurgedBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewPkgPurgedBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewPkgPurgedBuilder(mctx, modschema.DecodeOptions{})
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

// Check's probe is dpkg-status-aware and deliberately does NOT go through
// PackageExec.IsInstalled: that provider probe answers "fully installed?"
// (rc = not installed) for pkg.installed/pkg.removed, while pkg.purged's
// entire value-add is clearing residual 'rc' state (removed, conffiles
// remain). Converged means: no dpkg record at all.
func TestPkgPurgedCheckDebianStatuses(t *testing.T) {
	for name, tc := range map[string]struct {
		stdout string
		exit   int
		err    error
		needs  bool
	}{
		"installed needs purge":          {"installed\n", 0, nil, true},
		"rc residual config needs purge": {"config-files\n", 0, nil, true},
		"half-installed needs purge":     {"half-installed\n", 0, nil, true},
		"no dpkg record converged":       {"", 1, errors.New("exit status 1"), false},
		"not-installed status converged": {"not-installed\n", 0, nil, false},
		"multiarch one rc needs purge":   {"not-installed\nconfig-files\n", 0, nil, true},
		"multiarch installed":            {"installed\ninstalled\n", 0, nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			fakeCmd := exectest.NewFakeCommandExec()
			fakeCmd.SetResult("dpkg-query", &exec.CommandResult{Stdout: tc.stdout, ExitCode: tc.exit}, tc.err)
			mctx := testPkgPurgedMctx(exectest.NewFakePackageExec("apt"), fakeCmd, "debian")
			builder := NewPkgPurgedBuilder(mctx, modschema.DecodeOptions{})
			s, err := builder("nginx", map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			cr, err := s.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if cr.NeedsChange != tc.needs {
				t.Errorf("NeedsChange = %v, want %v (diff %q)", cr.NeedsChange, tc.needs, cr.Diff)
			}
		})
	}
}

func TestPkgPurgedCheckProbeSpawnFailureErrors(t *testing.T) {
	// A probe that never ran (nil result: spawn failure, context death) is a
	// real error — reporting "converged" would silently skip the purge.
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("dpkg-query", nil, errors.New("fork failed"))
	mctx := testPkgPurgedMctx(exectest.NewFakePackageExec("apt"), fakeCmd, "debian")
	builder := NewPkgPurgedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Check(context.Background()); err == nil {
		t.Error("expected Check to fail when the probe never ran")
	}
}

func TestPkgPurgedCheckRedhat(t *testing.T) {
	for name, tc := range map[string]struct {
		res   *exec.CommandResult
		err   error
		needs bool
	}{
		"installed needs purge": {&exec.CommandResult{ExitCode: 0}, nil, true},
		"absent converged":      {&exec.CommandResult{ExitCode: 1}, errors.New("exit status 1"), false},
	} {
		t.Run(name, func(t *testing.T) {
			fakeCmd := exectest.NewFakeCommandExec()
			fakeCmd.SetResult("rpm", tc.res, tc.err)
			mctx := testPkgPurgedMctx(exectest.NewFakePackageExec("dnf"), fakeCmd, "redhat")
			builder := NewPkgPurgedBuilder(mctx, modschema.DecodeOptions{})
			s, err := builder("httpd", map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			cr, err := s.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if cr.NeedsChange != tc.needs {
				t.Errorf("NeedsChange = %v, want %v", cr.NeedsChange, tc.needs)
			}
		})
	}
}

func TestPkgPurgedRcStateConvergence(t *testing.T) {
	// THE regression walk: a removed-but-not-purged package ('rc' state)
	// must be seen as needs-purge, and after Apply purges the record the
	// second Check must report converged.
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("dpkg-query", &exec.CommandResult{Stdout: "config-files\n", ExitCode: 0}, nil)
	mctx := testPkgPurgedMctx(exectest.NewFakePackageExec("apt"), fakeCmd, "debian")
	builder := NewPkgPurgedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange for a package in dpkg 'rc' state (conffiles linger)")
	}

	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	var purged bool
	for _, c := range fakeCmd.Calls() {
		if c.Command == "apt-get" && len(c.Args) > 0 && c.Args[0] == "purge" {
			purged = true
		}
	}
	if !purged {
		t.Fatal("expected Apply to run apt-get purge")
	}

	// After the purge dpkg has no record for the package.
	fakeCmd.SetResult("dpkg-query", &exec.CommandResult{ExitCode: 1}, errors.New("exit status 1"))
	cr, err = s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected Check -> Apply -> Check convergence, diff %q", cr.Diff)
	}
}

func TestPkgPurgedDarwinProviderFallback(t *testing.T) {
	// Unknown-to-dpkg/rpm families fall back to the PackageExec probe (no
	// residual-config concept there), mirroring Apply's provider fallback.
	fakePkg := exectest.NewFakePackageExec("brew")
	mctx := testPkgPurgedMctx(fakePkg, exectest.NewFakeCommandExec(), "darwin")
	builder := NewPkgPurgedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("wget", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected converged when the provider reports not installed")
	}

	fakePkg.PreInstall("wget", "")
	cr, err = s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when the provider reports installed")
	}
}

func TestPkgPurgedApplyDebian(t *testing.T) {
	fakePkg := exectest.NewFakePackageExec("apt")
	fakePkg.PreInstall("nginx", "")
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPkgPurgedMctx(fakePkg, fakeCmd, "debian")
	builder := NewPkgPurgedBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewPkgPurgedBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewPkgPurgedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err == nil {
		t.Error("expected error from failed purge")
	}
}

func TestPkgPurgedRevertFreshInstanceCleanNoOp(t *testing.T) {
	// The runner builds FRESH instances for ModeRevert: Revert must be an
	// explicit clean no-op — never a guessed reinstall of whatever the
	// repo's latest candidate is, and never a lying Changed:true.
	fakePkg := exectest.NewFakePackageExec("apt")
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPkgPurgedMctx(fakePkg, fakeCmd, "debian")
	builder := NewPkgPurgedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("Revert must not report Changed: nothing was purged this run")
	}
	if !strings.Contains(ar.Diff, "nothing to revert") {
		t.Errorf("diff should explain the no-op, got %q", ar.Diff)
	}
	if fakeCmd.CallCount() != 0 {
		t.Errorf("Revert must not run any command, ran %d", fakeCmd.CallCount())
	}
	if fakePkg.IsInstalledSync("nginx") {
		t.Error("Revert must not reinstall the package")
	}
}

func TestPkgPurgedRevertAfterApplySameInstanceStillNoOp(t *testing.T) {
	// Even same-instance Apply -> Revert must not reinstall: the purged
	// version was never recorded and the purged conffiles cannot be
	// restored — reinstall-latest is not the inverse of Apply.
	fakePkg := exectest.NewFakePackageExec("apt")
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("dpkg-query", &exec.CommandResult{Stdout: "config-files\n", ExitCode: 0}, nil)
	mctx := testPkgPurgedMctx(fakePkg, fakeCmd, "debian")
	builder := NewPkgPurgedBuilder(mctx, modschema.DecodeOptions{})
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
		t.Error("Revert must not report Changed even after a same-instance Apply")
	}
	for _, c := range fakeCmd.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "install" {
			t.Errorf("Revert must not run an install, saw %s %v", c.Command, c.Args)
		}
	}
	if fakePkg.IsInstalledSync("nginx") {
		t.Error("Revert must not reinstall the package")
	}
}

func TestPkgPurgedNoCommandProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Package: exectest.NewFakePackageExec("apt")}}
	builder := NewPkgPurgedBuilder(mctx, modschema.DecodeOptions{})
	if _, err := builder("nginx", map[string]any{}); err == nil {
		t.Error("expected error when no command provider is set")
	}
}

var _ state.State = (*PkgPurged)(nil)

// TestPkgPurgedContract replays the permanent differential contract fixtures
// against the migrated spec decoder. The cases were approved by the
// legacy-vs-new equivalence comparison while the legacy constructor still
// existed (see the migration changelog); after the legacy constructor's
// deletion this replay is the permanent regression guard for pkg.purged's
// decode behavior — including the flagged BD-6 divergence (a non-string
// `name` is now coerced, or rejected for composites, instead of silently
// falling back to the state ID).
func TestPkgPurgedContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var p PkgPurged
		if _, err := pkgPurgedSpec.Decode(id, config, &p, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &p, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/pkg.purged.yaml")
}
