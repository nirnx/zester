package modules

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/state"
)

func testFileAppendMctx() *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File: &exec.OSFileExec{},
		},
	}
}

func TestFileAppendName(t *testing.T) {
	mctx := testFileAppendMctx()
	builder := NewFileAppendBuilder(mctx)
	s, err := builder("/etc/hosts", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "file.append:/etc/hosts" {
		t.Errorf("Name: got %q, want %q", s.Name(), "file.append:/etc/hosts")
	}
}

func TestFileAppendPrimaryParamDefault(t *testing.T) {
	mctx := testFileAppendMctx()
	builder := NewFileAppendBuilder(mctx)
	s, err := builder("/etc/resolv.conf", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	fa := s.(*FileAppend)
	if fa.Path != "/etc/resolv.conf" {
		t.Errorf("Path: got %q, want %q", fa.Path, "/etc/resolv.conf")
	}
}

func TestFileAppendRequisites(t *testing.T) {
	mctx := testFileAppendMctx()
	builder := NewFileAppendBuilder(mctx)
	s, err := builder("test", map[string]any{
		"require":   []any{"pkg.installed:bind"},
		"watch":     []any{"file.managed:/etc/named.conf"},
		"onchanges": []any{"cmd.run:reload-dns"},
		"onfail":    []any{"cmd.run:alert"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:bind" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/named.conf" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:reload-dns" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:alert" {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}

func TestFileAppendCheckFileNotExists(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "newfile.conf")

	mctx := testFileAppendMctx()
	builder := NewFileAppendBuilder(mctx)
	s, err := builder(filePath, map[string]any{
		"text": []any{"line1", "line2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for nonexistent file")
	}
}

func TestFileAppendCheckAllLinesPresent(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "complete.conf")
	if err := os.WriteFile(filePath, []byte("line1\nline2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileAppendMctx()
	builder := NewFileAppendBuilder(mctx)
	s, err := builder(filePath, map[string]any{
		"text": []any{"line1", "line2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change when all lines present, got diff: %s", cr.Diff)
	}
}

func TestFileAppendCheckSomeLinesMissing(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "partial.conf")
	if err := os.WriteFile(filePath, []byte("line1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileAppendMctx()
	builder := NewFileAppendBuilder(mctx)
	s, err := builder(filePath, map[string]any{
		"text": []any{"line1", "line2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when some lines are missing")
	}
}

func TestFileAppendApplyToExistingFile(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "existing.conf")
	if err := os.WriteFile(filePath, []byte("existing\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileAppendMctx()
	builder := NewFileAppendBuilder(mctx)
	s, err := builder(filePath, map[string]any{
		"text": []any{"new-line"},
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after appending")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if content != "existing\nnew-line\n" {
		t.Errorf("content: got %q, want %q", content, "existing\nnew-line\n")
	}
}

func TestFileAppendApplyCreatesNewFile(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "brand-new.conf")

	mctx := testFileAppendMctx()
	builder := NewFileAppendBuilder(mctx)
	s, err := builder(filePath, map[string]any{
		"text": []any{"first-line", "second-line"},
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after creating new file")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if content != "first-line\nsecond-line\n" {
		t.Errorf("content: got %q, want %q", content, "first-line\nsecond-line\n")
	}
}

func TestFileAppendApplyIdempotent(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "idem.conf")
	if err := os.WriteFile(filePath, []byte("line1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileAppendMctx()
	builder := NewFileAppendBuilder(mctx)

	// First apply: appends line2.
	s1, err := builder(filePath, map[string]any{
		"text": []any{"line1", "line2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ar1, err := s1.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar1.Changed {
		t.Error("first apply should have changed")
	}

	// Second apply: both lines already present.
	s2, err := builder(filePath, map[string]any{
		"text": []any{"line1", "line2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ar2, err := s2.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar2.Changed {
		t.Error("second apply should not have changed (idempotent)")
	}
}

func TestFileAppendRevertRestoresOriginal(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "revert.conf")
	original := "original content\n"
	if err := os.WriteFile(filePath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileAppendMctx()
	builder := NewFileAppendBuilder(mctx)
	s, err := builder(filePath, map[string]any{
		"text": []any{"appended-line"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Apply first to populate backup.
	_, err = s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed on revert")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("content after revert: got %q, want %q", string(data), original)
	}
}

func TestFileAppendRevertRemovesNewFile(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "revert-new.conf")

	mctx := testFileAppendMctx()
	builder := NewFileAppendBuilder(mctx)
	s, err := builder(filePath, map[string]any{
		"text": []any{"new-line"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Apply creates the file.
	_, err = s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Revert should remove it (file didn't exist before).
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed on revert of new file")
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Error("expected file to be removed after revert")
	}
}

func TestFileAppendNameFromConfig(t *testing.T) {
	mctx := testFileAppendMctx()
	builder := NewFileAppendBuilder(mctx)
	s, err := builder("add-hosts", map[string]any{
		"name": "/etc/hosts",
	})
	if err != nil {
		t.Fatal(err)
	}
	fa := s.(*FileAppend)
	if fa.Path != "/etc/hosts" {
		t.Errorf("Path: got %q, want %q", fa.Path, "/etc/hosts")
	}
}

func TestFileAppendApplyError(t *testing.T) {
	tmp := t.TempDir()
	readOnlyDir := filepath.Join(tmp, "readonly")
	if err := os.MkdirAll(readOnlyDir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(readOnlyDir, 0755)

	filePath := filepath.Join(readOnlyDir, "cantwrite")

	mctx := testFileAppendMctx()
	builder := NewFileAppendBuilder(mctx)
	s, err := builder(filePath, map[string]any{
		"text": []any{"blocked line"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from write to read-only directory")
	}
}

func TestFileAppendApplyNoTrailingNewline(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "notrailing")
	if err := os.WriteFile(filePath, []byte("line1"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileAppendMctx()
	builder := NewFileAppendBuilder(mctx)
	s, err := builder(filePath, map[string]any{
		"text": []any{"line2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if content != "line1\nline2\n" {
		t.Errorf("expected newline between existing and appended, got: %q", content)
	}
}

func TestFileAppendFreshInstanceRevertIsNoOp(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "fresh.conf")
	original := "keep me\n"
	if err := os.WriteFile(filePath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	s, err := NewFileAppendBuilder(testFileAppendMctx())(filePath, map[string]any{
		"text": []any{"line"},
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if ar.Changed {
		t.Error("fresh-instance revert must not report a change")
	}
	if ar.Diff != fsxNothingToRevert {
		t.Errorf("Diff: got %q, want %q", ar.Diff, fsxNothingToRevert)
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("file must survive a fresh-instance revert: %v", err)
	}
	if string(got) != original {
		t.Errorf("content after revert: got %q, want %q", string(got), original)
	}
}

func TestFileAppendReadErrorFailsCheckAndApply(t *testing.T) {
	ctx := context.Background()
	fake := exectest.NewFakeFileExec()
	fake.PreCreate("/etc/hosts", []byte("127.0.0.1 localhost\n"), 0644)
	fake.SetReadError("/etc/hosts", errors.New("input/output error"))
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: fake}}
	s, err := NewFileAppendBuilder(mctx)("/etc/hosts", map[string]any{
		"text": []any{"10.0.0.1 db"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Check(ctx); err == nil {
		t.Error("Check: want error when read fails with a non-not-exist error")
	}
	if _, err := s.Apply(ctx); err == nil {
		t.Error("Apply: want error when read fails with a non-not-exist error")
	}
	if got, _ := fake.GetFile("/etc/hosts"); string(got) != "127.0.0.1 localhost\n" {
		t.Errorf("file must not be modified on a read error: got %q", string(got))
	}
}

// Verify the State interface is fully satisfied at compile time.
var _ state.State = (*FileAppend)(nil)
