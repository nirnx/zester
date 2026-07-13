package modules

import (
	"context"
	"errors"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
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
	s, err := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
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
	s, err := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
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
	_, err := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{})
	if err == nil {
		t.Fatal("expected error when command is missing")
	}
}

func TestCronPresentCheckNeedsChange(t *testing.T) {
	fake := exectest.NewFakeCronExec()
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
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
	s, _ := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
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
	s, _ := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup-job", map[string]any{
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
	s, _ := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup-job", map[string]any{
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
	s, _ := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
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
	s, _ := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
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
	s, _ := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
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

func TestCronPresentRevertCreatedEntry(t *testing.T) {
	// Same-instance Apply (created a new entry) then Revert: the created
	// entry is removed.
	fake := exectest.NewFakeCronExec()
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
	})
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true on revert of a created entry")
	}
	if len(fake.ListSync("root")) != 0 {
		t.Error("expected entry removed after Revert")
	}
}

func TestCronPresentRevertFreshInstanceNoOp(t *testing.T) {
	// A fresh instance (standalone ModeRevert) has no apply memo: Revert must
	// be an explicit clean no-op — the old code deleted the pre-existing
	// entry by bare command and reported Changed=true.
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{Command: "/usr/bin/backup.sh"})
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
	})
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected Changed=false on fresh-instance Revert")
	}
	if ar.Diff != "nothing to revert (no apply recorded in this run)" {
		t.Errorf("Diff = %q, want the explicit no-op explanation", ar.Diff)
	}
	if len(fake.ListSync("root")) != 1 {
		t.Error("fresh-instance Revert must not delete pre-existing entries")
	}
}

func TestCronPresentRevertAfterConvergedApplyNoOp(t *testing.T) {
	// Apply that no-op'd (already converged) must not arm the revert memo:
	// Revert leaves the entry alone.
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{
		Minute: "0", Hour: "2", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/bin/backup.sh", Comment: "backup",
	})
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
		"minute":  "0",
		"hour":    "2",
	})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Fatalf("expected converged Apply to be a no-op, diff: %s", ar.Diff)
	}
	rr, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rr.Changed {
		t.Error("expected Changed=false on Revert after a no-op Apply")
	}
	if len(fake.ListSync("root")) != 1 {
		t.Error("Revert after a no-op Apply must not delete the entry")
	}
}

func TestCronPresentRevertPreservesOtherLabelsSameCommand(t *testing.T) {
	// Two states manage the same command under distinct labels (blessed
	// coexistence). Reverting one must not delete the other's entry, even
	// though CronExec.Remove is command-scoped.
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{
		Minute: "0", Hour: "*", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/bin/sync.sh", Comment: "sync-daily",
	})
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("sync-hourly", map[string]any{
		"command": "/usr/bin/sync.sh",
		"minute":  "30",
	})
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.ListSync("root")); got != 2 {
		t.Fatalf("expected 2 coexisting entries after Apply, got %d", got)
	}
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true on revert of the created entry")
	}
	entries := fake.ListSync("root")
	if len(entries) != 1 {
		t.Fatalf("expected exactly the other label's entry to survive, got %v", entries)
	}
	if entries[0].Comment != "sync-daily" {
		t.Errorf("surviving entry = %+v, want the sync-daily one", entries[0])
	}
}

func TestCronPresentRevertRestoresAdoptedLine(t *testing.T) {
	// Apply adopted a hand-written label-less line (stamping the label and
	// fixing the schedule); a same-instance Revert restores the original
	// line instead of deleting it.
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{
		Minute: "15", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/bin/backup.sh",
	})
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
		"minute":  "0",
		"hour":    "2",
	})
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true when restoring the adopted line")
	}
	entries := fake.ListSync("root")
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 restored entry, got %v", entries)
	}
	e := entries[0]
	if e.Comment != "" || e.Minute != "15" || e.Hour != "3" {
		t.Errorf("restored entry = %+v, want the original label-less 15 3 line", e)
	}
}

func TestCronPresentRevertRestoresSameLabelOriginal(t *testing.T) {
	// Apply replaced a prior run's entry (same label, edited command); a
	// same-instance Revert restores the original command in place.
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{
		Minute: "0", Hour: "2", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/local/bin/backup.sh", Comment: "backup-job",
	})
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup-job", map[string]any{
		"command": "/usr/local/bin/backup.sh --v2",
		"minute":  "0",
		"hour":    "2",
	})
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true when restoring the original entry")
	}
	entries := fake.ListSync("root")
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 entry after revert, got %v", entries)
	}
	if entries[0].Command != "/usr/local/bin/backup.sh" || entries[0].Comment != "backup-job" {
		t.Errorf("restored entry = %+v, want the original command under the same label", entries[0])
	}
}

func TestCronPresentApplyConvergedNoOp(t *testing.T) {
	// Watch-forced Apply (bypasses Check) on a fully converged entry: clean
	// no-op — no crontab rewrite, no lying Changed=true.
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{
		Minute: "0", Hour: "2", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/bin/backup.sh", Comment: "backup",
	})
	fake.SetErr = errors.New("Set must not be called when converged")
	mctx := testCronMctx(fake)
	s, _ := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
		"command": "/usr/bin/backup.sh",
		"minute":  "0",
		"hour":    "2",
	})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Errorf("expected Changed=false when converged, diff: %s", ar.Diff)
	}
}

// cronConvergenceWalk runs Check → Apply → Check and fails unless the second
// Check reports converged: what Check compares must be exactly what Apply
// produces.
func cronConvergenceWalk(t *testing.T, fake *exectest.FakeCronExec, config map[string]any) {
	t.Helper()
	mctx := testCronMctx(fake)
	s, err := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", config)
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange=true before Apply")
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	cr, err = s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected converged after Apply, diff: %s", cr.Diff)
	}
}

func TestCronPresentConvergenceCreate(t *testing.T) {
	cronConvergenceWalk(t, exectest.NewFakeCronExec(), map[string]any{
		"command": "/usr/bin/backup.sh",
		"minute":  "0",
		"hour":    "2",
	})
}

func TestCronPresentConvergenceAdopt(t *testing.T) {
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{
		Minute: "15", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/bin/backup.sh",
	})
	cronConvergenceWalk(t, fake, map[string]any{
		"command": "/usr/bin/backup.sh",
		"minute":  "0",
		"hour":    "2",
	})
}

func TestCronPresentConvergenceCommandEdit(t *testing.T) {
	fake := exectest.NewFakeCronExec()
	fake.PreAdd("root", exec.CronEntry{
		Minute: "0", Hour: "2", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/bin/backup.sh", Comment: "backup",
	})
	cronConvergenceWalk(t, fake, map[string]any{
		"command": "/usr/bin/backup.sh --v2",
		"minute":  "0",
		"hour":    "2",
	})
}

func TestCronPresentNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Cron: nil}}
	_, err := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{"command": "/bin/x"})
	if err == nil {
		t.Fatal("expected error when cron provider is nil")
	}
}

func TestCronPresentRequisites(t *testing.T) {
	mctx := testCronMctx(exectest.NewFakeCronExec())
	s, err := NewCronPresentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
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
	s, err := NewCronAbsentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
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
	s, _ := NewCronAbsentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
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
	s, _ := NewCronAbsentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
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
	s, _ := NewCronAbsentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{
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
	_, err := NewCronAbsentBuilder(mctx, modschema.DecodeOptions{})("backup", map[string]any{"command": "/bin/x"})
	if err == nil {
		t.Fatal("expected error when cron provider is nil")
	}
}
