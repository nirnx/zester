package servicemod

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// SvcEnabled implements the service.enabled state.
// It ensures a service is enabled to start at boot.
//
// SvcEnabled is also its own schema proto: the single tagged exported field IS
// the module's parameter declaration (one schema declaration per module).
// `name` is the primary parameter (defaults to the state ID) — a numeric name
// now coerces to its string form and a composite name is rejected (BD-6), where
// the legacy `.(string)` assertion silently fell back to the state ID. The
// unexported runtime fields (id, reqs, svc, and the revert memo) are untagged,
// so the schema compiler skips them.
type SvcEnabled struct {
	id   string
	reqs state.Requisites

	// Service is the service name; it defaults to the state ID.
	Service string `zester:"name,primary" usage:"service name to enable at boot; defaults to the state ID"`

	svc exec.ServiceExec

	// revert state tracked during Apply: armed ONLY when Apply actually
	// enabled the service, so a fresh-instance or no-op-Apply Revert never
	// disables a service this run did not touch.
	wasEnabled bool
}

// svcEnabledSpec is the compiled schema + documentation for service.enabled. Its
// Doc is drift-corrected against the live Check/Apply/Revert behavior — notably
// the already-enabled Apply no-op (which does NOT arm the revert memo) and the
// standalone-revert clean no-op.
var svcEnabledSpec = regdef.MustSpec("service.enabled", modschema.KindState, SvcEnabled{}, modschema.Doc{
	Summary: "Ensure a service is enabled to start at boot.",
	Description: "`service.enabled` ensures a service (`name`, defaulting to the state ID) is enabled to " +
		"start at boot. It manages ONLY the boot-time enablement — it does not start, stop, or otherwise " +
		"affect whether the service is currently running (use `service.running` for that).",
	Effects: modschema.Effects{
		Check: "Reads the service's boot-enablement (via the service manager, e.g. `systemctl is-enabled " +
			"--quiet <service>`). Reports no change when it is already enabled, and a change when it is not.",
		Apply: "Re-reads the enablement (a self-contained flow: a watch-forced Apply bypasses Check). An " +
			"already-enabled service is a clean no-op that does NOT arm the revert memo. Otherwise it enables " +
			"the service and records that this run did so, reporting the service name and manager in its " +
			"details.",
		Revert: "Disables the service ONLY when this run's Apply actually enabled it (the revert memo). A " +
			"fresh instance (a standalone revert) or a no-op Apply recorded nothing and is an explicit clean " +
			"no-op — it never disables a service this run did not enable.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Enable a service at boot",
			Kind:        "state",
			Explanation: "The service name defaults to the state ID.",
			Code:        "sshd:\n  service.enabled: []\n",
		},
		{
			Title:       "Enable a service under a descriptive ID",
			Kind:        "state",
			Explanation: "name selects the managed service when the state ID is not the service name.",
			Code:        "enable-nginx-boot:\n  service.enabled:\n    - name: nginx\n",
		},
		{
			Title:       "Enable a service ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the service name.",
			Code:        "zester 'web*' service.enabled nginx",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Boot enablement only, not running state",
			Body: "`service.enabled` manages only whether the service starts at boot; it never starts or " +
				"stops the service. Pair it with `service.running` (which can also enable) when the service " +
				"must be both running now and enabled at boot.",
		},
	},
	Divergences: []string{"BD-6"},
	SeeAlso:     []string{"service.running", "service.dead"},
})

// NewSvcEnabledBuilder returns a state.Builder that creates SvcEnabled states.
// Decode policy (unknown-key handling, reserved keys) is threaded via opts; the
// peel supplies it through modules.RegisterAll.
func NewSvcEnabledBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Service == nil {
			return nil, fmt.Errorf("service.enabled: no service provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it.
		s := &SvcEnabled{}
		if _, err := svcEnabledSpec.Decode(id, config, s, opts); err != nil {
			return nil, fmt.Errorf("service.enabled: %w", err)
		}
		s.id = id
		s.svc = mctx.Service
		s.reqs = state.ParseRequisites(config)
		return s, nil
	}
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
