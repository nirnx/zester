package modules

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
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
