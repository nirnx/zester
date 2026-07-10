package modules

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

func testFileCopyMctx() *exec.ModuleContext {
	return &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: &exec.OSFileExec{}}}
}

func TestFileCopyName(t *testing.T) {
	s, err := NewFileCopyBuilder(testFileCopyMctx())("/etc/dst", map[string]any{
		"source": "/etc/src",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "file.copy:/etc/dst" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestFileCopyMissingSource(t *testing.T) {
	_, err := NewFileCopyBuilder(testFileCopyMctx())("/etc/dst", map[string]any{})
	if err == nil {
		t.Fatal("expected error when source is missing")
	}
}

func TestFileCopyMissingProvider(t *testing.T) {
	_, err := NewFileCopyBuilder(&exec.ModuleContext{})("/etc/dst", map[string]any{
		"source": "/etc/src",
	})
	if err == nil {
		t.Fatal("expected error when File provider is nil")
	}
}

func TestFileCopyHappyPath(t *testing.T) {
	ctx := context.Background()
	src := writeTempFile(t, "src.txt", "payload\n")
	dst := filepath.Join(t.TempDir(), "dst.txt")

	s, err := NewFileCopyBuilder(testFileCopyMctx())(dst, map[string]any{"source": src})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange when destination is missing")
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after copy")
	}
	if ar.Details["source"] != src || ar.Details["path"] != dst {
		t.Errorf("Details: got %v", ar.Details)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "payload\n" {
		t.Errorf("content: got %q", string(got))
	}

	// Idempotent: destination exists, so a second Check reports no change.
	cr2, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr2.NeedsChange {
		t.Error("expected no change on second Check")
	}
}

func TestFileCopyExistingNoForce(t *testing.T) {
	ctx := context.Background()
	src := writeTempFile(t, "src.txt", "new\n")
	dst := writeTempFile(t, "dst.txt", "old\n")

	s, err := NewFileCopyBuilder(testFileCopyMctx())(dst, map[string]any{"source": src})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when destination exists and force is unset")
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected Apply no-op without force")
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "old\n" {
		t.Errorf("destination modified without force: got %q", string(got))
	}
}

func TestFileCopyForceOverwrite(t *testing.T) {
	ctx := context.Background()
	src := writeTempFile(t, "src.txt", "new\n")
	dst := writeTempFile(t, "dst.txt", "old\n")

	s, err := NewFileCopyBuilder(testFileCopyMctx())(dst, map[string]any{
		"source": src, "force": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange when contents differ and force is set")
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after forced copy")
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "new\n" {
		t.Errorf("content: got %q", string(got))
	}

	// Idempotent: contents now identical.
	cr2, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr2.NeedsChange {
		t.Error("expected no change on second Check after forced copy")
	}
}

func TestFileCopyMakeDirs(t *testing.T) {
	ctx := context.Background()
	src := writeTempFile(t, "src.txt", "data\n")
	dst := filepath.Join(t.TempDir(), "a", "b", "dst.txt")

	s, err := NewFileCopyBuilder(testFileCopyMctx())(dst, map[string]any{
		"source": src, "makedirs": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "data\n" {
		t.Errorf("content: got %q", string(got))
	}
}

func TestFileCopyNoMakeDirsFails(t *testing.T) {
	ctx := context.Background()
	src := writeTempFile(t, "src.txt", "data\n")
	dst := filepath.Join(t.TempDir(), "missing", "dst.txt")

	s, err := NewFileCopyBuilder(testFileCopyMctx())(dst, map[string]any{"source": src})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err == nil {
		t.Error("expected error when parent dir is missing and makedirs is unset")
	}
}

func TestFileCopyRevert(t *testing.T) {
	ctx := context.Background()
	src := writeTempFile(t, "src.txt", "new\n")
	dst := writeTempFile(t, "dst.txt", "old\n")

	s, err := NewFileCopyBuilder(testFileCopyMctx())(dst, map[string]any{
		"source": src, "force": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revert(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "old\n" {
		t.Errorf("revert content: got %q", string(got))
	}
}

func TestFileCopyRevertRemovesFreshCopy(t *testing.T) {
	ctx := context.Background()
	src := writeTempFile(t, "src.txt", "data\n")
	dst := filepath.Join(t.TempDir(), "dst.txt")

	s, err := NewFileCopyBuilder(testFileCopyMctx())(dst, map[string]any{"source": src})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revert(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("expected destination removed on revert of fresh copy")
	}
}

func TestFileCopyRevertFreshInstanceNoOp(t *testing.T) {
	ctx := context.Background()
	src := writeTempFile(t, "src.txt", "new\n")
	dst := writeTempFile(t, "dst.txt", "precious\n")

	s, err := NewFileCopyBuilder(testFileCopyMctx())(dst, map[string]any{
		"source": src, "force": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Revert on a fresh instance (no Apply recorded) must be a clean no-op:
	// never delete a pre-existing destination Apply never wrote.
	rr, err := s.Revert(ctx)
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if rr.Changed {
		t.Error("fresh-instance revert must not report a change")
	}
	if !strings.Contains(rr.Diff, "nothing to revert") {
		t.Errorf("diff: got %q, want nothing-to-revert notice", rr.Diff)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("destination destroyed by fresh-instance revert: %v", err)
	}
	if string(got) != "precious\n" {
		t.Errorf("content after no-op revert: got %q", string(got))
	}
}

func TestFileCopyRevertFreshInstanceMissingDest(t *testing.T) {
	ctx := context.Background()
	src := writeTempFile(t, "src.txt", "new\n")
	dst := filepath.Join(t.TempDir(), "missing.txt")

	s, err := NewFileCopyBuilder(testFileCopyMctx())(dst, map[string]any{"source": src})
	if err != nil {
		t.Fatal(err)
	}

	// Destination absent + no Apply recorded: clean no-op, not a hard error.
	rr, err := s.Revert(ctx)
	if err != nil {
		t.Fatalf("Revert on missing destination: %v", err)
	}
	if rr.Changed {
		t.Error("expected no change reverting a fresh instance with a missing destination")
	}
}

func TestFileCopyRevertCreatedToleratesMissing(t *testing.T) {
	ctx := context.Background()
	src := writeTempFile(t, "src.txt", "data\n")
	dst := filepath.Join(t.TempDir(), "dst.txt")

	s, err := NewFileCopyBuilder(testFileCopyMctx())(dst, map[string]any{"source": src})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}

	// The created destination vanished externally; revert must tolerate it.
	if err := os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revert(ctx); err != nil {
		t.Fatalf("Revert must tolerate an already-missing created destination: %v", err)
	}
}

// TestFileCopyReApplyKeepsFirstBackup pins first-capture-wins: a re-Apply on
// the same instance (retry:, watch-forced runs) must not clobber the original
// destination backup with the copy's own content.
func TestFileCopyReApplyKeepsFirstBackup(t *testing.T) {
	ctx := context.Background()
	src := writeTempFile(t, "src.txt", "new\n")
	dst := writeTempFile(t, "dst.txt", "old\n")

	s, err := NewFileCopyBuilder(testFileCopyMctx())(dst, map[string]any{
		"source": src, "force": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Apply(ctx); err != nil {
		t.Fatalf("Apply #1: %v", err)
	}
	// Re-apply on the same instance: dst now holds the source content — the
	// backup memo must keep the first capture ("old\n").
	if _, err := s.Apply(ctx); err != nil {
		t.Fatalf("Apply #2: %v", err)
	}

	if _, err := s.Revert(ctx); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "old\n" {
		t.Errorf("content after revert: got %q, want original %q (backup clobbered by re-apply)", string(got), "old\n")
	}
}

// TestFileCopyCreatedPrecedenceOverBackup pins that wasCreated outranks a
// later backup capture: a destination this instance CREATED must be REMOVED
// by Revert even after a forced re-Apply saw it existing — never rewritten
// with the copy's own content.
func TestFileCopyCreatedPrecedenceOverBackup(t *testing.T) {
	ctx := context.Background()
	src := writeTempFile(t, "src.txt", "data\n")
	dst := filepath.Join(t.TempDir(), "dst.txt")

	s, err := NewFileCopyBuilder(testFileCopyMctx())(dst, map[string]any{
		"source": src, "force": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Apply(ctx); err != nil {
		t.Fatalf("Apply #1: %v", err)
	}
	// Re-apply (force path: dest exists and matches source): the destination
	// never PRE-existed, so Revert must still remove it.
	if _, err := s.Apply(ctx); err != nil {
		t.Fatalf("Apply #2: %v", err)
	}

	rr, err := s.Revert(ctx)
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if !rr.Changed {
		t.Error("expected Changed on revert of created destination")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("created destination must be removed by revert, not rewritten with source content")
	}
}

func TestFileCopyCheckReadErrorFails(t *testing.T) {
	ctx := context.Background()
	fake := exectest.NewFakeFileExec()
	fake.PreCreate("/src", []byte("data"), 0644)
	fake.PreCreate("/dst", []byte("old"), 0644)
	fake.SetReadError("/dst", errors.New("permission denied"))

	s, err := newFileCopy("/dst", map[string]any{"source": "/src", "force": true}, fake)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Check(ctx); err == nil {
		t.Error("expected Check to fail on a non-not-exist read error, not report the destination as absent")
	}
}

func TestFileCopyApplyReadErrorFails(t *testing.T) {
	ctx := context.Background()
	fake := exectest.NewFakeFileExec()
	fake.PreCreate("/src", []byte("data"), 0644)
	fake.PreCreate("/dst", []byte("old"), 0644)
	fake.SetReadError("/dst", errors.New("permission denied"))

	s, err := newFileCopy("/dst", map[string]any{"source": "/src", "force": true}, fake)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Apply(ctx); err == nil {
		t.Fatal("expected Apply to fail when the prior destination content cannot be captured")
	}
	got, ok := fake.GetFile("/dst")
	if !ok || string(got) != "old" {
		t.Errorf("destination overwritten despite read error: got %q", string(got))
	}
}
