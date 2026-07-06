package modules

import (
	"context"
	"errors"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

func testPipMctx(fakeCmd *exectest.FakeCommandExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Command: fakeCmd,
			File:    exectest.NewFakeFileExec(),
			Package: exectest.NewFakePackageExec("apt"),
		},
	}
}

func TestPipInstalledName(t *testing.T) {
	mctx := testPipMctx(exectest.NewFakeCommandExec())
	builder := NewPipInstalledBuilder(mctx)
	s, err := builder("requests", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "pip.installed:requests" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestPipInstalledPrimaryParamDefault(t *testing.T) {
	mctx := testPipMctx(exectest.NewFakeCommandExec())
	builder := NewPipInstalledBuilder(mctx)
	s, err := builder("requests", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	p := s.(*PipInstalled)
	if p.Package != "requests" {
		t.Errorf("Package: got %q, want requests", p.Package)
	}
	if p.Bin != "pip3" {
		t.Errorf("Bin: got %q, want pip3", p.Bin)
	}
}

func TestPipInstalledRequisites(t *testing.T) {
	mctx := testPipMctx(exectest.NewFakeCommandExec())
	builder := NewPipInstalledBuilder(mctx)
	s, err := builder("requests", map[string]any{
		"require":   []any{"pkg.installed:python3"},
		"onchanges": []any{"cmd.run:update-pip"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:python3" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:update-pip" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
}

func TestPipInstalledCheckAlreadyInstalled(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("pip3", &exec.CommandResult{
		Stdout:   "Name: requests\nVersion: 2.28.0\n",
		ExitCode: 0,
	}, nil)
	mctx := testPipMctx(fakeCmd)
	builder := NewPipInstalledBuilder(mctx)

	s, err := builder("requests", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when package is installed")
	}
}

func TestPipInstalledCheckVersionMismatch(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("pip3", &exec.CommandResult{
		Stdout:   "Name: requests\nVersion: 2.27.0\n",
		ExitCode: 0,
	}, nil)
	mctx := testPipMctx(fakeCmd)
	builder := NewPipInstalledBuilder(mctx)

	s, err := builder("requests", map[string]any{"version": "2.28.0"})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange on version mismatch")
	}
}

func TestPipInstalledCheckNotInstalled(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetError("pip3", errors.New("package not found"))
	mctx := testPipMctx(fakeCmd)
	builder := NewPipInstalledBuilder(mctx)

	s, err := builder("requests", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when package is not installed")
	}
}

func TestPipInstalledApply(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPipMctx(fakeCmd)
	builder := NewPipInstalledBuilder(mctx)

	s, err := builder("requests", map[string]any{})
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
	if ar.Details["bin"] != "pip3" {
		t.Errorf("bin: got %q, want pip3", ar.Details["bin"])
	}

	calls := fakeCmd.Calls()
	if len(calls) == 0 {
		t.Fatal("expected pip3 call")
	}
	if calls[0].Command != "pip3" {
		t.Errorf("command: got %q, want pip3", calls[0].Command)
	}
	if len(calls[0].Args) < 2 || calls[0].Args[0] != "install" {
		t.Errorf("args: got %v, expected install subcommand", calls[0].Args)
	}
}

func TestPipInstalledApplyWithVersion(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPipMctx(fakeCmd)
	builder := NewPipInstalledBuilder(mctx)

	s, err := builder("requests", map[string]any{"version": "2.28.0"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	calls := fakeCmd.Calls()
	if len(calls) == 0 {
		t.Fatal("no calls recorded")
	}
	found := false
	for _, a := range calls[0].Args {
		if a == "requests==2.28.0" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected requests==2.28.0 in args: %v", calls[0].Args)
	}
}

func TestPipInstalledApplyError(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetError("pip3", errors.New("permission denied"))
	mctx := testPipMctx(fakeCmd)
	builder := NewPipInstalledBuilder(mctx)

	s, err := builder("requests", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error when pip install fails")
	}
}

func TestPipInstalledRevert(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPipMctx(fakeCmd)
	builder := NewPipInstalledBuilder(mctx)

	s, err := builder("requests", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after uninstall")
	}

	calls := fakeCmd.Calls()
	if len(calls) == 0 {
		t.Fatal("no calls")
	}
	if calls[0].Args[0] != "uninstall" {
		t.Errorf("expected uninstall, got %v", calls[0].Args)
	}
}

func TestPipInstalledRevert_Requirements(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPipMctx(fakeCmd)
	builder := NewPipInstalledBuilder(mctx)

	s, err := builder("myapp", map[string]any{"requirements": "/app/requirements.txt"})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected no change on requirements revert")
	}
}

func TestPipInstalledNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File: exectest.NewFakeFileExec(),
		},
	}
	builder := NewPipInstalledBuilder(mctx)
	_, err := builder("requests", map[string]any{})
	if err == nil {
		t.Error("expected error when no command provider is set")
	}
}
