package modules

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// CronAbsent implements the cron.absent state.
// It ensures a crontab entry does not exist for the given user.
//
// CronAbsent is also its own schema proto: the tagged CronName (primary), User
// (eager default root), and Command (required) fields ARE its parameter
// declaration. The unexported runtime fields are untagged.
type CronAbsent struct {
	id   string
	reqs state.Requisites

	CronName string `zester:"name,primary" usage:"label for the state; defaults to the state ID"`
	User     string `zester:"user,default=root" usage:"user whose crontab is managed; defaults to root"`
	Command  string `zester:"command,required" usage:"command line whose crontab entry is removed (required)"`

	cron exec.CronExec
}

// cronAbsentSpec is the compiled schema + documentation for cron.absent.
var cronAbsentSpec = mustSpec("cron.absent", modschema.KindState, CronAbsent{}, modschema.Doc{
	Summary: "Ensure a crontab entry does not exist for a user.",
	Description: "`cron.absent` ensures no crontab entry with the given `command` exists in the target " +
		"user's crontab. `command` is required and identifies the line to remove; `user` defaults to " +
		"`root`. The `name` parameter is a label for the state only (defaulting to the state ID) — " +
		"removal matches on the command, not the label.",
	Effects: modschema.Effects{
		Check: "Lists the user's crontab and reports a change when any entry's command matches the " +
			"declared command; otherwise it reports no change.",
		Apply: "Removes the crontab entry matching the command through the cron provider. Reports the " +
			"user and command in its details.",
		Revert: "Cannot restore a deleted crontab entry (the removed line's schedule is not recorded), " +
			"so Revert is an explicit no-op rather than a guessed re-creation.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Remove a legacy backup job",
			Kind:        "state",
			Explanation: "The entry is identified by its command.",
			Code:        "remove-legacy-backup:\n  cron.absent:\n    - user: root\n    - command: /usr/local/bin/old_backup.sh\n",
		},
		{
			Title:       "Remove a cron entry ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the state's name; command is a key=value.",
			Code:        "zester '*' cron.absent old-job command=/usr/local/bin/old_backup.sh",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Matched by command, not name",
			Body: "`cron.absent` removes the crontab line whose command equals the declared `command`. " +
				"The `name` parameter labels the state; it does not select which line is removed.",
		},
	},
	Divergences: []string{"BD-6"},
	SeeAlso:     []string{"cron.present"},
})

// NewCronAbsentBuilder returns a state.Builder that creates CronAbsent states
// using the given ModuleContext's cron provider. Decode policy is threaded via
// opts; the peel supplies it through modules.RegisterAll.
func NewCronAbsentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Cron == nil {
			return nil, fmt.Errorf("cron.absent: no cron provider available")
		}
		c := &CronAbsent{}
		if _, err := cronAbsentSpec.Decode(id, config, c, opts); err != nil {
			return nil, fmt.Errorf("cron.absent: %w", err)
		}
		c.id = id
		c.cron = mctx.Cron
		c.reqs = state.ParseRequisites(config)
		return c, nil
	}
}

func (c *CronAbsent) Name() string           { return "cron.absent:" + c.id }
func (c *CronAbsent) Reqs() state.Requisites { return c.reqs }

func (c *CronAbsent) Check(ctx context.Context) (state.CheckResult, error) {
	entries, err := c.cron.List(ctx, c.User)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("cron.absent: list %s: %w", c.User, err)
	}
	for _, e := range entries {
		if e.Command == c.Command {
			return state.CheckResult{
				NeedsChange: true,
				Diff:        fmt.Sprintf("cron entry for %q exists and should be absent", c.Command),
			}, nil
		}
	}
	return state.CheckResult{NeedsChange: false}, nil
}

func (c *CronAbsent) Apply(ctx context.Context) (state.ApplyResult, error) {
	if err := c.cron.Remove(ctx, c.User, c.Command); err != nil {
		return state.ApplyResult{}, fmt.Errorf("cron.absent: remove %s: %w", c.Command, err)
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed cron entry %q for user %s", c.Command, c.User),
		Details: map[string]string{"user": c.User, "command": c.Command},
	}, nil
}

func (c *CronAbsent) Revert(_ context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{
		Changed: false,
		Diff:    "cannot revert cron entry deletion",
	}, nil
}
