package exec

import (
	"context"
	"fmt"
	"strings"
)

// CrontabProvider implements CronExec using the system crontab(1) command.
//
// Edits are LINE-PRESERVING: Set and Remove splice only the lines belonging
// to the targeted entry (its schedule line plus its ZESTER_CRON_ID marker)
// and write everything else back verbatim. A whole-crontab reconstruction
// from parsed entries would silently destroy everything the parser does not
// model — MAILTO=/PATH= environment assignments, human comments, and
// @reboot/@daily nickname entries.
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
	lines, err := p.readLines(ctx, user)
	if err != nil {
		return nil, err
	}
	parsed := parseCrontabLines(lines)
	entries := make([]CronEntry, 0, len(parsed))
	for _, pe := range parsed {
		entries = append(entries, pe.entry)
	}
	return entries, nil
}

// Set adds or replaces a cron entry. Identity is resolved by FindCronEntry:
// the Comment (state label / identifier) when present — so editing a managed
// entry's command replaces the old line in-place instead of orphaning it —
// with a Command fallback for comment-less entries. Only the targeted lines
// change; the rest of the crontab is preserved byte-for-byte.
func (p *CrontabProvider) Set(ctx context.Context, user string, entry CronEntry) error {
	lines, err := p.readLines(ctx, user)
	if err != nil {
		return err
	}
	parsed := parseCrontabLines(lines)
	entries := make([]CronEntry, 0, len(parsed))
	for _, pe := range parsed {
		entries = append(entries, pe.entry)
	}

	block := serializeEntry(entry)
	if i := FindCronEntry(entries, entry); i >= 0 {
		target := parsed[i]
		start, end := target.lineIdx, target.lineIdx+1
		if target.markerIdx >= 0 {
			start = target.markerIdx
		}
		replaced := append([]string{}, lines[:start]...)
		replaced = append(replaced, block...)
		replaced = append(replaced, lines[end:]...)
		lines = replaced
	} else {
		lines = append(lines, block...)
	}

	return p.writeLines(ctx, user, lines)
}

// Remove deletes every cron entry matching the given command string (plus
// its marker line). All other lines are preserved verbatim; when nothing
// matches, the crontab is not rewritten at all.
func (p *CrontabProvider) Remove(ctx context.Context, user string, command string) error {
	lines, err := p.readLines(ctx, user)
	if err != nil {
		return err
	}
	drop := make(map[int]bool)
	for _, pe := range parseCrontabLines(lines) {
		if pe.entry.Command != command {
			continue
		}
		drop[pe.lineIdx] = true
		if pe.markerIdx >= 0 {
			drop[pe.markerIdx] = true
		}
	}
	if len(drop) == 0 {
		return nil // nothing matched: never rewrite a crontab for a no-op
	}
	kept := lines[:0]
	for i, l := range lines {
		if !drop[i] {
			kept = append(kept, l)
		}
	}
	return p.writeLines(ctx, user, kept)
}

// readLines fetches the user's crontab verbatim ([]string, no trailing
// newline element). A missing crontab is an empty slice.
func (p *CrontabProvider) readLines(ctx context.Context, user string) ([]string, error) {
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
	if res.Stdout == "" {
		return nil, nil
	}
	return strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n"), nil
}

// writeLines installs the crontab via `crontab -u <user> -`.
func (p *CrontabProvider) writeLines(ctx context.Context, user string, lines []string) error {
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}

	args := []string{"-"}
	if user != "" {
		args = []string{"-u", user, "-"}
	}

	// CommandExec.Run has no stdin injection — pipe the content in via
	// shell-mode printf.
	quoted := shellQuote(content)
	opts := CommandOpts{
		Command: fmt.Sprintf("printf '%%s' %s | crontab %s", quoted, strings.Join(args, " ")),
		Shell:   true,
	}
	if _, err := p.cmd.Run(ctx, opts); err != nil {
		return fmt.Errorf("exec: crontab: write user %q: %w", user, err)
	}
	return nil
}

// serializeEntry renders an entry as its crontab lines: the identity marker
// comment (when labeled) followed by the schedule line.
func serializeEntry(e CronEntry) []string {
	var out []string
	if e.Comment != "" {
		out = append(out, fmt.Sprintf("# %s %s", CronLabelPrefix, e.Comment))
	}
	field := func(v string) string {
		if v == "" {
			return "*"
		}
		return v
	}
	out = append(out, fmt.Sprintf("%s %s %s %s %s %s",
		field(e.Minute), field(e.Hour), field(e.DayOfMonth), field(e.Month), field(e.DayOfWeek), e.Command))
	return out
}

// parsedEntry ties a parsed CronEntry to the verbatim line positions it owns
// (its schedule line, and its marker comment line when labeled) so edits can
// splice exactly those lines.
type parsedEntry struct {
	entry     CronEntry
	lineIdx   int
	markerIdx int // -1 when the entry has no ZESTER_CRON_ID marker
}

// parseCrontabLines extracts managed-shape entries with their line indices.
// Only `# ZESTER_CRON_ID: <label>` marker lines carry entry identity and are
// attached (as Comment) to the next entry; ordinary human comments are
// annotations, deliberately NOT identity — treating them as labels made a
// descriptive comment above a hand-written line block command-based adoption
// and silently duplicate the job. Environment assignments (MAILTO=…),
// nickname entries (@reboot …), and anything else unparsable are simply not
// entries — they stay untouched in the verbatim line set.
func parseCrontabLines(lines []string) []parsedEntry {
	var out []parsedEntry
	pendingComment := ""
	pendingIdx := -1

	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			text := strings.TrimSpace(strings.TrimPrefix(line, "#"))
			if strings.HasPrefix(text, CronLabelPrefix) {
				pendingComment = strings.TrimSpace(strings.TrimPrefix(text, CronLabelPrefix))
				pendingIdx = i
			}
			// Non-marker comments neither set nor clear a pending label, so a
			// human annotating below a marker cannot orphan the labeled entry.
			continue
		}
		// Environment variable assignments (e.g. MAILTO=...) end any pending
		// marker but are not entries.
		if strings.Contains(line, "=") && !strings.Contains(strings.Fields(line)[0], "/") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 && !strings.ContainsAny(parts[0], " \t*") {
				pendingComment, pendingIdx = "", -1
				continue
			}
		}

		fields := strings.Fields(line)
		if len(fields) < 6 || strings.HasPrefix(fields[0], "@") {
			// Too short, or an @reboot/@daily nickname entry — not managed,
			// preserved verbatim by the splicing editors.
			pendingComment, pendingIdx = "", -1
			continue
		}

		out = append(out, parsedEntry{
			entry: CronEntry{
				Minute:     fields[0],
				Hour:       fields[1],
				DayOfMonth: fields[2],
				Month:      fields[3],
				DayOfWeek:  fields[4],
				Command:    strings.Join(fields[5:], " "),
				Comment:    pendingComment,
			},
			lineIdx:   i,
			markerIdx: pendingIdx,
		})
		pendingComment, pendingIdx = "", -1
	}
	return out
}

// shellQuote wraps s in single quotes, escaping any single quotes within.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
