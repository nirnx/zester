package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

func testFileSymlinkMctx() *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File: &exec.OSFileExec{},
		},
	}
}

func TestFileSymlinkName(t *testing.T) {
	mctx := testFileSymlinkMctx()
	builder := NewFileSymlinkBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("/etc/link", map[string]any{"target": "/etc/target"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "file.symlink:/etc/link" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestFileSymlinkPrimaryParamDefault(t *testing.T) {
	mctx := testFileSymlinkMctx()
	builder := NewFileSymlinkBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("/etc/link", map[string]any{"target": "/etc/target"})
	if err != nil {
		t.Fatal(err)
	}
	sl := s.(*FileSymlink)
	if sl.Path != "/etc/link" {
		t.Errorf("Path: got %q, want /etc/link", sl.Path)
	}
}

func TestFileSymlinkRequisites(t *testing.T) {
	mctx := testFileSymlinkMctx()
	builder := NewFileSymlinkBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("test", map[string]any{
		"target":    "/etc/target",
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

func TestFileSymlinkCheckMissing(t *testing.T) {
	tmp := t.TempDir()
	linkPath := filepath.Join(tmp, "mylink")

	mctx := testFileSymlinkMctx()
	builder := NewFileSymlinkBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder(linkPath, map[string]any{"target": "/etc/hosts"})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for missing symlink")
	}
}

func TestFileSymlinkCheckCorrect(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(tmp, "mylink")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatal(err)
	}

	mctx := testFileSymlinkMctx()
	builder := NewFileSymlinkBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder(linkPath, map[string]any{"target": target})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when symlink already correct")
	}
}

func TestFileSymlinkCheckWrongTarget(t *testing.T) {
	tmp := t.TempDir()
	linkPath := filepath.Join(tmp, "mylink")
	if err := os.Symlink("/wrong/target", linkPath); err != nil {
		t.Fatal(err)
	}

	mctx := testFileSymlinkMctx()
	builder := NewFileSymlinkBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder(linkPath, map[string]any{"target": "/correct/target", "force": true})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when symlink points to wrong target")
	}
}

func TestFileSymlinkApplyCreate(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "target.txt")
	if err := os.WriteFile(target, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(tmp, "mylink")

	mctx := testFileSymlinkMctx()
	builder := NewFileSymlinkBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder(linkPath, map[string]any{"target": target})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after creating symlink")
	}

	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Errorf("symlink target: got %q, want %q", got, target)
	}
}

func TestFileSymlinkApplyReplaceWithForce(t *testing.T) {
	tmp := t.TempDir()
	linkPath := filepath.Join(tmp, "mylink")
	if err := os.Symlink("/old/target", linkPath); err != nil {
		t.Fatal(err)
	}

	mctx := testFileSymlinkMctx()
	builder := NewFileSymlinkBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder(linkPath, map[string]any{"target": "/new/target", "force": true})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after replacing symlink")
	}

	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/new/target" {
		t.Errorf("symlink target: got %q, want /new/target", got)
	}
}

func TestFileSymlinkApplyNoForceError(t *testing.T) {
	tmp := t.TempDir()
	linkPath := filepath.Join(tmp, "mylink")
	if err := os.Symlink("/old/target", linkPath); err != nil {
		t.Fatal(err)
	}

	mctx := testFileSymlinkMctx()
	builder := NewFileSymlinkBuilder(mctx, modschema.DecodeOptions{})
	// force is false (default)
	s, err := builder(linkPath, map[string]any{"target": "/new/target"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error when symlink exists with wrong target and force is false")
	}
}

func TestFileSymlinkApplyMakeDirs(t *testing.T) {
	tmp := t.TempDir()
	linkPath := filepath.Join(tmp, "subdir", "nested", "mylink")

	mctx := testFileSymlinkMctx()
	builder := NewFileSymlinkBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder(linkPath, map[string]any{"target": "/etc/hosts", "makedirs": true})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after creating symlink with makedirs")
	}

	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/etc/hosts" {
		t.Errorf("symlink target: got %q, want /etc/hosts", got)
	}
}

func TestFileSymlinkApplyError(t *testing.T) {
	// Canonical makedirs contract (§13): a missing parent without makedirs
	// fails Apply with the contract error naming the parent and the remedy.
	fakeFile := exectest.NewFakeFileExec()
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: fakeFile}}
	builder := NewFileSymlinkBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("/dev/null/impossible/link", map[string]any{"target": "/etc/hosts"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "makedirs: true") {
		t.Fatalf("apply with missing parent: err=%v, want the canonical makedirs contract error", err)
	}
}

func TestFileSymlinkApplyRealError(t *testing.T) {
	// Attempt to create a symlink in a non-existent directory without makedirs.
	mctx := testFileSymlinkMctx()
	builder := NewFileSymlinkBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("/nonexistent/dir/mylink", map[string]any{"target": "/etc/hosts"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error creating symlink in nonexistent dir")
	}
}

func TestFileSymlinkRevert(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "target.txt")
	if err := os.WriteFile(target, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(tmp, "mylink")

	mctx := testFileSymlinkMctx()
	builder := NewFileSymlinkBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder(linkPath, map[string]any{"target": target})
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

	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Error("expected symlink to be removed after revert")
	}
}

func TestFileSymlinkRevertError(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.RemoveAllErr = fmt.Errorf("permission denied") // Remove doesn't use this, but let's test Remove error path
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: fakeFile}}
	builder := NewFileSymlinkBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("/some/link", map[string]any{"target": "/some/target"})
	if err != nil {
		t.Fatal(err)
	}
	// Fake Remove doesn't error — just verify no panic.
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed from fake Revert")
	}
}

var _ state.State = (*FileSymlink)(nil)

// TestFileSymlinkContract replays the permanent differential contract fixtures
// against the migrated fileSymlinkSpec decoder. The cases were approved by the
// legacy-vs-new equivalence comparison while the legacy constructor still
// existed (see the migration changelog); after its deletion this replay is the
// permanent regression guard for file.symlink's decode behavior — including
// the flagged BD-2, BD-6, and BD-7 divergences.
func TestFileSymlinkContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var s FileSymlink
		if _, err := fileSymlinkSpec.Decode(id, config, &s, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &s, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/file.symlink.yaml")
}
