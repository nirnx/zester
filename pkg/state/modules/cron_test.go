package modules

import (
	"context"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

func testCronMctx(cron *exectest.FakeCronExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Cron:    cron,
			Package: exectest.NewFakePackageExec("apt"),
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
	}
}

func TestCronPresentName(t *testing.T) {
	mctx := testCronMctx(exectest.NewFakeCronExec())
	s, err := NewCronPresentBuilder(mctx)("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "cron.present:backup" {
		t.Errorf("Name() = %q", s.Name())
	}
}

func TestCronPresentDefaultUser(t *testing.T) {
	mctx := testCronMctx(exectest.NewFakeCronExec())
	s, err := NewCronPresentBuilder(mctx)("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
	})
	if err != nil {
		t.Fatal(err)
	}
	cp := s.(*CronPresent)
	if cp.User != "root" {
		t.Errorf("User default = %q, want root", cp.User)
	}
}

func TestCronPresentCommandRequired(t *testing.T) {
	mctx := testCronMctx(exectest.NewFakeCronExec())
	_, err := NewCronPresentBuilder(mctx)("backup", map[string]any{})
	if err == nil {
		t.Fatal("expected error when command is missing")
	}
}

func TestCronPresentCheckNeedsChange(t *testing.T) {
	fake := exectest.NewFakeCronExec()
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx)("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
		"minute":  "0",
		"hour":    "2",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange=true when entry absent")
	}
}

func TestCronPresentCheckNoChange(t *testing.T) {
	// The entry carries the state's Label as its identifier comment —
	// that is what a previous Apply writes.
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{
		Minute: "0", Hour: "2", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/bin/backup.sh", Comment: "backup",
	})
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx)("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
		"minute":  "0",
		"hour":    "2",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected NeedsChange=false when entry already matches, diff: %s", cr.Diff)
	}
}

func TestCronPresentCheckCommandEditedUnderSameLabel(t *testing.T) {
	// Identity is the Label comment: editing the command in the state must
	// flag the OLD entry as drifted, not report "does not exist" and orphan it.
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{
		Minute: "0", Hour: "2", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/local/bin/backup.sh", Comment: "backup-job",
	})
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx)("backup-job", map[string]any{
		"command": "/usr/local/bin/backup.sh --v2",
		"minute":  "0",
		"hour":    "2",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange=true when the labeled entry's command differs")
	}
}

func TestCronPresentApplyCommandEditReplacesNotOrphans(t *testing.T) {
	// Editing the command must REPLACE the old labeled entry in place — the
	// old command must not keep running as an orphan alongside the new one.
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{
		Minute: "0", Hour: "2", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/local/bin/backup.sh", Comment: "backup-job",
	})
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx)("backup-job", map[string]any{
		"command": "/usr/local/bin/backup.sh --v2",
		"minute":  "0",
		"hour":    "2",
	})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true")
	}
	entries := fake.ListSync("root")
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 entry (old one replaced), got %d: %v", len(entries), entries)
	}
	if entries[0].Command != "/usr/local/bin/backup.sh --v2" {
		t.Errorf("Command = %q, want the edited command", entries[0].Command)
	}
	if entries[0].Comment != "backup-job" {
		t.Errorf("Comment = %q, want %q", entries[0].Comment, "backup-job")
	}
}

func TestCronPresentAdoptsUnlabeledEntry(t *testing.T) {
	// A hand-written comment-less entry with the same command is adopted
	// (Check flags the missing identifier; Apply stamps it, no duplicate).
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{
		Minute: "0", Hour: "2", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/bin/backup.sh",
	})
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx)("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
		"minute":  "0",
		"hour":    "2",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange=true for an unlabeled matching entry")
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := fake.ListSync("root")
	if len(entries) != 1 {
		t.Fatalf("expected the unlabeled entry to be adopted, not duplicated: %v", entries)
	}
	if entries[0].Comment != "backup" {
		t.Errorf("Comment = %q, want %q", entries[0].Comment, "backup")
	}
}

func TestCronPresentDistinctLabelsSameCommandCoexist(t *testing.T) {
	// A same-command entry under a DIFFERENT label is a different managed
	// entry — it must not be stolen or replaced.
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{
		Minute: "0", Hour: "2", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/bin/backup.sh", Comment: "other-job",
	})
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx)("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
		"minute":  "30",
		"hour":    "4",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange=true: no entry with this label exists")
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := fake.ListSync("root")
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (distinct labels coexist), got %d: %v", len(entries), entries)
	}
}

func TestCronPresentApply(t *testing.T) {
	fake := exectest.NewFakeCronExec()
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx)("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
		"minute":  "0",
		"hour":    "3",
	})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true")
	}
	entries := fake.ListSync("root")
	if len(entries) != 1 || entries[0].Command != "/usr/bin/backup.sh" {
		t.Errorf("entry not set: %v", entries)
	}
}

func TestCronPresentRevert(t *testing.T) {
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{Command: "/usr/bin/backup.sh"})
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx)("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
	})
	_, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.ListSync("root")) != 0 {
		t.Error("expected entry removed after Revert")
	}
}

func TestCronPresentNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Cron: nil}}
	_, err := NewCronPresentBuilder(mctx)("backup", map[string]any{"command": "/bin/x"})
	if err == nil {
		t.Fatal("expected error when cron provider is nil")
	}
}

func TestCronPresentRequisites(t *testing.T) {
	mctx := testCronMctx(exectest.NewFakeCronExec())
	s, err := NewCronPresentBuilder(mctx)("backup", map[string]any{
		"command": "/bin/x",
		"require": []any{"pkg.installed:cron"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:cron" {
		t.Errorf("Require = %v", reqs.Require)
	}
}

// --- CronAbsent ---

func TestCronAbsentName(t *testing.T) {
	mctx := testCronMctx(exectest.NewFakeCronExec())
	s, err := NewCronAbsentBuilder(mctx)("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "cron.absent:backup" {
		t.Errorf("Name() = %q", s.Name())
	}
}

func TestCronAbsentCheckNeedsChange(t *testing.T) {
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{Command: "/usr/bin/backup.sh"})
	mctx := testCronMctx(fake)
	s, _ := NewCronAbsentBuilder(mctx)("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange=true when entry exists")
	}
}

func TestCronAbsentCheckNoChange(t *testing.T) {
	fake := exectest.NewFakeCronExec()
	mctx := testCronMctx(fake)
	s, _ := NewCronAbsentBuilder(mctx)("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
	})
	cr, _ := s.Check(context.Background())
	if cr.NeedsChange {
		t.Error("expected NeedsChange=false when entry absent")
	}
}

func TestCronAbsentApply(t *testing.T) {
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{Command: "/usr/bin/backup.sh"})
	mctx := testCronMctx(fake)
	s, _ := NewCronAbsentBuilder(mctx)("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
	})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true")
	}
	if len(fake.ListSync("root")) != 0 {
		t.Error("expected entry removed")
	}
}

func TestCronAbsentNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Cron: nil}}
	_, err := NewCronAbsentBuilder(mctx)("backup", map[string]any{"command": "/bin/x"})
	if err == nil {
		t.Fatal("expected error when cron provider is nil")
	}
}
