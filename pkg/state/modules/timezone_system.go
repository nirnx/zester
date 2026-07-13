package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// TimezoneSystem implements the timezone.system state.
// It ensures the system timezone is set to the desired value.
type TimezoneSystem struct {
	id   string
	reqs state.Requisites

	// Timezone is the desired timezone (e.g. "America/New_York").
	Timezone string

	// UTC sets the hardware clock to UTC when true.
	UTC bool

	cmd  exec.CommandExec
	file exec.FileExec

	// previousTZ stores the timezone before Apply ran (for Revert).
	previousTZ string
}

// NewTimezoneSystemBuilder returns a state.Builder that creates TimezoneSystem states
// using the given ModuleContext's command and file providers.
func NewTimezoneSystemBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("timezone.system: no command provider available")
		}
		return newTimezoneSystem(id, config, mctx.Command, mctx.File)
	}
}

func newTimezoneSystem(id string, config map[string]any, cmd exec.CommandExec, file exec.FileExec) (state.State, error) {
	t := &TimezoneSystem{id: id, cmd: cmd, file: file}

	t.Timezone, _ = config["name"].(string)
	if t.Timezone == "" {
		t.Timezone = id
	}

	t.UTC, _ = config["utc"].(bool)

	t.reqs = state.ParseRequisites(config)
	return t, nil
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
