package modules

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

func testFileDirMctx() *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File: &exec.OSFileExec{},
		},
	}
}

func TestFileDirectoryName(t *testing.T) {
	mctx := testFileDirMctx()
	builder := NewFileDirectoryBuilder(mctx)
	s, err := builder("/tmp/mydir", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "file.directory:/tmp/mydir" {
		t.Errorf("Name: got %q, want %q", s.Name(), "file.directory:/tmp/mydir")
	}
}

func TestFileDirectoryRequisites(t *testing.T) {
	mctx := testFileDirMctx()
	builder := NewFileDirectoryBuilder(mctx)
	s, err := builder("test", map[string]any{
		"require":   []any{"pkg.installed:nginx"},
		"watch":     []any{"file.managed:/etc/nginx"},
		"onchanges": []any{"cmd.run:setup"},
		"onfail":    []any{"cmd.run:fallback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:nginx" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/nginx" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:setup" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:fallback" {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}

func TestFileDirectoryCheckNotExists(t *testing.T) {
	tmp := t.TempDir()
	dirPath := filepath.Join(tmp, "newdir")

	mctx := testFileDirMctx()
	builder := NewFileDirectoryBuilder(mctx)
	s, err := builder(dirPath, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for nonexistent directory")
	}
}

func TestFileDirectoryCheckExistsCorrectMode(t *testing.T) {
	tmp := t.TempDir()
	dirPath := filepath.Join(tmp, "existdir")
	if err := os.Mkdir(dirPath, 0755); err != nil {
		t.Fatal(err)
	}

	mctx := testFileDirMctx()
	builder := NewFileDirectoryBuilder(mctx)
	s, err := builder(dirPath, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change for existing dir with default mode, got diff: %s", cr.Diff)
	}
}

func TestFileDirectoryCheckWrongMode(t *testing.T) {
	tmp := t.TempDir()
	dirPath := filepath.Join(tmp, "wrongmode")
	if err := os.Mkdir(dirPath, 0700); err != nil {
		t.Fatal(err)
	}

	mctx := testFileDirMctx()
	builder := NewFileDirectoryBuilder(mctx)
	s, err := builder(dirPath, map[string]any{
		"mode": "0755",
	})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for wrong mode")
	}
}

func TestFileDirectoryApplyCreates(t *testing.T) {
	tmp := t.TempDir()
	dirPath := filepath.Join(tmp, "created")

	mctx := testFileDirMctx()
	builder := NewFileDirectoryBuilder(mctx)
	s, err := builder(dirPath, map[string]any{
		"mode": "0750",
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after creating directory")
	}

	info, err := os.Stat(dirPath)
	if err != nil {
		t.Fatalf("directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected a directory")
	}
	if info.Mode().Perm() != 0750 {
		t.Errorf("mode: got %04o, want %04o", info.Mode().Perm(), 0750)
	}
}

func TestFileDirectoryApplySetsMode(t *testing.T) {
	tmp := t.TempDir()
	dirPath := filepath.Join(tmp, "chmoddir")
	if err := os.Mkdir(dirPath, 0700); err != nil {
		t.Fatal(err)
	}

	mctx := testFileDirMctx()
	builder := NewFileDirectoryBuilder(mctx)
	s, err := builder(dirPath, map[string]any{
		"mode": "0755",
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after chmod")
	}

	info, err := os.Stat(dirPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("mode: got %04o, want %04o", info.Mode().Perm(), 0755)
	}
}

func TestFileDirectoryRevertCreated(t *testing.T) {
	tmp := t.TempDir()
	dirPath := filepath.Join(tmp, "revertdir")

	mctx := testFileDirMctx()
	builder := NewFileDirectoryBuilder(mctx)
	s, err := builder(dirPath, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	// Apply first to set wasCreated.
	_, err = s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed on revert of created directory")
	}

	if _, err := os.Stat(dirPath); !os.IsNotExist(err) {
		t.Error("expected directory to be removed after revert")
	}
}

func TestFileDirectoryRevertNoChange(t *testing.T) {
	tmp := t.TempDir()
	// Pre-existing directory: revert should not remove it.
	dirPath := filepath.Join(tmp, "preexist")
	if err := os.Mkdir(dirPath, 0755); err != nil {
		t.Fatal(err)
	}

	mctx := testFileDirMctx()
	builder := NewFileDirectoryBuilder(mctx)
	s, err := builder(dirPath, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	// Apply on existing dir (wasCreated stays false).
	_, err = s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected no change on revert for pre-existing directory")
	}
}

func TestFileDirectoryPrimaryParamDefault(t *testing.T) {
	mctx := testFileDirMctx()
	builder := NewFileDirectoryBuilder(mctx)
	s, err := builder("/var/log/app", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	fd := s.(*FileDirectory)
	if fd.Path != "/var/log/app" {
		t.Errorf("Path: got %q, want %q", fd.Path, "/var/log/app")
	}
}

func TestFileDirectoryNameFromConfig(t *testing.T) {
	mctx := testFileDirMctx()
	builder := NewFileDirectoryBuilder(mctx)
	s, err := builder("data-dir", map[string]any{
		"name": "/opt/data",
	})
	if err != nil {
		t.Fatal(err)
	}
	fd := s.(*FileDirectory)
	if fd.Path != "/opt/data" {
		t.Errorf("Path: got %q, want %q", fd.Path, "/opt/data")
	}
}

func TestFileDirectoryApplyInvalidMode(t *testing.T) {
	mctx := testFileDirMctx()
	builder := NewFileDirectoryBuilder(mctx)
	s, err := builder("/tmp/badmode", map[string]any{
		"mode": "not-octal",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from invalid mode string")
	}
}

func TestFileDirectoryCheckIsFile(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "notadir")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileDirMctx()
	builder := NewFileDirectoryBuilder(mctx)
	s, err := builder(filePath, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when path exists but is not a directory")
	}
}

// Verify the State interface is fully satisfied at compile time.
var _ state.State = (*FileDirectory)(nil)
