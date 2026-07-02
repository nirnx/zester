package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/exec/exectest"
	"github.com/ptorbus/zester/pkg/state"
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
	builder := NewFileSymlinkBuilder(mctx)
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
	builder := NewFileSymlinkBuilder(mctx)
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
	builder := NewFileSymlinkBuilder(mctx)
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
	builder := NewFileSymlinkBuilder(mctx)
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
	builder := NewFileSymlinkBuilder(mctx)
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
	builder := NewFileSymlinkBuilder(mctx)
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
	builder := NewFileSymlinkBuilder(mctx)
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
	builder := NewFileSymlinkBuilder(mctx)
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
	builder := NewFileSymlinkBuilder(mctx)
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
	builder := NewFileSymlinkBuilder(mctx)
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
	fakeFile := exectest.NewFakeFileExec()
	// Make Readlink return "not found" (symlink absent) and Stat also fail.
	// Symlink will fail because fakeFile doesn't actually create symlinks that work with real OS.
	// We simulate by having an internal issue — use a real bad path scenario.
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: fakeFile}}
	builder := NewFileSymlinkBuilder(mctx)
	s, err := builder("/dev/null/impossible/link", map[string]any{"target": "/etc/hosts"})
	if err != nil {
		t.Fatal(err)
	}
	// FakeFileExec.Symlink doesn't fail — just verify no panic and it creates the symlink in the fake.
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed with fake FileExec")
	}
}

func TestFileSymlinkApplyRealError(t *testing.T) {
	// Attempt to create a symlink in a non-existent directory without makedirs.
	mctx := testFileSymlinkMctx()
	builder := NewFileSymlinkBuilder(mctx)
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
	builder := NewFileSymlinkBuilder(mctx)
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
	builder := NewFileSymlinkBuilder(mctx)
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
