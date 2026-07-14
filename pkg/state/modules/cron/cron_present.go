package cronmod

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// CronPresent implements the cron.present state.
// It ensures a crontab entry exists for the given user.
//
// CronPresent is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). The
// schedule fields (minute/hour/daymonth/month/dayweek) carry EAGER `default=*`
// literals and user an eager `default=root`, reproducing the legacy
// construction-time defaults — but through the uniform decoder, so a YAML or
// msgpack INTEGER (`minute: 5`) now coerces to its string form ("5") instead of
// being dropped to "" and then to "*" by the legacy `.(string)` assertion (the
// every-minute bug, BD-3). command is `required`. The unexported runtime fields
// (id, reqs, cron, revert memos) are untagged, so the schema compiler skips them.
type CronPresent struct {
	id   string
	reqs state.Requisites

	// Label is the identifier comment stamped on the crontab line (its identity);
	// it defaults to the state ID.
	Label string `zester:"name,primary" usage:"identifier comment stamped on the crontab line (its Salt-identifier identity); defaults to the state ID"`
	// User/Command: cron.* family target component.
	cronTargetParam
	// Minute..DayWeek are the five schedule fields; each defaults to *.
	Minute   string `zester:"minute,default=*" usage:"minute field (0-59 or *); defaults to *"`
	Hour     string `zester:"hour,default=*" usage:"hour field (0-23 or *); defaults to *"`
	DayMonth string `zester:"daymonth,default=*" usage:"day-of-month field (1-31 or *); defaults to *"`
	Month    string `zester:"month,default=*" usage:"month field (1-12 or *); defaults to *"`
	DayWeek  string `zester:"dayweek,default=*" usage:"day-of-week field (0-7 or *); defaults to *"`

	cron exec.CronExec

	// Revert memos — armed ONLY when Apply actually wrote the crontab.
	// original is the pre-Apply entry at this identity (nil when Apply
	// created a brand-new entry): a same-instance Revert restores it, and a
	// fresh instance (or converged Apply) reverts as a clean no-op instead
	// of blindly deleting by command.
	applied  bool
	original *exec.CronEntry
}

// cronPresentSpec is the compiled schema + documentation for cron.present. Its
// Doc is drift-corrected against the live Check/Apply/Revert behavior.
var cronPresentSpec = regdef.MustSpec("cron.present", modschema.KindState, CronPresent{}, modschema.Doc{
	Summary: "Ensure a crontab entry exists for a user with the given schedule and command.",
	Description: "`cron.present` ensures a single crontab entry exists in the target user's crontab. " +
		"The entry's IDENTITY is the state's `name` (defaulting to the state ID), stamped as a " +
		"`# ZESTER_CRON_ID:` marker comment on the line — so editing the `command` under an unchanged " +
		"name REPLACES the old line in place instead of orphaning it, and two states with distinct " +
		"names but the same command coexist. `command` is required; `user` defaults to `root` and each " +
		"of the five schedule fields (`minute`, `hour`, `daymonth`, `month`, `dayweek`) defaults to `*`.",
	Effects: modschema.Effects{
		Check: "Lists the user's crontab and locates the entry sharing this state's identity — the " +
			"marker comment when present, or, as an adoption fallback, an exact command match among " +
			"label-less (hand-written) lines. Reports a change when no such entry exists, or when the " +
			"located entry's command (compared with internal whitespace collapsed, matching the crontab " +
			"parser), schedule fields, or identifier comment drift from the declared values.",
		Apply: "Re-lists the crontab (a self-contained flow: a watch-forced Apply bypasses Check). An " +
			"already-converged entry is a clean no-op — no crontab rewrite, no lying change. Otherwise it " +
			"writes the desired entry, stamping the identifier comment (adopting a matching label-less " +
			"line rather than duplicating it), and memoizes the replaced or adopted original for revert. " +
			"Reports the user, command, and schedule in its details.",
		Revert: "Undoes only what this run's Apply wrote. An entry created under the same identity as a " +
			"prior one is restored in place; an adopted label-less line is put back; a freshly created " +
			"entry is removed by its (command-scoped) line, preserving other names' same-command entries " +
			"which are re-added. A fresh instance (a standalone revert) recorded nothing and is an " +
			"explicit no-op — it never deletes an entry by bare command.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Nightly database backup",
			Kind:        "state",
			Explanation: "command is required; the schedule fields default to * where omitted.",
			Code: "backup-db:\n  cron.present:\n    - user: postgres\n    - command: /usr/local/bin/pg_backup.sh\n" +
				"    - minute: \"0\"\n    - hour: \"3\"\n    - require:\n      - \"pkg.installed:postgresql\"\n",
		},
		{
			Title:       "Run a command every five minutes",
			Kind:        "state",
			Explanation: "A numeric minute is coerced to its string form (minute 5, not every minute — see the BD-3 divergence).",
			Code:        "poll:\n  cron.present:\n    - command: /usr/local/bin/poll.sh\n    - minute: 5\n",
		},
		{
			Title:       "Create a cron entry ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the entry's name (its identifier comment); command is a key=value.",
			Code:        "zester 'web*' cron.present nightly command=/usr/local/bin/nightly.sh hour=2 minute=0",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Identity is the name, not the command",
			Body: "The entry's identity is its `name` (stamped as a `# ZESTER_CRON_ID:` marker). Editing " +
				"the `command` under an unchanged name replaces the line in place; two states with " +
				"distinct names and the same command coexist as separate entries.",
		},
		{
			Level: "warn",
			Title: "An integer minute now means that minute",
			Body: "Under the uniform decoder a YAML/msgpack integer schedule value (for example " +
				"`minute: 5`) coerces to \"5\". The legacy parser dropped a non-string value to \"\" and " +
				"then to `*`, so `minute: 5` used to run EVERY minute. Quote schedule values you intend " +
				"as strings; a numeric value now maps to that exact field (BD-3).",
		},
	},
	Divergences: []string{"BD-3", "BD-6"},
	SeeAlso:     []string{"cron.absent"},
})

// NewCronPresentBuilder returns a state.Builder that creates CronPresent states
// using the given ModuleContext's cron provider. Decode policy (unknown-key
// handling, reserved keys) is threaded via opts; the peel supplies it through
// modules.RegisterAll.
func NewCronPresentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Cron == nil {
			return nil, fmt.Errorf("cron.present: no cron provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		c := &CronPresent{}
		if _, err := cronPresentSpec.Decode(id, config, c, opts); err != nil {
			return nil, fmt.Errorf("cron.present: %w", err)
		}
		c.id = id
		c.cron = mctx.Cron
		c.reqs = state.ParseRequisites(config)
		return c, nil
	}
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
//
// Commands are compared whitespace-normalized: the crontab parser collapses
// internal runs of spaces (fields are re-joined with single spaces), so a
// declared command containing consecutive spaces would otherwise mismatch
// its own installed line forever — perpetual churn rewriting the crontab
// every run.
func (c *CronPresent) entryDiffs(e exec.CronEntry) []string {
	var diffs []string
	if normalizeCronCommand(e.Command) != normalizeCronCommand(c.Command) {
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
	// only label-less lines, so labeled and label-less lines each land once.
	// KNOWN LIMITATION: multiple pre-existing label-less lines with the
	// IDENTICAL command (different schedules) collapse to one on revert —
	// Set keys label-less identity on the command, so the second re-add
	// replaces the first. Pathological input; documented rather than
	// re-modeled (CronExec has no append primitive).
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

// normalizeCronCommand collapses internal whitespace runs the way the
// crontab parser does (fields re-joined with single spaces), so desired and
// parsed commands compare on equal footing.
func normalizeCronCommand(cmd string) string {
	return strings.Join(strings.Fields(cmd), " ")
}
