package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

func testFileAbsentMctx() *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File: &exec.OSFileExec{},
		},
	}
}

func TestFileAbsentName(t *testing.T) {
	mctx := testFileAbsentMctx()
	builder := NewFileAbsentBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("/tmp/gone.txt", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "file.absent:/tmp/gone.txt" {
		t.Errorf("Name: got %q, want %q", s.Name(), "file.absent:/tmp/gone.txt")
	}
}

func TestFileAbsentPrimaryParamDefault(t *testing.T) {
	mctx := testFileAbsentMctx()
	builder := NewFileAbsentBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("/var/log/old.log", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	fa := s.(*FileAbsent)
	if fa.Path != "/var/log/old.log" {
		t.Errorf("Path: got %q, want %q", fa.Path, "/var/log/old.log")
	}
}

func TestFileAbsentRequisites(t *testing.T) {
	mctx := testFileAbsentMctx()
	builder := NewFileAbsentBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("test", map[string]any{
		"require":   []any{"cmd.run:stop-service"},
		"watch":     []any{"file.managed:/etc/conf"},
		"onchanges": []any{"cmd.run:cleanup"},
		"onfail":    []any{"cmd.run:alert"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "cmd.run:stop-service" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/conf" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:cleanup" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:alert" {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}

func TestFileAbsentCheckFileNotExists(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "nonexistent.txt")

	mctx := testFileAbsentMctx()
	builder := NewFileAbsentBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder(filePath, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change for nonexistent file")
	}
}

func TestFileAbsentCheckFileExists(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "exists.txt")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileAbsentMctx()
	builder := NewFileAbsentBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder(filePath, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for existing file")
	}
}

func TestFileAbsentApplyRemovesFile(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "remove-me.txt")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileAbsentMctx()
	builder := NewFileAbsentBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder(filePath, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after removing file")
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Error("expected file to be removed")
	}
}

func TestFileAbsentApplyRemovesDirectoryTree(t *testing.T) {
	tmp := t.TempDir()
	dirPath := filepath.Join(tmp, "removedir")
	if err := os.MkdirAll(filepath.Join(dirPath, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirPath, "sub", "file.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileAbsentMctx()
	builder := NewFileAbsentBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder(dirPath, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after removing directory tree")
	}

	if _, err := os.Stat(dirPath); !os.IsNotExist(err) {
		t.Error("expected directory tree to be removed")
	}
}

func TestFileAbsentNameFromConfig(t *testing.T) {
	mctx := testFileAbsentMctx()
	builder := NewFileAbsentBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("remove-old", map[string]any{
		"name": "/var/tmp/old",
	})
	if err != nil {
		t.Fatal(err)
	}
	fa := s.(*FileAbsent)
	if fa.Path != "/var/tmp/old" {
		t.Errorf("Path: got %q, want %q", fa.Path, "/var/tmp/old")
	}
}

func TestFileAbsentApplyError(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.RemoveAllErr = fmt.Errorf("permission denied")
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File: fakeFile,
		},
	}
	builder := NewFileAbsentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("/some/protected/path", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from failed remove")
	}
}

func TestFileAbsentRevert(t *testing.T) {
	mctx := testFileAbsentMctx()
	builder := NewFileAbsentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("/some/path", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected no change from revert (cannot revert file removal)")
	}
}

// Verify the State interface is fully satisfied at compile time.
var _ state.State = (*FileAbsent)(nil)

// TestFileAbsentContract replays the permanent differential contract fixtures
// against the migrated fileAbsentSpec decoder. The cases were approved by the
// legacy-vs-new equivalence comparison while the legacy constructor still
// existed (see the migration changelog); after its deletion this replay is the
// permanent regression guard for file.absent's decode behavior — including the
// flagged BD-6 divergence (a non-string `name` is now coerced, or rejected for
// composites, instead of silently falling back to the state ID).
func TestFileAbsentContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var f FileAbsent
		if _, err := fileAbsentSpec.Decode(id, config, &f, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &f, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/file.absent.yaml")
}
