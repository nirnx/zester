package modules

import (
	"context"
	"fmt"
	"strings"

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

	// Revert memos — armed ONLY when Apply actually wrote the crontab.
	// original is the pre-Apply entry at this identity (nil when Apply
	// created a brand-new entry): a same-instance Revert restores it, and a
	// fresh instance (or converged Apply) reverts as a clean no-op instead
	// of blindly deleting by command.
	applied  bool
	original *exec.CronEntry
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

// desiredEntry builds the CronEntry this state manages. The Label rides in
// the Comment field — it is the entry's IDENTITY (Salt identifier semantics),
// so editing the command in the state replaces the old line instead of
// orphaning it in the crontab.
func (c *CronPresent) desiredEntry() exec.CronEntry {
	return exec.CronEntry{
		Minute:     c.Minute,
		Hour:       c.Hour,
		DayOfMonth: c.DayMonth,
		Month:      c.Month,
		DayOfWeek:  c.DayWeek,
		Command:    c.Command,
		Comment:    c.Label,
	}
}

// entryDiffs reports how e drifts from the desired entry. Empty means fully
// converged. Shared by Check and Apply so what Check compares is exactly what
// Apply enforces (and vice versa).
func (c *CronPresent) entryDiffs(e exec.CronEntry) []string {
	var diffs []string
	if e.Command != c.Command {
		diffs = append(diffs, fmt.Sprintf("command %q != %q", e.Command, c.Command))
	}
	if e.Minute != c.Minute || e.Hour != c.Hour ||
		e.DayOfMonth != c.DayMonth || e.Month != c.Month || e.DayOfWeek != c.DayWeek {
		diffs = append(diffs, "schedule differs")
	}
	if e.Comment != c.Label {
		// A label-less entry matched by command (adoption fallback): apply
		// stamps the identifier so future command edits replace, not orphan.
		diffs = append(diffs, fmt.Sprintf("entry is missing identifier comment %q", c.Label))
	}
	return diffs
}

func (c *CronPresent) Check(ctx context.Context) (state.CheckResult, error) {
	entries, err := c.cron.List(ctx, c.User)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("cron.present: list %s: %w", c.User, err)
	}

	desired := c.desiredEntry()
	i := exec.FindCronEntry(entries, desired)
	if i < 0 {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("cron entry %q does not exist", c.Label),
		}, nil
	}

	if diffs := c.entryDiffs(entries[i]); len(diffs) > 0 {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("cron entry %q: %s", c.Label, strings.Join(diffs, "; ")),
		}, nil
	}
	return state.CheckResult{NeedsChange: false}, nil
}

func (c *CronPresent) Apply(ctx context.Context) (state.ApplyResult, error) {
	// Self-contained full flow: a watch-forced Apply bypasses Check, and a
	// converged entry must be a clean no-op — not a lossy full-crontab
	// rewrite reported as a change.
	entries, err := c.cron.List(ctx, c.User)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("cron.present: list %s: %w", c.User, err)
	}

	entry := c.desiredEntry()
	i := exec.FindCronEntry(entries, entry)
	if i >= 0 {
		if len(c.entryDiffs(entries[i])) == 0 {
			return state.ApplyResult{
				Changed: false,
				Diff:    fmt.Sprintf("cron entry %q already converged", c.Label),
			}, nil
		}
		orig := entries[i]
		c.original = &orig
	}

	if err := c.cron.Set(ctx, c.User, entry); err != nil {
		return state.ApplyResult{}, fmt.Errorf("cron.present: set %s: %w", c.Command, err)
	}
	c.applied = true
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
	if !c.applied {
		// Fresh instance or converged Apply: nothing was written this run —
		// never delete by bare command (that would destroy pre-existing lines
		// and same-command entries owned by other labels).
		return state.ApplyResult{
			Changed: false,
			Diff:    "nothing to revert (no apply recorded in this run)",
		}, nil
	}

	// Apply replaced a prior entry under the SAME label: the label still
	// identifies the line, so a plain Set restores it in place.
	if c.original != nil && c.original.Comment == c.Label {
		if err := c.cron.Set(ctx, c.User, *c.original); err != nil {
			return state.ApplyResult{}, fmt.Errorf("cron.present: revert restore %s: %w", c.Command, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("restored cron entry %q (revert)", c.Label),
		}, nil
	}

	// Apply created a new labeled entry or adopted a label-less line: remove
	// OUR labeled line and, for an adoption, put the original back. Remove is
	// command-scoped (label-scoped removal is inexpressible via CronExec), so
	// same-command entries owned by other labels are preserved and re-added.
	entries, err := c.cron.List(ctx, c.User)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("cron.present: revert list %s: %w", c.User, err)
	}
	var labeled, unlabeled []exec.CronEntry
	found := false
	for _, e := range entries {
		if e.Command != c.Command {
			continue
		}
		if !found && e.Comment == c.Label {
			found = true // the line this Apply wrote — dropped (or replaced by original)
			continue
		}
		if e.Comment != "" {
			labeled = append(labeled, e)
		} else {
			unlabeled = append(unlabeled, e)
		}
	}
	if !found && c.original == nil {
		return state.ApplyResult{
			Changed: false,
			Diff:    fmt.Sprintf("cron entry %q no longer present; nothing to revert", c.Label),
		}, nil
	}

	if err := c.cron.Remove(ctx, c.User, c.Command); err != nil {
		return state.ApplyResult{}, fmt.Errorf("cron.present: revert remove %s: %w", c.Command, err)
	}
	// Re-add preserved entries: labeled ones first — a label-less Set matches
	// only label-less lines, so this order re-adds every line exactly once.
	for _, e := range labeled {
		if err := c.cron.Set(ctx, c.User, e); err != nil {
			return state.ApplyResult{}, fmt.Errorf("cron.present: revert re-add %q: %w", e.Comment, err)
		}
	}
	for _, e := range unlabeled {
		if err := c.cron.Set(ctx, c.User, e); err != nil {
			return state.ApplyResult{}, fmt.Errorf("cron.present: revert re-add %s: %w", e.Command, err)
		}
	}
	if c.original != nil {
		if err := c.cron.Set(ctx, c.User, *c.original); err != nil {
			return state.ApplyResult{}, fmt.Errorf("cron.present: revert restore %s: %w", c.Command, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("restored adopted cron entry for %q (revert)", c.Command),
		}, nil
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed cron entry %q (revert)", c.Label),
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
