package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// TimezoneSystem implements the timezone.system state.
// It ensures the system timezone is set to the desired value.
//
// TimezoneSystem is also its own schema proto: the tagged exported fields ARE
// the module's parameter declaration (one schema declaration per module) —
// both are primitives (a string primary and a bool), so no semantic type is
// needed. Under the uniform decoder a numeric `name` coerces to its string
// form and a composite is rejected (BD-6); `utc` honors an integer 1/0 (BD-7)
// and a truthy/falsy string (BD-2) where the legacy `.(bool)` assertion
// silently dropped them. The unexported runtime fields (id, reqs, cmd, file,
// revert memo) are untagged, so the schema compiler skips them.
type TimezoneSystem struct {
	id   string
	reqs state.Requisites

	// Timezone is the desired timezone (e.g. "America/New_York"); it defaults
	// to the state ID.
	Timezone string `zester:"name,primary" usage:"desired timezone (e.g. America/New_York, UTC); defaults to the state ID"`

	// UTC sets the hardware clock to UTC when true.
	UTC bool `zester:"utc" usage:"set the hardware clock to UTC (timedatectl set-local-rtc 0); a boolean that also accepts the integers 1 (true) and 0 (false)"`

	cmd  exec.CommandExec
	file exec.FileExec

	// previousTZ stores the timezone before Apply ran (for Revert).
	previousTZ string
}

// timezoneSystemSpec is the compiled schema + documentation for
// timezone.system. It is compiled once at package init and executed by every
// decode path (the builder below, and Registry.Parse). The prose is verified
// against the live Check/Apply/Revert behavior.
var timezoneSystemSpec = mustSpec("timezone.system", modschema.KindState, TimezoneSystem{}, modschema.Doc{
	Summary: "Ensure the system timezone is set to a desired value.",
	Description: "`timezone.system` ensures the system timezone (`name`, defaulting to the state ID) is " +
		"set. It uses `timedatectl` as the primary mechanism, falling back to writing `/etc/timezone` and " +
		"invoking `dpkg-reconfigure` on systems without it (non-systemd). `utc` additionally sets the " +
		"hardware clock to UTC.",
	Effects: modschema.Effects{
		Check: "Reads the current timezone via `timedatectl show --property=Timezone --value`, falling " +
			"back to `/etc/timezone` when `timedatectl` fails or is unavailable. Reports a change when the " +
			"current value does not match `name`.",
		Apply: "Reads and remembers the current timezone (for Revert), then runs `timedatectl set-timezone " +
			"<name>`. If that fails, it writes `<name>` to `/etc/timezone` and runs `dpkg-reconfigure -f " +
			"noninteractive tzdata` instead. When `utc` is set, it additionally runs `timedatectl " +
			"set-local-rtc 0`. Reports the timezone and its previous value in its details.",
		Revert: "Restores the timezone that was active immediately before this run's Apply, via " +
			"`timedatectl set-timezone <previous>`. If Apply was never called on this instance the previous " +
			"timezone is unknown, and Revert is an explicit no-op rather than a guess.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Set the timezone to UTC",
			Kind:        "state",
			Explanation: "The timezone defaults to the state ID.",
			Code:        "UTC:\n  timezone.system: []\n",
		},
		{
			Title:       "Set a regional timezone and sync the hardware clock",
			Kind:        "state",
			Explanation: "utc: true additionally sets the hardware clock to UTC.",
			Code: "America/Chicago:\n  timezone.system:\n    - utc: true\n" +
				"    - require:\n      - pkg.installed:tzdata\n",
		},
		{
			Title:       "Use an explicit name parameter",
			Kind:        "state",
			Explanation: "name overrides the state ID as the desired timezone.",
			Code:        "system-timezone:\n  timezone.system:\n    - name: Europe/Berlin\n",
		},
		{
			Title:       "Set the timezone ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the timezone string.",
			Code:        "zester '*' timezone.system America/New_York",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Timezone strings are IANA names",
			Body: "`name` must match an entry in the IANA Time Zone Database (e.g. `America/New_York`, " +
				"not `EST`).",
		},
		{
			Level: "info",
			Title: "Non-systemd fallback",
			Body: "On systems without `timedatectl` (non-systemd), Apply falls back to writing " +
				"`/etc/timezone` and invoking `dpkg-reconfigure -f noninteractive tzdata`; the " +
				"`dpkg-reconfigure` binary must be available there.",
		},
		{
			Level: "info",
			Title: "utc only affects the hardware clock",
			Body: "The `utc` parameter only controls whether the hardware clock is set to UTC " +
				"(`timedatectl set-local-rtc 0`); it does not change the timezone string itself.",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7"},
})

// NewTimezoneSystemBuilder returns a state.Builder that creates TimezoneSystem
// states using the given ModuleContext's command and file providers. Decode
// policy (unknown-key handling, reserved keys) is threaded via opts; the peel
// supplies it through modules.RegisterAll.
func NewTimezoneSystemBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("timezone.system: no command provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected providers, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		t := &TimezoneSystem{}
		if _, err := timezoneSystemSpec.Decode(id, config, t, opts); err != nil {
			return nil, fmt.Errorf("timezone.system: %w", err)
		}
		t.id = id
		t.cmd = mctx.Command
		t.file = mctx.File
		t.reqs = state.ParseRequisites(config)
		return t, nil
	}
}

func (t *TimezoneSystem) Name() string           { return "timezone.system:" + t.id }
func (t *TimezoneSystem) Reqs() state.Requisites { return t.reqs }

func (t *TimezoneSystem) currentTZ(ctx context.Context) (string, error) {
	result, err := t.cmd.Run(ctx, exec.CommandOpts{
		Command: "timedatectl",
		Args:    []string{"show", "--property=Timezone", "--value"},
	})
	if err == nil && result != nil && result.ExitCode == 0 {
		return strings.TrimSpace(result.Stdout), nil
	}

	// Fallback: read /etc/timezone.
	if t.file != nil {
		data, readErr := t.file.ReadFile(ctx, "/etc/timezone")
		if readErr == nil {
			return strings.TrimSpace(string(data)), nil
		}
	}

	return "", fmt.Errorf("timezone.system: could not determine current timezone")
}

func (t *TimezoneSystem) Check(ctx context.Context) (state.CheckResult, error) {
	current, err := t.currentTZ(ctx)
	if err != nil {
		return state.CheckResult{}, err
	}

	if current == t.Timezone {
		return state.CheckResult{
			NeedsChange: false,
			Diff:        fmt.Sprintf("timezone is already %s", t.Timezone),
		}, nil
	}

	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("timezone mismatch: current=%q, desired=%q", current, t.Timezone),
	}, nil
}

func (t *TimezoneSystem) Apply(ctx context.Context) (state.ApplyResult, error) {
	current, err := t.currentTZ(ctx)
	if err == nil {
		t.previousTZ = current
	}

	// Try timedatectl first.
	result, err := t.cmd.Run(ctx, exec.CommandOpts{
		Command: "timedatectl",
		Args:    []string{"set-timezone", t.Timezone},
	})
	if err != nil || (result != nil && result.ExitCode != 0) {
		// Fallback: write /etc/timezone and reconfigure.
		if t.file != nil {
			if writeErr := t.file.WriteFile(ctx, "/etc/timezone", []byte(t.Timezone+"\n"), 0644); writeErr != nil {
				return state.ApplyResult{}, fmt.Errorf("timezone.system: write /etc/timezone: %w", writeErr)
			}
		}
		if _, reconfigErr := t.cmd.Run(ctx, exec.CommandOpts{
			Command: "dpkg-reconfigure",
			Args:    []string{"-f", "noninteractive", "tzdata"},
		}); reconfigErr != nil {
			return state.ApplyResult{}, fmt.Errorf("timezone.system: dpkg-reconfigure: %w", reconfigErr)
		}
	}

	if t.UTC {
		if _, utcErr := t.cmd.Run(ctx, exec.CommandOpts{
			Command: "timedatectl",
			Args:    []string{"set-local-rtc", "0"},
		}); utcErr != nil {
			return state.ApplyResult{}, fmt.Errorf("timezone.system: set-local-rtc: %w", utcErr)
		}
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("set timezone from %q to %q", t.previousTZ, t.Timezone),
		Details: map[string]string{
			"timezone": t.Timezone,
			"previous": t.previousTZ,
		},
	}, nil
}

func (t *TimezoneSystem) Revert(ctx context.Context) (state.ApplyResult, error) {
	if t.previousTZ == "" {
		return state.ApplyResult{
			Changed: false,
			Diff:    "timezone.system: previous timezone unknown; cannot revert",
		}, nil
	}

	if _, err := t.cmd.Run(ctx, exec.CommandOpts{
		Command: "timedatectl",
		Args:    []string{"set-timezone", t.previousTZ},
	}); err != nil {
		return state.ApplyResult{}, fmt.Errorf("timezone.system: revert to %s: %w", t.previousTZ, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("reverted timezone from %q to %q", t.Timezone, t.previousTZ),
	}, nil
}
