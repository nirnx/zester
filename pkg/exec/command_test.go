package exec

import (
	"context"
	"os"
	"path/filepath"
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

func TestOSCommandExecNoArgsNoShellExecsBareBinary(t *testing.T) {
	// Shell:false with no Args exec's Command directly as a bare binary —
	// it is NOT routed through `sh -c`.
	e := &OSCommandExec{}
	r, err := e.Run(context.Background(), CommandOpts{
		Command: "true",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.ExitCode != 0 {
		t.Errorf("ExitCode: got %d, want 0", r.ExitCode)
	}
}

func TestOSCommandExecNoShellNeverInterpretsCommandString(t *testing.T) {
	// Regression for the implicit-shell fallback: a Shell:false command
	// string with no Args used to be silently handed to `sh -c`, so a value
	// interpolated into Command by a caller that never asked for a shell
	// was interpreted — metacharacters and all. It must now be treated as a
	// single binary name (a lookup failure), and the payload must not run.
	marker := filepath.Join(t.TempDir(), "pwned")
	e := &OSCommandExec{}
	r, err := e.Run(context.Background(), CommandOpts{
		Command: "true; touch " + marker,
	})
	if err == nil {
		t.Fatal("expected a lookup error for a space-containing binary name, got success")
	}
	if r == nil || r.ExitCode != -1 {
		t.Errorf("expected ExitCode -1 (never spawned), got %+v", r)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatalf("shell payload RAN: marker %s exists", marker)
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
		Command: "echo $ZESTER_TEST_VAR",
		Shell:   true,
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
		Command: "echo oops >&2",
		Shell:   true,
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
		Command: "sleep",
		Args:    []string{"10"},
	})
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}
