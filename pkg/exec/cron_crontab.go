package exec

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// CrontabProvider implements CronExec using the system crontab(1) command.
type CrontabProvider struct {
	cmd CommandExec
}

// NewCrontabProvider creates a CrontabProvider with the given CommandExec.
func NewCrontabProvider(cmd CommandExec) *CrontabProvider {
	return &CrontabProvider{cmd: cmd}
}

// List returns all cron entries for the given user by parsing `crontab -l`.
// Returns an empty slice if the user has no crontab.
func (p *CrontabProvider) List(ctx context.Context, user string) ([]CronEntry, error) {
	args := []string{"-l"}
	if user != "" {
		args = []string{"-l", "-u", user}
	}
	res, err := p.cmd.Run(ctx, CommandOpts{Command: "crontab", Args: args})
	if err != nil {
		// "no crontab for <user>" exits non-zero — treat as empty.
		if res != nil && strings.Contains(res.Stderr, "no crontab") {
			return nil, nil
		}
		return nil, fmt.Errorf("exec: crontab: list user %q: %w", user, err)
	}
	return parseCrontab(res.Stdout), nil
}

// Set adds or replaces a cron entry. Identity is resolved by FindCronEntry:
// the Comment (state label / identifier) when present — so editing a managed
// entry's command replaces the old line in-place instead of orphaning it —
// with a Command fallback for comment-less entries.
func (p *CrontabProvider) Set(ctx context.Context, user string, entry CronEntry) error {
	existing, err := p.List(ctx, user)
	if err != nil {
		return err
	}

	if i := FindCronEntry(existing, entry); i >= 0 {
		existing[i] = entry
	} else {
		existing = append(existing, entry)
	}

	return p.writeCrontab(ctx, user, existing)
}

// Remove deletes the cron entry matching the given command string.
func (p *CrontabProvider) Remove(ctx context.Context, user string, command string) error {
	existing, err := p.List(ctx, user)
	if err != nil {
		return err
	}

	filtered := existing[:0]
	for _, e := range existing {
		if e.Command != command {
			filtered = append(filtered, e)
		}
	}

	return p.writeCrontab(ctx, user, filtered)
}

// writeCrontab writes entries back via `crontab -u <user> -` (reads from stdin).
func (p *CrontabProvider) writeCrontab(ctx context.Context, user string, entries []CronEntry) error {
	var buf bytes.Buffer
	for _, e := range entries {
		if e.Comment != "" {
			// Labels are serialized with the identity marker so parseCrontab
			// can tell them apart from ordinary human comments.
			fmt.Fprintf(&buf, "# %s %s\n", CronLabelPrefix, e.Comment)
		}
		minute := e.Minute
		if minute == "" {
			minute = "*"
		}
		hour := e.Hour
		if hour == "" {
			hour = "*"
		}
		dom := e.DayOfMonth
		if dom == "" {
			dom = "*"
		}
		month := e.Month
		if month == "" {
			month = "*"
		}
		dow := e.DayOfWeek
		if dow == "" {
			dow = "*"
		}
		fmt.Fprintf(&buf, "%s %s %s %s %s %s\n", minute, hour, dom, month, dow, e.Command)
	}

	args := []string{"-"}
	if user != "" {
		args = []string{"-u", user, "-"}
	}

	// CommandExec.Run has no stdin injection — pipe the content in via
	// shell-mode printf.
	quoted := shellQuote(buf.String())
	opts := CommandOpts{
		Command: fmt.Sprintf("printf '%%s' %s | crontab %s", quoted, strings.Join(args, " ")),
		Shell:   true,
	}
	_, err := p.cmd.Run(ctx, opts)
	if err != nil {
		return fmt.Errorf("exec: crontab: write user %q: %w", user, err)
	}
	return nil
}

// parseCrontab parses the output of `crontab -l` into CronEntry values.
// Only `# ZESTER_CRON_ID: <label>` marker lines carry entry identity and are
// attached (as Comment) to the next entry; ordinary human comments are
// annotations, deliberately NOT identity — treating them as labels made a
// descriptive comment above a hand-written line block command-based adoption
// and silently duplicate the job.
func parseCrontab(output string) []CronEntry {
	var entries []CronEntry
	var pendingComment string

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			text := strings.TrimSpace(strings.TrimPrefix(line, "#"))
			if strings.HasPrefix(text, CronLabelPrefix) {
				pendingComment = strings.TrimSpace(strings.TrimPrefix(text, CronLabelPrefix))
			}
			// Non-marker comments neither set nor clear a pending label, so a
			// human annotating below a marker cannot orphan the labeled entry.
			continue
		}
		// Skip environment variable assignments (e.g. MAILTO=...)
		if strings.Contains(line, "=") && !strings.Contains(strings.Fields(line)[0], "/") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 && !strings.ContainsAny(parts[0], " \t*") {
				pendingComment = ""
				continue
			}
		}

		fields := strings.Fields(line)
		if len(fields) < 6 {
			pendingComment = ""
			continue
		}

		e := CronEntry{
			Minute:     fields[0],
			Hour:       fields[1],
			DayOfMonth: fields[2],
			Month:      fields[3],
			DayOfWeek:  fields[4],
			Command:    strings.Join(fields[5:], " "),
			Comment:    pendingComment,
		}
		pendingComment = ""
		entries = append(entries, e)
	}
	return entries
}

// shellQuote wraps s in single quotes, escaping any single quotes within.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
