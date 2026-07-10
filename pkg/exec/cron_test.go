package exec

import (
	"strings"
	"testing"
)

func TestFindCronEntryByComment(t *testing.T) {
	existing := []CronEntry{
		{Command: "/usr/local/bin/backup.sh", Comment: "backup-job"},
		{Command: "/usr/bin/other.sh", Comment: "other-job"},
	}
	// Same label, edited command: must match the labeled entry.
	i := FindCronEntry(existing, CronEntry{Command: "/usr/local/bin/backup.sh --v2", Comment: "backup-job"})
	if i != 0 {
		t.Errorf("FindCronEntry by comment = %d, want 0", i)
	}
}

func TestFindCronEntryDifferentCommentSameCommand(t *testing.T) {
	// A same-command entry under a DIFFERENT non-empty comment is a
	// different managed entry — never matched.
	existing := []CronEntry{
		{Command: "/usr/bin/backup.sh", Comment: "other-job"},
	}
	i := FindCronEntry(existing, CronEntry{Command: "/usr/bin/backup.sh", Comment: "backup-job"})
	if i != -1 {
		t.Errorf("FindCronEntry = %d, want -1 (different label owns that line)", i)
	}
}

func TestFindCronEntryAdoptsCommentless(t *testing.T) {
	// A comment-less hand-written line with the same command is adopted.
	existing := []CronEntry{
		{Command: "/usr/bin/other.sh", Comment: "other-job"},
		{Command: "/usr/bin/backup.sh"},
	}
	i := FindCronEntry(existing, CronEntry{Command: "/usr/bin/backup.sh", Comment: "backup-job"})
	if i != 1 {
		t.Errorf("FindCronEntry adoption = %d, want 1", i)
	}
}

func TestFindCronEntryNoCommentMatchesOnlyUnlabeled(t *testing.T) {
	existing := []CronEntry{
		{Command: "/usr/bin/backup.sh", Comment: "backup-job"},
		{Command: "/usr/bin/backup.sh"},
	}
	// A label-less desired entry keys on the exact command among LABEL-LESS
	// lines only — it never steals a labeled managed entry.
	i := FindCronEntry(existing, CronEntry{Command: "/usr/bin/backup.sh"})
	if i != 1 {
		t.Errorf("FindCronEntry label-less match = %d, want 1 (the unlabeled line)", i)
	}
	if j := FindCronEntry(existing[:1], CronEntry{Command: "/usr/bin/backup.sh"}); j != -1 {
		t.Errorf("FindCronEntry = %d, want -1 (only a labeled line exists)", j)
	}
	if j := FindCronEntry(existing, CronEntry{Command: "/usr/bin/missing.sh"}); j != -1 {
		t.Errorf("FindCronEntry miss = %d, want -1", j)
	}
}

func TestParseCrontabLabelMarkerIsIdentity(t *testing.T) {
	out := parseCrontab("# ZESTER_CRON_ID: backup-job\n0 2 * * * /usr/local/bin/backup.sh\n30 4 * * * /usr/bin/uncommented.sh\n")
	if len(out) != 2 {
		t.Fatalf("parsed %d entries, want 2", len(out))
	}
	if out[0].Comment != "backup-job" || out[0].Command != "/usr/local/bin/backup.sh" {
		t.Errorf("entry 0 = %+v", out[0])
	}
	if out[1].Comment != "" {
		t.Errorf("entry 1 comment = %q, want empty", out[1].Comment)
	}
}

func TestParseCrontabHumanCommentIsNotIdentity(t *testing.T) {
	// An ordinary descriptive comment above a hand-written line is an
	// annotation, NOT a label: the entry parses label-less so command-based
	// adoption still matches it (treating it as identity silently DUPLICATED
	// the job when a state adopted the line).
	out := parseCrontab("# nightly db dump\n0 2 * * * /usr/local/bin/dump.sh\n")
	if len(out) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(out))
	}
	if out[0].Comment != "" {
		t.Errorf("Comment = %q, want empty (human comments are not identity)", out[0].Comment)
	}
	if out[0].Command != "/usr/local/bin/dump.sh" {
		t.Errorf("Command = %q", out[0].Command)
	}
}

func TestParseCrontabHumanCommentDoesNotClearPendingLabel(t *testing.T) {
	// A human annotating between the marker and the entry must not orphan
	// the labeled entry.
	out := parseCrontab("# ZESTER_CRON_ID: backup\n# temporarily bumped to 3am\n0 3 * * * /usr/bin/backup.sh\n")
	if len(out) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(out))
	}
	if out[0].Comment != "backup" {
		t.Errorf("Comment = %q, want %q", out[0].Comment, "backup")
	}
}

// parseCrontab adapts the line-indexed parser to the entry-list shape these
// identity tests assert on.
func parseCrontab(output string) []CronEntry {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	parsed := parseCrontabLines(lines)
	entries := make([]CronEntry, 0, len(parsed))
	for _, pe := range parsed {
		entries = append(entries, pe.entry)
	}
	return entries
}
