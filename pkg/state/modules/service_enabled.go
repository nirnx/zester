package modules

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// SvcEnabled implements the service.enabled state.
// It ensures a service is enabled to start at boot.
type SvcEnabled struct {
	id   string
	reqs state.Requisites

	Service string

	svc exec.ServiceExec

	// revert state tracked during Apply: armed ONLY when Apply actually
	// enabled the service, so a fresh-instance or no-op-Apply Revert never
	// disables a service this run did not touch.
	wasEnabled bool
}

// NewSvcEnabledBuilder returns a state.Builder that creates SvcEnabled states.
func NewSvcEnabledBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Service == nil {
			return nil, fmt.Errorf("service.enabled: no service provider available")
		}
		return newSvcEnabled(id, config, mctx.Service)
	}
}

func newSvcEnabled(id string, config map[string]any, svc exec.ServiceExec) (state.State, error) {
	s := &SvcEnabled{id: id, svc: svc}

	s.Service, _ = config["name"].(string)
	if s.Service == "" {
		s.Service = id
	}

	s.reqs = state.ParseRequisites(config)

	return s, nil
}

func (s *SvcEnabled) Name() string           { return "service.enabled:" + s.id }
func (s *SvcEnabled) Reqs() state.Requisites { return s.reqs }

func (s *SvcEnabled) Check(ctx context.Context) (state.CheckResult, error) {
	enabled, err := s.svc.IsEnabled(ctx, s.Service)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("service.enabled: check %s: %w", s.Service, err)
	}
	if enabled {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%s is not enabled", s.Service),
	}, nil
}

func (s *SvcEnabled) Apply(ctx context.Context) (state.ApplyResult, error) {
	// Self-contained full flow: a watch-forced Apply bypasses Check, and an
	// already-enabled service must be a clean no-op (and must not arm the
	// revert memo).
	enabled, err := s.svc.IsEnabled(ctx, s.Service)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("service.enabled: check %s: %w", s.Service, err)
	}
	if enabled {
		return state.ApplyResult{
			Changed: false,
			Diff:    fmt.Sprintf("%s already enabled", s.Service),
		}, nil
	}

	if err := s.svc.Enable(ctx, s.Service); err != nil {
		return state.ApplyResult{}, fmt.Errorf("service.enabled: enable %s: %w", s.Service, err)
	}
	s.wasEnabled = true
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("enabled %s", s.Service),
		Details: map[string]string{
			"service": s.Service,
			"manager": s.svc.Name(),
		},
	}, nil
}

func (s *SvcEnabled) Revert(ctx context.Context) (state.ApplyResult, error) {
	if !s.wasEnabled {
		return state.ApplyResult{
			Changed: false,
			Diff:    "nothing to revert (no apply recorded in this run)",
		}, nil
	}
	if err := s.svc.Disable(ctx, s.Service); err != nil {
		return state.ApplyResult{}, fmt.Errorf("service.enabled: revert disable %s: %w", s.Service, err)
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("disabled %s (revert enable)", s.Service),
	}, nil
}
