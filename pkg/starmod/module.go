package starmod

import (
	"context"
	"fmt"
	"sync"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
	"go.starlark.net/starlark"
)

// DefaultMaxExecutionSteps is the per-thread Starlark execution-step budget
// applied when LoaderConfig.MaxExecutionSteps (or WithMaxExecutionSteps) is
// zero. A step is roughly one interpreter instruction; 50 million is far
// beyond any realistic configuration-management module (tens of thousands of
// items × a few hundred operations each) yet bounds a runaway loop to well
// under a second of CPU on current hardware (~100ms measured) instead of
// wedging the peel's single serialized exec worker forever. It applies to
// module LOAD (top-level code at startup /
// compile time, where no cancellable context exists) and to every
// Check/Apply/Revert call.
const DefaultMaxExecutionSteps uint64 = 50_000_000

// StarlarkState implements state.State for a Starlark-defined module.
// It wraps the user's apply, check, and revert functions with the
// standard Check/Apply/Revert lifecycle.
type StarlarkState struct {
	moduleName string
	id         string
	reqs       state.Requisites
	config     map[string]any

	applyFn  *starlark.Function
	checkFn  *starlark.Function // nil if no _check function
	revertFn *starlark.Function // nil if no _revert function

	builtins starlark.StringDict
	mctx     *exec.ModuleContext

	// maxSteps is the per-call execution-step budget; 0 means
	// DefaultMaxExecutionSteps (resolved by effectiveMaxSteps at call time).
	maxSteps uint64
}

// StateOption tunes a StarlarkState built by NewStarlarkBuilder.
type StateOption func(*StarlarkState)

// WithMaxExecutionSteps sets the per-call Starlark execution-step budget for
// every state the builder produces. 0 selects DefaultMaxExecutionSteps.
func WithMaxExecutionSteps(n uint64) StateOption {
	return func(s *StarlarkState) { s.maxSteps = n }
}

// effectiveMaxSteps maps the "0 = default" convention onto a concrete budget.
func effectiveMaxSteps(n uint64) uint64 {
	if n == 0 {
		return DefaultMaxExecutionSteps
	}
	return n
}

// limitThreadSteps caps thread at maxSteps execution steps (0 = default). The
// interpreter checks the counter on every instruction, so exceeding it fails
// the running call promptly with an EvalError whose message names the step
// limit — a load-time or run-time infinite loop can no longer hang the peel.
func limitThreadSteps(thread *starlark.Thread, maxSteps uint64) {
	limit := effectiveMaxSteps(maxSteps)
	thread.SetMaxExecutionSteps(limit)
	thread.OnMaxSteps = func(th *starlark.Thread) {
		th.Cancel(fmt.Sprintf("execution step limit exceeded (%d steps): the module loops too long or forever", limit))
	}
}

// cancelOnContext makes thread fail promptly (with an EvalError naming the
// context error) once ctx is done, so job cancellation and timeouts interrupt
// Starlark code that is otherwise stuck in a pure-Starlark loop (builtins that
// block already consult the "context" thread-local). The returned stop func
// MUST be called when the Starlark call returns: it releases the watcher
// goroutine. A ctx that can never be cancelled installs no watcher.
func cancelOnContext(ctx context.Context, thread *starlark.Thread) (stop func()) {
	if ctx.Done() == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			thread.Cancel("context cancelled: " + ctx.Err().Error())
		case <-done:
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

func (s *StarlarkState) Name() string           { return s.moduleName + ":" + s.id }
func (s *StarlarkState) Reqs() state.Requisites { return s.reqs }

// Check inspects the current system state. If no _check function is defined,
// always reports NeedsChange: true (like cmd.run).
func (s *StarlarkState) Check(ctx context.Context) (state.CheckResult, error) {
	if s.checkFn == nil {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        "no check function defined, assuming change needed",
		}, nil
	}

	thread, stop := s.newThread(ctx)
	defer stop()
	configVal, err := GoToStarlark(s.config)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("starmod: %s: convert config: %w", s.moduleName, err)
	}

	result, err := starlark.Call(thread, s.checkFn, starlark.Tuple{
		starlark.String(s.id),
		configVal,
	}, nil)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("starmod: %s: check: %w", s.moduleName, err)
	}

	return DictToCheckResult(result)
}

// Apply makes the system match the desired state by calling the user's apply function.
func (s *StarlarkState) Apply(ctx context.Context) (state.ApplyResult, error) {
	thread, stop := s.newThread(ctx)
	defer stop()
	configVal, err := GoToStarlark(s.config)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("starmod: %s: convert config: %w", s.moduleName, err)
	}

	result, err := starlark.Call(thread, s.applyFn, starlark.Tuple{
		starlark.String(s.id),
		configVal,
	}, nil)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("starmod: %s: apply: %w", s.moduleName, err)
	}

	return DictToApplyResult(result)
}

// Revert undoes changes. If no _revert function is defined, returns a no-op.
func (s *StarlarkState) Revert(ctx context.Context) (state.ApplyResult, error) {
	if s.revertFn == nil {
		return state.ApplyResult{
			Changed: false,
			Diff:    "no revert function defined",
		}, nil
	}

	thread, stop := s.newThread(ctx)
	defer stop()
	configVal, err := GoToStarlark(s.config)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("starmod: %s: convert config: %w", s.moduleName, err)
	}

	result, err := starlark.Call(thread, s.revertFn, starlark.Tuple{
		starlark.String(s.id),
		configVal,
	}, nil)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("starmod: %s: revert: %w", s.moduleName, err)
	}

	return DictToApplyResult(result)
}

// newThread creates a fresh Starlark thread for one Check/Apply/Revert call:
// the "context" thread-local (consumed by blocking builtins such as sleep,
// http_*, cmd_run), the execution-step budget, and a context-cancellation
// watcher so `zester job cancel` / the job timeout interrupt pure-Starlark
// loops too. The caller MUST defer the returned stop func.
func (s *StarlarkState) newThread(ctx context.Context) (*starlark.Thread, func()) {
	thread := &starlark.Thread{
		Name: s.moduleName + ":" + s.id,
	}
	thread.SetLocal("context", ctx)
	limitThreadSteps(thread, s.maxSteps)
	return thread, cancelOnContext(ctx, thread)
}

// NewStarlarkBuilder creates a state.Builder for a Starlark-defined module.
// This is called by the Loader for each discovered apply function. Options
// (e.g. WithMaxExecutionSteps) apply to every state the builder produces.
func NewStarlarkBuilder(
	moduleName string,
	applyFn *starlark.Function,
	checkFn *starlark.Function,
	revertFn *starlark.Function,
	builtins starlark.StringDict,
	mctx *exec.ModuleContext,
	opts ...StateOption,
) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		s := &StarlarkState{
			moduleName: moduleName,
			id:         id,
			reqs:       state.ParseRequisites(config),
			config:     config,
			applyFn:    applyFn,
			checkFn:    checkFn,
			revertFn:   revertFn,
			builtins:   builtins,
			mctx:       mctx,
		}
		for _, opt := range opts {
			opt(s)
		}
		return s, nil
	}
}
