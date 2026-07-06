package modules

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
)

func testCmdMctx() *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File:    &exec.OSFileExec{},
			Command: &exec.OSCommandExec{},
		},
	}
}

func TestCmdRunBasic(t *testing.T) {
	mctx := testCmdMctx()
	builder := NewCmdRunBuilder(mctx)
	s, err := builder("echo-test", map[string]any{
		"command": "echo",
		"args":    []any{"hello"},
	})
	if err != nil {
		t.Fatalf("NewCmdRunBuilder: %v", err)
	}

	ctx := context.Background()

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for cmd.run without creates")
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ar.Changed {
		t.Error("expected Changed")
	}
	if ar.Details["stdout"] != "hello" {
		t.Errorf("stdout: got %q, want %q", ar.Details["stdout"], "hello")
	}
	if ar.Details["exitcode"] != "0" {
		t.Errorf("exitcode: got %q, want %q", ar.Details["exitcode"], "0")
	}
}

func TestCmdRunShellCommand(t *testing.T) {
	mctx := testCmdMctx()
	builder := NewCmdRunBuilder(mctx)
	s, err := builder("shell-test", map[string]any{
		"command": "echo hello world",
	})
	if err != nil {
		t.Fatalf("NewCmdRunBuilder: %v", err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ar.Details["stdout"] != "hello world" {
		t.Errorf("stdout: got %q, want %q", ar.Details["stdout"], "hello world")
	}
}

func TestCmdRunCreatesGuard(t *testing.T) {
	dir := t.TempDir()
	guardPath := filepath.Join(dir, "guard.txt")

	mctx := testCmdMctx()
	builder := NewCmdRunBuilder(mctx)
	s, err := builder("test", map[string]any{
		"command": "true",
		"creates": guardPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when creates file doesn't exist")
	}

	os.WriteFile(guardPath, []byte("done"), 0644)

	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when creates file exists")
	}
}

func TestCmdRunWithCwd(t *testing.T) {
	dir := t.TempDir()

	mctx := testCmdMctx()
	builder := NewCmdRunBuilder(mctx)
	s, err := builder("pwd-test", map[string]any{
		"command": "pwd",
		"cwd":     dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ar.Details["stdout"] != dir {
		t.Errorf("stdout: got %q, want %q", ar.Details["stdout"], dir)
	}
}

func TestCmdRunWithEnv(t *testing.T) {
	mctx := testCmdMctx()
	builder := NewCmdRunBuilder(mctx)
	s, err := builder("env-test", map[string]any{
		"command": "sh -c 'echo $MY_VAR'",
		"env": map[string]any{
			"MY_VAR": "hello_env",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ar.Details["stdout"] != "hello_env" {
		t.Errorf("stdout: got %q, want %q", ar.Details["stdout"], "hello_env")
	}
}

func TestCmdRunFailedCommand(t *testing.T) {
	mctx := testCmdMctx()
	builder := NewCmdRunBuilder(mctx)
	s, err := builder("fail-test", map[string]any{
		"command": "false",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error for failed command")
	}
}

func TestCmdRunRevert(t *testing.T) {
	mctx := testCmdMctx()
	builder := NewCmdRunBuilder(mctx)
	s, err := builder("test", map[string]any{
		"command": "echo test",
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if ar.Changed {
		t.Error("cmd.run revert should not report changed")
	}
}

func TestCmdRunName(t *testing.T) {
	mctx := testCmdMctx()
	builder := NewCmdRunBuilder(mctx)
	s, err := builder("my-cmd", map[string]any{
		"command": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "cmd.run:my-cmd" {
		t.Errorf("Name: got %q, want %q", s.Name(), "cmd.run:my-cmd")
	}
}

func TestCmdRunRequires(t *testing.T) {
	mctx := testCmdMctx()
	builder := NewCmdRunBuilder(mctx)
	s, err := builder("test", map[string]any{
		"command": "true",
		"require": []any{"file.managed:/etc/config"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "file.managed:/etc/config" {
		t.Errorf("Reqs().Require: got %v", reqs.Require)
	}
}

func TestCmdRunRequisites(t *testing.T) {
	mctx := testCmdMctx()
	builder := NewCmdRunBuilder(mctx)
	s, err := builder("test", map[string]any{
		"command":   "true",
		"require":   []any{"file.managed:/etc/config"},
		"watch":     []any{"file.managed:/etc/nginx.conf"},
		"onchanges": []any{"cmd.run:build"},
		"onfail":    []any{"cmd.run:primary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "file.managed:/etc/config" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/nginx.conf" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:build" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:primary" {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}
