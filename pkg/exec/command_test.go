package exec

import (
	"context"
	"testing"
)

func TestOSCommandExecBasic(t *testing.T) {
	e := &OSCommandExec{}
	r, err := e.Run(context.Background(), CommandOpts{
		Command: "echo",
		Args:    []string{"hello"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Stdout != "hello" {
		t.Errorf("Stdout: got %q, want %q", r.Stdout, "hello")
	}
	if r.ExitCode != 0 {
		t.Errorf("ExitCode: got %d, want 0", r.ExitCode)
	}
}

func TestOSCommandExecShell(t *testing.T) {
	e := &OSCommandExec{}
	r, err := e.Run(context.Background(), CommandOpts{
		Command: "echo hello world",
		Shell:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Stdout != "hello world" {
		t.Errorf("Stdout: got %q, want %q", r.Stdout, "hello world")
	}
}

func TestOSCommandExecNoArgsDefaultsToShell(t *testing.T) {
	e := &OSCommandExec{}
	r, err := e.Run(context.Background(), CommandOpts{
		Command: "echo default-shell",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Stdout != "default-shell" {
		t.Errorf("Stdout: got %q, want %q", r.Stdout, "default-shell")
	}
}

func TestOSCommandExecCwd(t *testing.T) {
	dir := t.TempDir()
	e := &OSCommandExec{}
	r, err := e.Run(context.Background(), CommandOpts{
		Command: "pwd",
		Dir:     dir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Stdout != dir {
		t.Errorf("Stdout: got %q, want %q", r.Stdout, dir)
	}
}

func TestOSCommandExecEnv(t *testing.T) {
	e := &OSCommandExec{}
	r, err := e.Run(context.Background(), CommandOpts{
		Command: "sh -c 'echo $ZESTER_TEST_VAR'",
		Env:     map[string]string{"ZESTER_TEST_VAR": "test_value"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Stdout != "test_value" {
		t.Errorf("Stdout: got %q, want %q", r.Stdout, "test_value")
	}
}

func TestOSCommandExecFailure(t *testing.T) {
	e := &OSCommandExec{}
	r, err := e.Run(context.Background(), CommandOpts{
		Command: "false",
	})
	if err == nil {
		t.Fatal("expected error for failed command")
	}
	if r == nil {
		t.Fatal("expected result even on error")
	}
	if r.ExitCode != 1 {
		t.Errorf("ExitCode: got %d, want 1", r.ExitCode)
	}
}

func TestOSCommandExecStderr(t *testing.T) {
	e := &OSCommandExec{}
	r, err := e.Run(context.Background(), CommandOpts{
		Command: "sh -c 'echo oops >&2'",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Stderr != "oops" {
		t.Errorf("Stderr: got %q, want %q", r.Stderr, "oops")
	}
}

func TestOSCommandExecCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	e := &OSCommandExec{}
	_, err := e.Run(ctx, CommandOpts{
		Command: "sleep 10",
	})
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}
