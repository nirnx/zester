package modules

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

func testFileRecurseMctx() *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File: &exec.OSFileExec{},
		},
	}
}

func TestFileRecurseName(t *testing.T) {
	mctx := testFileRecurseMctx()
	builder := NewFileRecurseBuilder(mctx)
	s, err := builder("/etc/dest", map[string]any{"source": "/etc/src"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "file.recurse:/etc/dest" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestFileRecursePrimaryParamDefault(t *testing.T) {
	mctx := testFileRecurseMctx()
	builder := NewFileRecurseBuilder(mctx)
	s, err := builder("/etc/dest", map[string]any{"source": "/etc/src"})
	if err != nil {
		t.Fatal(err)
	}
	fr := s.(*FileRecurse)
	if fr.Dest != "/etc/dest" {
		t.Errorf("Dest: got %q, want /etc/dest", fr.Dest)
	}
}

func TestFileRecurseRequisites(t *testing.T) {
	mctx := testFileRecurseMctx()
	builder := NewFileRecurseBuilder(mctx)
	s, err := builder("test", map[string]any{
		"source":    "/src",
		"require":   []any{"pkg.installed:nginx"},
		"watch":     []any{"file.managed:/etc/conf"},
		"onchanges": []any{"cmd.run:build"},
		"onfail":    []any{"cmd.run:primary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:nginx" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/conf" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:build" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:primary" {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}

// setupSourceDir creates a source directory tree for tests.
func setupSourceDir(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "file1.txt"), []byte("content1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "subdir", "file2.txt"), []byte("content2"), 0644); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestFileRecurseCheckNeedsChange(t *testing.T) {
	src := setupSourceDir(t)
	dest := filepath.Join(t.TempDir(), "dest")

	mctx := testFileRecurseMctx()
	builder := NewFileRecurseBuilder(mctx)
	s, err := builder(dest, map[string]any{"source": src})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when dest is empty")
	}
}

func TestFileRecurseCheckNoChange(t *testing.T) {
	src := setupSourceDir(t)
	dest := t.TempDir()

	// Pre-populate dest to match source.
	if err := os.MkdirAll(filepath.Join(dest, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "file1.txt"), []byte("content1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "subdir", "file2.txt"), []byte("content2"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileRecurseMctx()
	builder := NewFileRecurseBuilder(mctx)
	s, err := builder(dest, map[string]any{"source": src, "file_mode": "0644"})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change, diff: %s", cr.Diff)
	}
}

func TestFileRecurseCheckMissingSource(t *testing.T) {
	dest := t.TempDir()

	mctx := testFileRecurseMctx()
	builder := NewFileRecurseBuilder(mctx)
	s, err := builder(dest, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Check(context.Background())
	if err == nil {
		t.Error("expected error when source is empty")
	}
}

func TestFileRecurseApply(t *testing.T) {
	src := setupSourceDir(t)
	dest := filepath.Join(t.TempDir(), "dest")

	mctx := testFileRecurseMctx()
	builder := NewFileRecurseBuilder(mctx)
	s, err := builder(dest, map[string]any{"source": src, "makedirs": true})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after copy")
	}

	// Verify files were copied.
	data, err := os.ReadFile(filepath.Join(dest, "file1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "content1" {
		t.Errorf("file1.txt: got %q, want %q", data, "content1")
	}

	data, err = os.ReadFile(filepath.Join(dest, "subdir", "file2.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "content2" {
		t.Errorf("subdir/file2.txt: got %q, want %q", data, "content2")
	}
}

func TestFileRecurseApplyError(t *testing.T) {
	dest := t.TempDir()

	mctx := testFileRecurseMctx()
	builder := NewFileRecurseBuilder(mctx)
	// Source does not exist.
	s, err := builder(dest, map[string]any{"source": "/nonexistent/source/dir"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error with nonexistent source")
	}
}

func TestFileRecurseApplyClean(t *testing.T) {
	src := setupSourceDir(t)
	dest := t.TempDir()

	// Place an extra file in dest that is not in source.
	extraFile := filepath.Join(dest, "extra.txt")
	if err := os.WriteFile(extraFile, []byte("extra"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileRecurseMctx()
	builder := NewFileRecurseBuilder(mctx)
	s, err := builder(dest, map[string]any{"source": src, "clean": true})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after copy with clean")
	}

	// Extra file should be removed.
	if _, err := os.Stat(extraFile); !os.IsNotExist(err) {
		t.Error("expected extra file to be cleaned")
	}
}

func TestFileRecurseRevert(t *testing.T) {
	src := setupSourceDir(t)
	dest := filepath.Join(t.TempDir(), "dest")

	mctx := testFileRecurseMctx()
	builder := NewFileRecurseBuilder(mctx)
	s, err := builder(dest, map[string]any{"source": src, "makedirs": true})
	if err != nil {
		t.Fatal(err)
	}

	// Apply first.
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Revert.
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after revert")
	}

	// Created files should be removed.
	if _, err := os.Stat(filepath.Join(dest, "file1.txt")); !os.IsNotExist(err) {
		t.Error("expected file1.txt to be removed after revert")
	}
}

var _ state.State = (*FileRecurse)(nil)
