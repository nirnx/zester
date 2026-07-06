package modules

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// CronPresent implements the cron.present state.
// It ensures a crontab entry exists for the given user.
type CronPresent struct {
	id   string
	reqs state.Requisites

	Label    string
	User     string
	Minute   string
	Hour     string
	DayMonth string
	Month    string
	DayWeek  string
	Command  string

	cron exec.CronExec
}

// NewCronPresentBuilder returns a state.Builder that creates CronPresent states.
func NewCronPresentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Cron == nil {
			return nil, fmt.Errorf("cron.present: no cron provider available")
		}
		return newCronPresent(id, config, mctx.Cron)
	}
}

func newCronPresent(id string, config map[string]any, cron exec.CronExec) (state.State, error) {
	c := &CronPresent{id: id, cron: cron}

	c.Label, _ = config["name"].(string)
	if c.Label == "" {
		c.Label = id
	}

	c.User, _ = config["user"].(string)
	if c.User == "" {
		c.User = "root"
	}

	c.Command, _ = config["command"].(string)
	if c.Command == "" {
		return nil, fmt.Errorf("cron.present: %s: command is required", id)
	}

	c.Minute, _ = config["minute"].(string)
	if c.Minute == "" {
		c.Minute = "*"
	}
	c.Hour, _ = config["hour"].(string)
	if c.Hour == "" {
		c.Hour = "*"
	}
	c.DayMonth, _ = config["daymonth"].(string)
	if c.DayMonth == "" {
		c.DayMonth = "*"
	}
	c.Month, _ = config["month"].(string)
	if c.Month == "" {
		c.Month = "*"
	}
	c.DayWeek, _ = config["dayweek"].(string)
	if c.DayWeek == "" {
		c.DayWeek = "*"
	}

	c.reqs = state.ParseRequisites(config)
	return c, nil
}

func (c *CronPresent) Name() string           { return "cron.present:" + c.id }
func (c *CronPresent) Reqs() state.Requisites { return c.reqs }

func (c *CronPresent) Check(ctx context.Context) (state.CheckResult, error) {
	entries, err := c.cron.List(ctx, c.User)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("cron.present: list %s: %w", c.User, err)
	}
	for _, e := range entries {
		if e.Command == c.Command {
			if e.Minute == c.Minute && e.Hour == c.Hour &&
				e.DayOfMonth == c.DayMonth && e.Month == c.Month && e.DayOfWeek == c.DayWeek {
				return state.CheckResult{NeedsChange: false}, nil
			}
			return state.CheckResult{
				NeedsChange: true,
				Diff:        fmt.Sprintf("cron entry for %q exists but schedule differs", c.Command),
			}, nil
		}
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("cron entry for %q does not exist", c.Command),
	}, nil
}

func (c *CronPresent) Apply(ctx context.Context) (state.ApplyResult, error) {
	entry := exec.CronEntry{
		Minute:     c.Minute,
		Hour:       c.Hour,
		DayOfMonth: c.DayMonth,
		Month:      c.Month,
		DayOfWeek:  c.DayWeek,
		Command:    c.Command,
		Comment:    c.Label,
	}
	if err := c.cron.Set(ctx, c.User, entry); err != nil {
		return state.ApplyResult{}, fmt.Errorf("cron.present: set %s: %w", c.Command, err)
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("set cron entry %q for user %s", c.Command, c.User),
		Details: map[string]string{
			"user":     c.User,
			"command":  c.Command,
			"schedule": fmt.Sprintf("%s %s %s %s %s", c.Minute, c.Hour, c.DayMonth, c.Month, c.DayWeek),
		},
	}, nil
}

func (c *CronPresent) Revert(ctx context.Context) (state.ApplyResult, error) {
	if err := c.cron.Remove(ctx, c.User, c.Command); err != nil {
		return state.ApplyResult{}, fmt.Errorf("cron.present: revert remove %s: %w", c.Command, err)
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed cron entry %q (revert)", c.Command),
	}, nil
}

// CronAbsent implements the cron.absent state.
// It ensures a crontab entry does not exist for the given user.
type CronAbsent struct {
	id   string
	reqs state.Requisites

	CronName string
	User     string
	Command  string

	cron exec.CronExec
}

// NewCronAbsentBuilder returns a state.Builder that creates CronAbsent states.
func NewCronAbsentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Cron == nil {
			return nil, fmt.Errorf("cron.absent: no cron provider available")
		}
		return newCronAbsent(id, config, mctx.Cron)
	}
}

func newCronAbsent(id string, config map[string]any, cron exec.CronExec) (state.State, error) {
	c := &CronAbsent{id: id, cron: cron}

	c.CronName, _ = config["name"].(string)
	if c.CronName == "" {
		c.CronName = id
	}

	c.User, _ = config["user"].(string)
	if c.User == "" {
		c.User = "root"
	}

	c.Command, _ = config["command"].(string)
	if c.Command == "" {
		return nil, fmt.Errorf("cron.absent: %s: command is required", id)
	}

	c.reqs = state.ParseRequisites(config)
	return c, nil
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
