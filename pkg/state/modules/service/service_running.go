package servicemod

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// SvcRunning implements the service.running state.
// It ensures a service is started, and optionally enabled at boot.
// When triggered via watch, it restarts rather than starting the service.
//
// SvcRunning is also its own schema proto: the tagged exported Service and
// Enable fields ARE the module's parameter declaration (one schema declaration
// per module). Enable is a paramtypes.TriState — its Declared() bit replaces the
// legacy "Enable bool + hasEnable bool" pair, so an undeclared enable leaves the
// unit's boot enablement untouched while an explicit enable:false actively
// disables it. The unexported runtime fields are untagged, so the compiler skips
// them.
type SvcRunning struct {
	id   string
	reqs state.Requisites

	// Service is the service unit name; it defaults to the state ID.
	Service string `zester:"name,primary" usage:"service unit name (defaults to the state ID)"`
	// Enable is the three-valued boot-enablement intent: unset leaves the unit's
	// enablement alone, true enables it at boot, false actively disables it.
	Enable paramtypes.TriState `zester:"enable" usage:"boot-enablement intent: unset leaves it alone, true enables, false disables"`

	svc exec.ServiceExec

	// revert state tracked during Apply.
	wasStarted  bool
	wasEnabled  bool
	wasDisabled bool
}

// svcRunningSpec is the compiled schema + documentation for service.running. It
// is compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is drift-corrected against the
// live Check/Apply/Revert tri-state behavior.
var svcRunningSpec = regdef.MustSpec("service.running", modschema.KindState, SvcRunning{}, modschema.Doc{
	Summary: "Ensure a service is running, optionally managing its boot enablement.",
	Description: "`service.running` ensures the named service is started, and — when a watch " +
		"requisite force-applies it — restarts it instead. The service name defaults to the " +
		"state ID. The `enable` parameter is three-valued: omit it to leave the unit's boot " +
		"enablement untouched, set `enable: true` to also enable it at boot, or `enable: false` " +
		"to actively disable it. Converging an enable-only drift on an already-running service " +
		"does NOT restart it.",
	Effects: modschema.Effects{
		Check: "Reports a change when the service is not running. When `enable` is declared, it " +
			"also compares boot enablement and reports drift (not-enabled under `enable: true`, or " +
			"still-enabled under `enable: false`); an undeclared `enable` is never compared, so a " +
			"running service never churns over boot state it does not manage.",
		Apply: "Converges the declared `enable` facet first (enabling or disabling at boot as " +
			"declared). Then, if the service is not running, it starts it; if it is already running " +
			"and only the enable facet drifted, it enables/disables WITHOUT restarting; otherwise " +
			"(a watch-forced apply on a healthy service) it restarts. Reports the action taken and " +
			"the service manager in its details.",
		Revert: "Undoes only the actions this run's Apply recorded: it re-disables a service it " +
			"enabled, re-enables one it disabled, and stops one it started. A fresh instance (a " +
			"standalone revert) recorded nothing and is an explicit no-op — it never stops a service " +
			"it did not start.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Ensure a service is running",
			Kind:        "state",
			Explanation: "The service name defaults to the state ID.",
			Code:        "nginx:\n  service.running: []\n",
		},
		{
			Title:       "Run and enable at boot",
			Kind:        "state",
			Explanation: "enable: true also enables the unit at boot; an enable-only drift converges without restarting.",
			Code:        "sshd:\n  service.running:\n    - enable: true\n",
		},
		{
			Title:       "Restart on config change",
			Kind:        "state",
			Explanation: "A watch requisite force-applies the state, which restarts (rather than starts) the running service.",
			Code: "nginx:\n  service.running:\n    - watch:\n" +
				"      - \"file.managed:/etc/nginx/nginx.conf\"\n",
		},
		{
			Title:       "Ensure running ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the service name.",
			Code:        "zester '*' service.running nginx",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Enable-only drift does not restart",
			Body: "When a running service's only drift is boot enablement, Apply enables or " +
				"disables it in place — it never restarts a healthy service just to fix boot state.",
		},
		{
			Level: "info",
			Title: "enable is three-valued",
			Body: "Omitting enable leaves the unit's boot enablement untouched; enable: false " +
				"actively disables it. This is the tri-state semantic — \"not specified\" is " +
				"distinct from an explicit false.",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7"},
	SeeAlso:     []string{"service.dead", "service.enabled"},
})

// NewSvcRunningBuilder returns a state.Builder that creates SvcRunning states
// using the given ModuleContext's service provider. Decode policy (unknown-key
// handling, reserved keys) is threaded via opts; the peel supplies it through
// modules.RegisterAll.
func NewSvcRunningBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Service == nil {
			return nil, fmt.Errorf("service.running: no service provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it.
		s := &SvcRunning{}
		if _, err := svcRunningSpec.Decode(id, config, s, opts); err != nil {
			return nil, fmt.Errorf("service.running: %w", err)
		}
		s.id = id
		s.svc = mctx.Service
		s.reqs = state.ParseRequisites(config)
		return s, nil
	}
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

	if s.Enable.Declared() {
		enabled, err := s.svc.IsEnabled(ctx, s.Service)
		if err != nil {
			return state.CheckResult{}, fmt.Errorf("service.running: check enabled %s: %w", s.Service, err)
		}
		if enabled != s.Enable.Value() {
			if s.Enable.Value() {
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
	if s.Enable.Declared() {
		want := s.Enable.Value()
		enabled, err := s.svc.IsEnabled(ctx, s.Service)
		if err != nil {
			return state.ApplyResult{}, fmt.Errorf("service.running: check enabled %s: %w", s.Service, err)
		}
		if want && !enabled {
			if err := s.svc.Enable(ctx, s.Service); err != nil {
				return state.ApplyResult{}, fmt.Errorf("service.running: enable %s: %w", s.Service, err)
			}
			s.wasEnabled = true
			enableFixed = true
		}
		if !want && enabled {
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
