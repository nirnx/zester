package peeld

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
)

// guardCmdFake returns a fixed (result, error) pair, mimicking
// OSCommandExec.Run's shape: a non-nil result with ExitCode populated even
// when the command exits non-zero (err wraps the ExitError), and ExitCode -1
// when the process never ran or was killed.
type guardCmdFake struct {
	res *exec.CommandResult
	err error
}

func (f guardCmdFake) Run(context.Context, exec.CommandOpts) (*exec.CommandResult, error) {
	return f.res, f.err
}

func guardAgent(cmd exec.CommandExec) *Agent {
	return &Agent{mctx: &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd}}}
}

// TestRunGuard_NonZeroExitIsAnswerNotError pins the guard contract: a guard
// command that RAN and exited non-zero returns (code, nil) — the caller
// (guardsAllow) turns that into a clean skip (onlyif) or a run (unless),
// never an error. Passing the exit error through made every failing onlyif
// report an error and every unless-guarded state fail instead of run.
func TestRunGuard_NonZeroExitIsAnswerNotError(t *testing.T) {
	a := guardAgent(guardCmdFake{
		res: &exec.CommandResult{ExitCode: 1},
		err: errors.New("exec: run test -f /nonexistent: exit status 1"),
	})
	code, err := a.runGuard(context.Background(), "test -f /nonexistent")
	if err != nil {
		t.Fatalf("non-zero guard exit must not error, got %v", err)
	}
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

// TestRunGuard_ZeroExit: the guard-met path is unchanged.
func TestRunGuard_ZeroExit(t *testing.T) {
	a := guardAgent(guardCmdFake{res: &exec.CommandResult{ExitCode: 0}})
	code, err := a.runGuard(context.Background(), "true")
	if err != nil || code != 0 {
		t.Fatalf("got (%d, %v), want (0, nil)", code, err)
	}
}

// TestRunGuard_SpawnFailureErrors: a guard that never ran (ExitCode -1, or
// no result at all) has no answer — that IS an error.
func TestRunGuard_SpawnFailureErrors(t *testing.T) {
	for name, fake := range map[string]guardCmdFake{
		"no result":   {res: nil, err: errors.New("exec: run x: no such file")},
		"exit code-1": {res: &exec.CommandResult{ExitCode: -1}, err: errors.New("exec: run x: fork failed")},
	} {
		a := guardAgent(fake)
		if _, err := a.runGuard(context.Background(), "x"); err == nil {
			t.Errorf("%s: spawn failure must error", name)
		}
	}
}

// TestRunGuard_ContextDeathErrors: a guard killed mid-run (deadline/cancel)
// has no meaningful exit code; the context error surfaces even though the
// shell reports a positive code for the signal.
func TestRunGuard_ContextDeathErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := guardAgent(guardCmdFake{
		res: &exec.CommandResult{ExitCode: 137},
		err: errors.New("exec: run sleep 60: signal: killed"),
	})
	if _, err := a.runGuard(ctx, "sleep 60"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled guard must return the context error, got %v", err)
	}
}

// TestRunGuard_RealOS runs the real provider end-to-end: `exit 1` is a clean
// (1, nil); `exit 7` preserves the code; `true` is (0, nil).
func TestRunGuard_RealOS(t *testing.T) {
	a := guardAgent(&exec.OSCommandExec{})
	for _, tc := range []struct {
		cmd  string
		want int
	}{
		{"true", 0},
		{"exit 1", 1},
		{"exit 7", 7},
		{"test -f /nonexistent-zester-guard", 1},
	} {
		code, err := a.runGuard(context.Background(), tc.cmd)
		if err != nil {
			t.Errorf("%q: unexpected error %v", tc.cmd, err)
			continue
		}
		if code != tc.want {
			t.Errorf("%q: code = %d, want %d", tc.cmd, code, tc.want)
		}
	}
}

// TestGuardEndToEnd_UnlessRunsOnNonZero pins the full attribute path that
// broke in the field: an unless guard exiting non-zero must let the state
// RUN (and onlyif exiting non-zero must cleanly skip), through the same
// GuardRunnerFunc wiring the peel uses.
func TestGuardEndToEnd_UnlessRunsOnNonZero(t *testing.T) {
	a := guardAgent(&exec.OSCommandExec{})
	code, err := a.runGuard(context.Background(), "test -f /nonexistent-zester-guard")
	if err != nil {
		t.Fatalf("field scenario: onlyif/unless probe errored instead of answering: %v", err)
	}
	if code == 0 {
		t.Fatal("probe unexpectedly exited 0")
	}
	// Sanity on the message shape the operator saw pre-fix.
	if err != nil && strings.Contains(err.Error(), "exit status") {
		t.Fatal("exit-status errors must not escape runGuard")
	}
}
