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

	// Check now verifies dir_mode (default 0755) on every managed directory,
	// including the dest root — align the temp dir explicitly.
	if err := os.Chmod(dest, 0755); err != nil {
		t.Fatal(err)
	}

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

func TestFileRecurseCheckCleanDetectsExtras(t *testing.T) {
	ctx := context.Background()
	src := setupSourceDir(t)
	dest := filepath.Join(t.TempDir(), "dest")

	s, err := NewFileRecurseBuilder(testFileRecurseMctx())(dest, map[string]any{
		"source": src, "clean": true, "makedirs": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Converge.
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Fatalf("expected converged tree after apply, diff: %s", cr.Diff)
	}

	// A stray file appears in dest: clean:true must surface it in Check.
	extra := filepath.Join(dest, "stray.txt")
	if err := os.WriteFile(extra, []byte("rogue"), 0644); err != nil {
		t.Fatal(err)
	}
	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange for a stray dest file with clean: true")
	}

	// Apply removes it and Check converges again.
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(extra); !os.IsNotExist(err) {
		t.Error("expected stray file to be cleaned by apply")
	}
	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected converged tree after clean, diff: %s", cr.Diff)
	}
}

func TestFileRecurseCheckCleanExtrasIgnoredWithoutClean(t *testing.T) {
	ctx := context.Background()
	src := setupSourceDir(t)
	dest := filepath.Join(t.TempDir(), "dest")

	s, err := NewFileRecurseBuilder(testFileRecurseMctx())(dest, map[string]any{
		"source": src, "makedirs": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dest, "stray.txt"), []byte("rogue"), 0644); err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("extras must not cause churn without clean: true, diff: %s", cr.Diff)
	}
}

func TestFileRecurseCheckCleanStrayDirNoChurn(t *testing.T) {
	ctx := context.Background()
	src := setupSourceDir(t)
	dest := filepath.Join(t.TempDir(), "dest")

	s, err := NewFileRecurseBuilder(testFileRecurseMctx())(dest, map[string]any{
		"source": src, "clean": true, "makedirs": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}

	// cleanDestination never removes directories, so an empty stray dir must
	// not flag Check either — otherwise the state churns forever.
	if err := os.MkdirAll(filepath.Join(dest, "straydir"), 0755); err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("empty stray dir must not churn (apply cannot remove it), diff: %s", cr.Diff)
	}
}

func TestFileRecurseCheckCleanDestMissing(t *testing.T) {
	src := setupSourceDir(t)
	dest := filepath.Join(t.TempDir(), "nonexistent-dest")

	s, err := NewFileRecurseBuilder(testFileRecurseMctx())(dest, map[string]any{
		"source": src, "clean": true, "makedirs": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// A missing dest with clean: true must not error — the source walk
	// already accounts for every missing entry.
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatalf("Check with missing dest: %v", err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when dest is missing")
	}
}

func TestFileRecurseCheckDirModeDrift(t *testing.T) {
	ctx := context.Background()
	src := setupSourceDir(t)
	dest := filepath.Join(t.TempDir(), "dest")

	s, err := NewFileRecurseBuilder(testFileRecurseMctx())(dest, map[string]any{
		"source": src, "dir_mode": "0750", "makedirs": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Fatalf("expected converged tree after apply, diff: %s", cr.Diff)
	}

	// Drift a dest subdirectory's mode: Check must flag it, Apply must fix it.
	sub := filepath.Join(dest, "subdir")
	if err := os.Chmod(sub, 0777); err != nil {
		t.Fatal(err)
	}
	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange on dir_mode drift")
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sub)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0750 {
		t.Errorf("dir mode after re-apply: got %04o, want 0750", info.Mode().Perm())
	}
}

func TestFileRecurseCheckOwnershipDrift(t *testing.T) {
	userName, uid, _, _ := testCurrentUserGroup(t)

	ctx := context.Background()
	fake := exectest.NewFakeFileExec()
	// Flat source tree (files only) built entirely in the fake.
	fake.PreCreate("/src/app.conf", []byte("conf"), 0644)

	s, err := newFileRecurse("/dst", map[string]any{
		"source": "/src", "user": userName,
	}, fake)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Fatalf("expected converged tree after apply (chowned), diff: %s", cr.Diff)
	}

	// Ownership drift on a dest file must flag Check.
	fake.SetOwner("/dst/app.conf", uid+1, 0)
	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange on dest file ownership drift")
	}
}

func TestFileRecurseCheckOwnershipUndeclared(t *testing.T) {
	ctx := context.Background()
	fake := exectest.NewFakeFileExec()
	fake.PreCreate("/src/app.conf", []byte("conf"), 0644)
	fake.PreCreate("/dst/app.conf", []byte("conf"), 0644)
	fake.SetOwner("/dst/app.conf", 12345, 54321)

	s, err := newFileRecurse("/dst", map[string]any{"source": "/src"}, fake)
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("undeclared ownership must not cause churn, diff: %s", cr.Diff)
	}
}

// TestFileRecurseWalksViaFileExec pins the module-layer contract: every
// filesystem access — including directory walks — goes through the injected
// FileExec, so the whole flow works against the in-memory fake.
func TestFileRecurseWalksViaFileExec(t *testing.T) {
	ctx := context.Background()
	fake := exectest.NewFakeFileExec()
	fake.PreCreate("/src/a.txt", []byte("alpha"), 0644)
	fake.PreCreate("/src/b.txt", []byte("beta"), 0644)
	// A stray file for clean mode, and a dest file matching source.
	fake.PreCreate("/dst/a.txt", []byte("alpha"), 0644)
	fake.PreCreate("/dst/stray.txt", []byte("rogue"), 0644)

	s, err := newFileRecurse("/dst", map[string]any{
		"source": "/src", "clean": true,
	}, fake)
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatalf("Check against fake: %v", err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange (missing b.txt + stray extra)")
	}

	if _, err := s.Apply(ctx); err != nil {
		t.Fatalf("Apply against fake: %v", err)
	}
	if data, ok := fake.GetFile("/dst/b.txt"); !ok || string(data) != "beta" {
		t.Errorf("b.txt not copied into fake: %q", string(data))
	}
	if fake.Exists("/dst/stray.txt") {
		t.Error("stray file not cleaned in fake")
	}

	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected converged tree in fake, diff: %s", cr.Diff)
	}
}

func TestFileRecurseCheckReadErrorFails(t *testing.T) {
	ctx := context.Background()
	fake := exectest.NewFakeFileExec()
	fake.PreCreate("/src/a.txt", []byte("alpha"), 0644)
	fake.PreCreate("/dst/a.txt", []byte("alpha"), 0644)
	fake.SetReadError("/dst/a.txt", errors.New("permission denied"))

	s, err := newFileRecurse("/dst", map[string]any{"source": "/src"}, fake)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Check(ctx); err == nil {
		t.Error("expected Check to fail on a non-not-exist read error, not count the file as absent")
	}
}

func TestFileRecurseRevertFreshInstanceNoOp(t *testing.T) {
	src := setupSourceDir(t)
	dest := filepath.Join(t.TempDir(), "dest")

	s, err := NewFileRecurseBuilder(testFileRecurseMctx())(dest, map[string]any{"source": src})
	if err != nil {
		t.Fatal(err)
	}

	// No Apply recorded on this instance: clean no-op.
	rr, err := s.Revert(context.Background())
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if rr.Changed {
		t.Error("fresh-instance revert must not report a change")
	}
}

var _ state.State = (*FileRecurse)(nil)
