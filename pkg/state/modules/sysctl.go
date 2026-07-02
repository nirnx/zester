package modules

import (
	"context"
	"fmt"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
)

// SysctlPresent implements the sysctl.present state.
// It ensures a kernel parameter is set to the desired value at runtime
// and optionally persisted across reboots.
type SysctlPresent struct {
	id   string
	reqs state.Requisites

	Key     string
	Value   string
	Persist bool

	sysctl exec.SysctlExec

	// original stores the value before Apply for Revert.
	original string
}

// NewSysctlPresentBuilder returns a state.Builder that creates SysctlPresent states.
func NewSysctlPresentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Sysctl == nil {
			return nil, fmt.Errorf("sysctl.present: no sysctl provider available")
		}
		return newSysctlPresent(id, config, mctx.Sysctl)
	}
}

func newSysctlPresent(id string, config map[string]any, sysctl exec.SysctlExec) (state.State, error) {
	s := &SysctlPresent{id: id, sysctl: sysctl}

	s.Key, _ = config["name"].(string)
	if s.Key == "" {
		s.Key = id
	}

	s.Value, _ = config["value"].(string)
	if s.Value == "" {
		return nil, fmt.Errorf("sysctl.present: %s: value is required", id)
	}

	s.Persist = true // default true
	if v, ok := config["persist"].(bool); ok {
		s.Persist = v
	}

	s.reqs = state.ParseRequisites(config)
	return s, nil
}

func (s *SysctlPresent) Name() string           { return "sysctl.present:" + s.id }
func (s *SysctlPresent) Reqs() state.Requisites { return s.reqs }

func (s *SysctlPresent) Check(ctx context.Context) (state.CheckResult, error) {
	current, err := s.sysctl.Get(ctx, s.Key)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("sysctl.present: get %s: %w", s.Key, err)
	}
	if current == s.Value {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%s: %q != %q", s.Key, current, s.Value),
	}, nil
}

func (s *SysctlPresent) Apply(ctx context.Context) (state.ApplyResult, error) {
	original, err := s.sysctl.Get(ctx, s.Key)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("sysctl.present: get %s: %w", s.Key, err)
	}
	s.original = original

	if err := s.sysctl.Set(ctx, s.Key, s.Value); err != nil {
		return state.ApplyResult{}, fmt.Errorf("sysctl.present: set %s: %w", s.Key, err)
	}

	if s.Persist {
		if err := s.sysctl.Persist(ctx, s.Key, s.Value); err != nil {
			return state.ApplyResult{}, fmt.Errorf("sysctl.present: persist %s: %w", s.Key, err)
		}
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("%s: %q -> %q", s.Key, original, s.Value),
		Details: map[string]string{
			"key":       s.Key,
			"value":     s.Value,
			"previous":  original,
			"persisted": fmt.Sprintf("%v", s.Persist),
		},
	}, nil
}

func (s *SysctlPresent) Revert(ctx context.Context) (state.ApplyResult, error) {
	if err := s.sysctl.Set(ctx, s.Key, s.original); err != nil {
		return state.ApplyResult{}, fmt.Errorf("sysctl.present: revert set %s: %w", s.Key, err)
	}
	if s.Persist {
		if err := s.sysctl.Persist(ctx, s.Key, s.original); err != nil {
			return state.ApplyResult{}, fmt.Errorf("sysctl.present: revert persist %s: %w", s.Key, err)
		}
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("%s: reverted to %q", s.Key, s.original),
	}, nil
}
