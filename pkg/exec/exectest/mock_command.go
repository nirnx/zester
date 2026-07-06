package exectest

import (
	"context"
	"fmt"
	"sync"

	"github.com/nirnx/zester/pkg/exec"
)

// FakeCommandExec is an in-memory fake for exec.CommandExec.
// It records calls and returns pre-configured results.
type FakeCommandExec struct {
	mu      sync.Mutex
	calls   []exec.CommandOpts
	results map[string]commandResult

	// DefaultResult is returned when no specific result is configured.
	DefaultResult *exec.CommandResult
	// DefaultErr is returned when no specific result is configured.
	DefaultErr error
}

type commandResult struct {
	result *exec.CommandResult
	err    error
}

func NewFakeCommandExec() *FakeCommandExec {
	return &FakeCommandExec{
		results:       make(map[string]commandResult),
		DefaultResult: &exec.CommandResult{ExitCode: 0},
	}
}

func (f *FakeCommandExec) Run(_ context.Context, opts exec.CommandOpts) (*exec.CommandResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, opts)

	key := opts.Command
	if r, ok := f.results[key]; ok {
		return r.result, r.err
	}

	return f.DefaultResult, f.DefaultErr
}

// SetResult configures the result for a specific command.
func (f *FakeCommandExec) SetResult(command string, result *exec.CommandResult, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[command] = commandResult{result: result, err: err}
}

// SetError configures an error for a specific command.
func (f *FakeCommandExec) SetError(command string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[command] = commandResult{
		result: &exec.CommandResult{ExitCode: 1},
		err:    fmt.Errorf("exec: run %s: %w", command, err),
	}
}

// Calls returns all recorded command calls.
func (f *FakeCommandExec) Calls() []exec.CommandOpts {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]exec.CommandOpts, len(f.calls))
	copy(cp, f.calls)
	return cp
}

// CallCount returns the number of recorded calls.
func (f *FakeCommandExec) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}
