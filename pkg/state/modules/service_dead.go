package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	"github.com/nirnx/zester/pkg/state"
)

// SvcDead implements the service.dead state.
// It ensures a service is stopped and optionally disabled at boot.
//
// SvcDead is also its own schema proto: the tagged Service and Enable fields are
// its parameter declaration. Enable is a paramtypes.TriState with an INVERTED
// default versus service.running — enablement defaults to "leave it alone", and
// only an explicit enable:false also disables the unit. The derived
// DisableOnApply captures that inversion (computed in the builder as
// Enable.Declared() && !Enable.Value()); it is exported for test/contract
// projection but untagged, so the schema compiler skips it — it is not a user
// parameter.
type SvcDead struct {
	id   string
	reqs state.Requisites

	// Service is the service unit name; it defaults to the state ID.
	Service string `zester:"name,primary" usage:"service unit name (defaults to the state ID)"`
	// Enable is the three-valued boot-enablement intent. service.dead inverts the
	// default: an unset OR true enable leaves the unit's enablement alone; only
	// enable:false also disables the unit at boot.
	Enable paramtypes.TriState `zester:"enable" usage:"boot-enablement intent: enable:false also disables the unit; unset or true leaves it alone"`

	// DisableOnApply is the derived facet: true iff enable was declared false.
	DisableOnApply bool

	svc exec.ServiceExec

	// revert state tracked during Apply: armed ONLY for the actions Apply
	// really performed, so a fresh-instance or no-op-Apply Revert never
	// starts the very service this state declares dead.
	wasStopped  bool
	wasDisabled bool
}

// svcDeadSpec is the compiled schema + documentation for service.dead. Its Doc
// is drift-corrected against the live inverted-default tri-state behavior.
var svcDeadSpec = mustSpec("service.dead", modschema.KindState, SvcDead{}, modschema.Doc{
	Summary: "Ensure a service is stopped, optionally disabling it at boot.",
	Description: "`service.dead` ensures the named service is stopped. The service name defaults " +
		"to the state ID. Its `enable` parameter inverts service.running's default: omitting it " +
		"(or `enable: true`) stops the service but leaves its boot enablement untouched, while " +
		"`enable: false` ALSO disables it at boot — the common case for retiring a unit so it does " +
		"not resurrect on the next reboot.",
	Effects: modschema.Effects{
		Check: "Reports a change when the service is running. When `enable: false` is declared, it " +
			"also reports a stopped-but-still-enabled unit as drift (it would resurrect at the next " +
			"boot); an unset or true enable never compares boot state.",
		Apply: "Stops the service if it is running. When `enable: false` is declared and the unit " +
			"is still enabled, it also disables it at boot. An already stopped-and-converged service " +
			"is a clean no-op. Reports the service manager in its details.",
		Revert: "Undoes only the actions this run's Apply recorded: it re-enables a unit it " +
			"disabled and restarts one it stopped. A fresh instance (a standalone revert) recorded " +
			"nothing and is an explicit no-op — it never starts the very service the state declares " +
			"dead.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Stop a service, leave boot state alone",
			Kind:        "state",
			Explanation: "With enable unset, only the running state is managed.",
			Code:        "apache2:\n  service.dead: []\n",
		},
		{
			Title:       "Stop and disable at boot",
			Kind:        "state",
			Explanation: "enable: false is service.dead's inversion — it also disables the unit so it does not resurrect on reboot.",
			Code:        "apache2:\n  service.dead:\n    - enable: false\n",
		},
		{
			Title: "Stop a conflicting service before starting a replacement",
			Kind:  "state",
			Explanation: "require_in orders this service.dead ahead of the replacement's service.running: apache2 is " +
				"stopped and disabled at boot before nginx starts, so the two never contend for the same port.",
			Code: "apache2:\n  service.dead:\n    - enable: false\n    - require_in:\n" +
				"      - \"service.running:nginx\"\n",
		},
		{
			Title:       "Stop a service ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the service name.",
			Code:        "zester '*' service.dead apache2",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Inverted enable default",
			Body: "Unlike service.running, service.dead leaves boot enablement alone by default. " +
				"Only an explicit enable: false disables the unit at boot.",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7"},
	SeeAlso:     []string{"service.running", "service.enabled"},
})

// NewSvcDeadBuilder returns a state.Builder that creates SvcDead states using the
// given ModuleContext's service provider. Decode policy is threaded via opts.
func NewSvcDeadBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Service == nil {
			return nil, fmt.Errorf("service.dead: no service provider available")
		}
		s := &SvcDead{}
		if _, err := svcDeadSpec.Decode(id, config, s, opts); err != nil {
			return nil, fmt.Errorf("service.dead: %w", err)
		}
		// enable defaults to true in service.dead — only an explicit enable:false
		// also disables the service at boot (the inverted default). Compute the
		// derived facet from the decoded tri-state.
		s.DisableOnApply = s.Enable.Declared() && !s.Enable.Value()
		s.id = id
		s.svc = mctx.Service
		s.reqs = state.ParseRequisites(config)
		return s, nil
	}
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
