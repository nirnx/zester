package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// SvcRunning implements the service.running state.
// It ensures a service is started, and optionally enabled at boot.
// When triggered via watch, it restarts rather than starting the service.
type SvcRunning struct {
	id   string
	reqs state.Requisites

	Service string
	Enable  bool

	// hasEnable records whether "enable" was declared in the state config —
	// the enable facet is compared and enforced ONLY when declared, so
	// undeclared states never churn on boot-enablement.
	hasEnable bool

	svc exec.ServiceExec

	// revert state tracked during Apply.
	wasStarted  bool
	wasEnabled  bool
	wasDisabled bool
}

// NewSvcRunningBuilder returns a state.Builder that creates SvcRunning states.
func NewSvcRunningBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Service == nil {
			return nil, fmt.Errorf("service.running: no service provider available")
		}
		return newSvcRunning(id, config, mctx.Service)
	}
}

func newSvcRunning(id string, config map[string]any, svc exec.ServiceExec) (state.State, error) {
	s := &SvcRunning{id: id, svc: svc}

	s.Service, _ = config["name"].(string)
	if s.Service == "" {
		s.Service = id
	}

	if v, ok := config["enable"].(bool); ok {
		s.Enable = v
		s.hasEnable = true
	}

	s.reqs = state.ParseRequisites(config)

	return s, nil
}

func (s *SvcRunning) Name() string           { return "service.running:" + s.id }
func (s *SvcRunning) Reqs() state.Requisites { return s.reqs }

func (s *SvcRunning) Check(ctx context.Context) (state.CheckResult, error) {
	running, err := s.svc.IsRunning(ctx, s.Service)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("service.running: check %s: %w", s.Service, err)
	}

	var diffs []string
	if !running {
		diffs = append(diffs, fmt.Sprintf("%s is not running", s.Service))
	}

	if s.hasEnable {
		enabled, err := s.svc.IsEnabled(ctx, s.Service)
		if err != nil {
			return state.CheckResult{}, fmt.Errorf("service.running: check enabled %s: %w", s.Service, err)
		}
		if enabled != s.Enable {
			if s.Enable {
				diffs = append(diffs, fmt.Sprintf("%s is not enabled at boot", s.Service))
			} else {
				diffs = append(diffs, fmt.Sprintf("%s is enabled at boot and should not be", s.Service))
			}
		}
	}

	if len(diffs) > 0 {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        strings.Join(diffs, "; "),
		}, nil
	}
	return state.CheckResult{NeedsChange: false}, nil
}

func (s *SvcRunning) Apply(ctx context.Context) (state.ApplyResult, error) {
	running, err := s.svc.IsRunning(ctx, s.Service)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("service.running: check %s: %w", s.Service, err)
	}

	// Converge the declared enable facet first so we know whether this Apply
	// was reached to fix boot enablement (in which case restarting a running
	// service would be gratuitous disruption).
	enableFixed := false
	if s.hasEnable {
		enabled, err := s.svc.IsEnabled(ctx, s.Service)
		if err != nil {
			return state.ApplyResult{}, fmt.Errorf("service.running: check enabled %s: %w", s.Service, err)
		}
		if s.Enable && !enabled {
			if err := s.svc.Enable(ctx, s.Service); err != nil {
				return state.ApplyResult{}, fmt.Errorf("service.running: enable %s: %w", s.Service, err)
			}
			s.wasEnabled = true
			enableFixed = true
		}
		if !s.Enable && enabled {
			if err := s.svc.Disable(ctx, s.Service); err != nil {
				return state.ApplyResult{}, fmt.Errorf("service.running: disable %s: %w", s.Service, err)
			}
			s.wasDisabled = true
			enableFixed = true
		}
	}

	var action string
	switch {
	case !running:
		if err := s.svc.Start(ctx, s.Service); err != nil {
			return state.ApplyResult{}, fmt.Errorf("service.running: start %s: %w", s.Service, err)
		}
		s.wasStarted = true
		action = "started"
	case enableFixed:
		// Running, and this Apply converged the enable facet — the runner's
		// Check gate reached Apply for that drift; restarting would be
		// needless disruption.
		action = "enabled"
		if s.wasDisabled {
			action = "disabled"
		}
	default:
		// Already running and nothing else drifted — a watch-forced apply
		// (watched config changed): restart per the watch contract.
		if err := s.svc.Restart(ctx, s.Service); err != nil {
			return state.ApplyResult{}, fmt.Errorf("service.running: restart %s: %w", s.Service, err)
		}
		action = "restarted"
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("%s %s", action, s.Service),
		Details: map[string]string{
			"service": s.Service,
			"action":  action,
			"manager": s.svc.Name(),
		},
	}, nil
}

func (s *SvcRunning) Revert(ctx context.Context) (state.ApplyResult, error) {
	var acts []string
	if s.wasEnabled {
		if err := s.svc.Disable(ctx, s.Service); err != nil {
			return state.ApplyResult{}, fmt.Errorf("service.running: revert disable %s: %w", s.Service, err)
		}
		acts = append(acts, "disabled")
	}
	if s.wasDisabled {
		if err := s.svc.Enable(ctx, s.Service); err != nil {
			return state.ApplyResult{}, fmt.Errorf("service.running: revert enable %s: %w", s.Service, err)
		}
		acts = append(acts, "re-enabled")
	}
	if s.wasStarted {
		if err := s.svc.Stop(ctx, s.Service); err != nil {
			return state.ApplyResult{}, fmt.Errorf("service.running: revert stop %s: %w", s.Service, err)
		}
		acts = append(acts, "stopped")
	}
	if len(acts) == 0 {
		return state.ApplyResult{
			Changed: false,
			Diff:    "nothing to revert (no apply recorded in this run)",
		}, nil
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("%s %s (revert)", strings.Join(acts, ", "), s.Service),
	}, nil
}

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

// SvcDead implements the service.dead state.
// It ensures a service is stopped and optionally disabled at boot.
type SvcDead struct {
	id   string
	reqs state.Requisites

	Service        string
	DisableOnApply bool // when true, also disable the service at boot

	svc exec.ServiceExec

	// revert state tracked during Apply: armed ONLY for the actions Apply
	// really performed, so a fresh-instance or no-op-Apply Revert never
	// starts the very service this state declares dead.
	wasStopped  bool
	wasDisabled bool
}

// NewSvcDeadBuilder returns a state.Builder that creates SvcDead states.
func NewSvcDeadBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Service == nil {
			return nil, fmt.Errorf("service.dead: no service provider available")
		}
		return newSvcDead(id, config, mctx.Service)
	}
}

func newSvcDead(id string, config map[string]any, svc exec.ServiceExec) (state.State, error) {
	s := &SvcDead{id: id, svc: svc}

	s.Service, _ = config["name"].(string)
	if s.Service == "" {
		s.Service = id
	}

	// enable defaults to true in service.dead — if "enable" is explicitly false,
	// also disable the service at boot.
	enableVal, ok := config["enable"].(bool)
	if ok && !enableVal {
		s.DisableOnApply = true
	}

	s.reqs = state.ParseRequisites(config)

	return s, nil
}

func (s *SvcDead) Name() string           { return "service.dead:" + s.id }
func (s *SvcDead) Reqs() state.Requisites { return s.reqs }

func (s *SvcDead) Check(ctx context.Context) (state.CheckResult, error) {
	running, err := s.svc.IsRunning(ctx, s.Service)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("service.dead: check %s: %w", s.Service, err)
	}

	var diffs []string
	if running {
		diffs = append(diffs, fmt.Sprintf("%s is running and should be dead", s.Service))
	}

	if s.DisableOnApply {
		enabled, err := s.svc.IsEnabled(ctx, s.Service)
		if err != nil {
			return state.CheckResult{}, fmt.Errorf("service.dead: check enabled %s: %w", s.Service, err)
		}
		if enabled {
			// A stopped-but-still-enabled unit resurrects at the next boot —
			// the declared enable:false facet must converge now, not after
			// the post-reboot run notices it running.
			diffs = append(diffs, fmt.Sprintf("%s is enabled at boot and should be disabled", s.Service))
		}
	}

	if len(diffs) > 0 {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        strings.Join(diffs, "; "),
		}, nil
	}
	return state.CheckResult{NeedsChange: false}, nil
}

func (s *SvcDead) Apply(ctx context.Context) (state.ApplyResult, error) {
	running, err := s.svc.IsRunning(ctx, s.Service)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("service.dead: check %s: %w", s.Service, err)
	}

	var acts []string
	if running {
		if err := s.svc.Stop(ctx, s.Service); err != nil {
			return state.ApplyResult{}, fmt.Errorf("service.dead: stop %s: %w", s.Service, err)
		}
		s.wasStopped = true
		acts = append(acts, "stopped")
	}

	if s.DisableOnApply {
		enabled, err := s.svc.IsEnabled(ctx, s.Service)
		if err != nil {
			return state.ApplyResult{}, fmt.Errorf("service.dead: check enabled %s: %w", s.Service, err)
		}
		if enabled {
			if err := s.svc.Disable(ctx, s.Service); err != nil {
				return state.ApplyResult{}, fmt.Errorf("service.dead: disable %s: %w", s.Service, err)
			}
			s.wasDisabled = true
			acts = append(acts, "disabled")
		}
	}

	if len(acts) == 0 {
		// Self-contained no-op: a watch-forced Apply on an already-converged
		// service must not report a change it did not make.
		return state.ApplyResult{
			Changed: false,
			Diff:    fmt.Sprintf("%s already stopped", s.Service),
		}, nil
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("%s %s", strings.Join(acts, ", "), s.Service),
		Details: map[string]string{
			"service": s.Service,
			"manager": s.svc.Name(),
		},
	}, nil
}

func (s *SvcDead) Revert(ctx context.Context) (state.ApplyResult, error) {
	var acts []string
	if s.wasDisabled {
		if err := s.svc.Enable(ctx, s.Service); err != nil {
			return state.ApplyResult{}, fmt.Errorf("service.dead: revert enable %s: %w", s.Service, err)
		}
		acts = append(acts, "re-enabled")
	}
	if s.wasStopped {
		if err := s.svc.Start(ctx, s.Service); err != nil {
			return state.ApplyResult{}, fmt.Errorf("service.dead: revert start %s: %w", s.Service, err)
		}
		acts = append(acts, "started")
	}
	if len(acts) == 0 {
		return state.ApplyResult{
			Changed: false,
			Diff:    "nothing to revert (no apply recorded in this run)",
		}, nil
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("%s %s (revert)", strings.Join(acts, ", "), s.Service),
	}, nil
}
