package exec

import (
	"fmt"
	"unicode"
)

// CronLabelPrefix marks a crontab comment line as a Zester-managed entry
// label (Salt's SALT_CRON_IDENTIFIER equivalent): `# ZESTER_CRON_ID: <label>`.
// Only marker comments carry entry identity — ordinary human comments above a
// line are annotations, never identity, so a descriptive comment on a
// hand-written entry cannot block command-based adoption (which would
// silently DUPLICATE the job).
const CronLabelPrefix = "ZESTER_CRON_ID:"

// CronEntry represents a single crontab entry.
type CronEntry struct {
	Minute     string
	Hour       string
	DayOfMonth string
	Month      string
	DayOfWeek  string
	Command    string
	User       string
	// Comment is the entry's Zester label (its IDENTITY, Salt identifier
	// semantics). It is serialized as a `# ZESTER_CRON_ID: <label>` marker
	// line; non-marker crontab comments never populate this field.
	Comment string
}

// Validate rejects an entry whose fields would corrupt the crontab when
// serialized. The five schedule fields are single whitespace-delimited
// columns — a space or tab inside one shifts every following column (and
// the command) over, silently changing WHEN and WHAT runs. A newline or any
// other control character in ANY field splices extra lines into the crontab:
// a second, unmanaged job installed under the state's identity (the classic
// vector is a value taken from another peel's facts via basket()). Command
// and Comment are the rest of their lines, so they reject control
// characters only; interior spaces are legitimate there.
func (e CronEntry) Validate() error {
	for _, f := range []struct{ name, val string }{
		{"minute", e.Minute}, {"hour", e.Hour}, {"day-of-month", e.DayOfMonth},
		{"month", e.Month}, {"day-of-week", e.DayOfWeek},
	} {
		for i, r := range f.val {
			if unicode.IsSpace(r) || unicode.IsControl(r) {
				return fmt.Errorf("exec: crontab: %s field %q: whitespace or control character %q at byte %d is not allowed", f.name, f.val, r, i)
			}
		}
	}
	for _, f := range []struct{ name, val string }{
		{"command", e.Command}, {"comment", e.Comment}, {"user", e.User},
	} {
		for i, r := range f.val {
			if unicode.IsControl(r) {
				return fmt.Errorf("exec: crontab: %s: control character %q at byte %d is not allowed", f.name, r, i)
			}
		}
	}
	return nil
}

// FindCronEntry returns the index of the entry in existing that shares
// identity with entry, or -1 when none does.
//
// Identity is the Comment (the state's Label / Salt identifier) when entry
// carries one: an existing entry with the SAME non-empty label is the same
// managed entry even after its command was edited (so editing a state's
// command replaces the old line instead of orphaning it), while an existing
// entry with a DIFFERENT non-empty label is a different managed entry even
// with an identical command. Label-less existing lines (hand-written
// crontabs — including ones annotated with ordinary human comments, which
// parse as label-less) are matched by exact Command as an adoption fallback,
// so pre-existing crontabs keep working without duplication.
//
// When entry itself has no label, identity is the exact Command among
// LABEL-LESS lines only — a label-less write never steals a labeled managed
// entry (labels own their lines; distinct labels with the same command
// coexist).
func FindCronEntry(existing []CronEntry, entry CronEntry) int {
	if entry.Comment != "" {
		for i, e := range existing {
			if e.Comment == entry.Comment {
				return i
			}
		}
		for i, e := range existing {
			if e.Comment == "" && e.Command == entry.Command {
				return i
			}
		}
		return -1
	}
	for i, e := range existing {
		if e.Comment == "" && e.Command == entry.Command {
			return i
		}
	}
	return -1
}
