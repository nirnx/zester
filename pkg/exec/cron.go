package exec

// CronEntry represents a single crontab entry.
type CronEntry struct {
	Minute     string
	Hour       string
	DayOfMonth string
	Month      string
	DayOfWeek  string
	Command    string
	User       string
	Comment    string
}

// FindCronEntry returns the index of the entry in existing that shares
// identity with entry, or -1 when none does.
//
// Identity is the Comment (the state's Label / Salt identifier) when entry
// carries one: an existing entry with the SAME non-empty comment is the same
// managed entry even after its command was edited (so editing a state's
// command replaces the old line instead of orphaning it), while an existing
// entry with a DIFFERENT non-empty comment is a different managed entry even
// with an identical command. Comment-less existing lines (hand-written
// crontabs) are matched by exact Command as an adoption fallback, so
// pre-existing crontabs keep working without duplication.
//
// When entry itself has no Comment, identity falls back to the exact Command
// string (the historical behavior for label-less entries).
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
		if e.Command == entry.Command {
			return i
		}
	}
	return -1
}
