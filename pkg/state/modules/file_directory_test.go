package modules

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
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

// lockedParentDir builds <tmp>/locked/<name> with content inside, then makes
// the parent unreadable so Stat on the child fails with EACCES (a genuine
// non-not-exist error). Returns the child path.
func lockedParentDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission errors cannot be simulated")
	}
	tmp := t.TempDir()
	parent := filepath.Join(tmp, "locked")
	target := filepath.Join(parent, "dir")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "data.txt"), []byte("precious"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0755) })
	return target
}

// TestFileDirectoryCheckStatErrorFails pins that a non-not-exist stat error
// (EACCES) fails the check phase instead of reporting phantom "does not
// exist" drift.
func TestFileDirectoryCheckStatErrorFails(t *testing.T) {
	target := lockedParentDir(t)

	s, err := NewFileDirectoryBuilder(testFileDirMctx())(target, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Check(context.Background()); err == nil {
		t.Error("expected Check to fail on a non-not-exist stat error, not report the directory as absent")
	}
}

// TestFileDirectoryApplyStatErrorDoesNotPoisonRevert pins that Apply fails on
// a non-not-exist stat error WITHOUT arming wasCreated — a poisoned memo
// would make a same-instance Revert RemoveAll a pre-existing tree.
func TestFileDirectoryApplyStatErrorDoesNotPoisonRevert(t *testing.T) {
	target := lockedParentDir(t)

	s, err := NewFileDirectoryBuilder(testFileDirMctx())(target, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := s.Apply(ctx); err == nil {
		t.Fatal("expected Apply to fail on a non-not-exist stat error")
	}

	// Restore access; the same-instance Revert must be a clean no-op that
	// leaves the pre-existing tree intact.
	if err := os.Chmod(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	rr, err := s.Revert(ctx)
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if rr.Changed {
		t.Error("revert after a failed apply must be a no-op (wasCreated poisoned)")
	}
	if _, err := os.Stat(filepath.Join(target, "data.txt")); err != nil {
		t.Errorf("pre-existing tree destroyed by revert: %v", err)
	}
}

// TestFileDirectoryConvergence walks Check → Apply → Check for both facets
// this module enforces: creation and mode.
func TestFileDirectoryConvergence(t *testing.T) {
	ctx := context.Background()

	// Facet: creation.
	created := filepath.Join(t.TempDir(), "newdir")
	s, err := NewFileDirectoryBuilder(testFileDirMctx())(created, map[string]any{"mode": "0750"})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange for missing directory")
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("creation facet did not converge, diff: %s", cr.Diff)
	}

	// Facet: mode on a pre-existing directory.
	existing := filepath.Join(t.TempDir(), "modedir")
	if err := os.Mkdir(existing, 0700); err != nil {
		t.Fatal(err)
	}
	s, err = NewFileDirectoryBuilder(testFileDirMctx())(existing, map[string]any{"mode": "0755"})
	if err != nil {
		t.Fatal(err)
	}
	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange for mode drift")
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("mode facet did not converge, diff: %s", cr.Diff)
	}
}

func TestFileDirectoryCheckOwnershipDrift(t *testing.T) {
	userName, uid, groupName, gid := testCurrentUserGroup(t)

	ctx := context.Background()
	fake := exectest.NewFakeFileExec()
	if err := fake.MkdirAll(ctx, "/opt/data", 0755); err != nil {
		t.Fatal(err)
	}
	fake.SetOwner("/opt/data", uid+1, gid+1)

	s, err := newFileDirectory("/opt/data", map[string]any{
		"mode": "0755", "user": userName, "group": groupName,
	}, fake)
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange when directory ownership drifts and mode matches")
	}

	fake.SetOwner("/opt/data", uid, gid)
	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatalf("Check converged: %v", err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change when ownership matches, diff: %s", cr.Diff)
	}
}

func TestFileDirectoryCheckOwnershipUndeclared(t *testing.T) {
	ctx := context.Background()
	fake := exectest.NewFakeFileExec()
	if err := fake.MkdirAll(ctx, "/opt/data", 0755); err != nil {
		t.Fatal(err)
	}
	// Arbitrary ownership: without user:/group: declared the facet never fires.
	fake.SetOwner("/opt/data", 12345, 54321)

	s, err := newFileDirectory("/opt/data", map[string]any{"mode": "0755"}, fake)
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if cr.NeedsChange {
		t.Errorf("undeclared ownership must not cause churn, diff: %s", cr.Diff)
	}
}
