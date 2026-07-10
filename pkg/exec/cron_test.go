package exec

import "testing"

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

func TestFindCronEntryNoCommentFallsBackToCommand(t *testing.T) {
	existing := []CronEntry{
		{Command: "/usr/bin/backup.sh", Comment: "backup-job"},
	}
	// A label-less desired entry keys on the exact command (historical
	// behavior), matching regardless of the existing comment.
	i := FindCronEntry(existing, CronEntry{Command: "/usr/bin/backup.sh"})
	if i != 0 {
		t.Errorf("FindCronEntry command fallback = %d, want 0", i)
	}
	if j := FindCronEntry(existing, CronEntry{Command: "/usr/bin/missing.sh"}); j != -1 {
		t.Errorf("FindCronEntry miss = %d, want -1", j)
	}
}

func TestParseCrontabAttachesComments(t *testing.T) {
	out := parseCrontab("# backup-job\n0 2 * * * /usr/local/bin/backup.sh\n30 4 * * * /usr/bin/uncommented.sh\n")
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
