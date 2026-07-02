package starmod

import (
	"context"
	"fmt"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
	"go.starlark.net/starlark"
)

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

	thread := s.newThread(ctx)
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
	thread := s.newThread(ctx)
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

	thread := s.newThread(ctx)
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

// newThread creates a fresh Starlark thread with context and builtins.
func (s *StarlarkState) newThread(ctx context.Context) *starlark.Thread {
	thread := &starlark.Thread{
		Name: s.moduleName + ":" + s.id,
	}
	thread.SetLocal("context", ctx)
	return thread
}

// NewStarlarkBuilder creates a state.Builder for a Starlark-defined module.
// This is called by the Loader for each discovered apply function.
func NewStarlarkBuilder(
	moduleName string,
	applyFn *starlark.Function,
	checkFn *starlark.Function,
	revertFn *starlark.Function,
	builtins starlark.StringDict,
	mctx *exec.ModuleContext,
) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		return &StarlarkState{
			moduleName: moduleName,
			id:         id,
			reqs:       state.ParseRequisites(config),
			config:     config,
			applyFn:    applyFn,
			checkFn:    checkFn,
			revertFn:   revertFn,
			builtins:   builtins,
			mctx:       mctx,
		}, nil
	}
}
