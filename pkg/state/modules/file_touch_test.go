package modules

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/exec/exectest"
)

func testFileTouchMctx() *exec.ModuleContext {
	return &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: &exec.OSFileExec{}}}
}

func TestFileTouchName(t *testing.T) {
	s, err := NewFileTouchBuilder(testFileTouchMctx())("/var/run/app.stamp", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "file.touch:/var/run/app.stamp" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestFileTouchMissingProvider(t *testing.T) {
	_, err := NewFileTouchBuilder(&exec.ModuleContext{})("/tmp/x", map[string]any{})
	if err == nil {
		t.Fatal("expected error when File provider is nil")
	}
}

func TestFileTouchCreatesFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "stamp")

	s, err := NewFileTouchBuilder(testFileTouchMctx())(path, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange when file is missing")
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after creating file")
	}
	if ar.Details["action"] != "created" {
		t.Errorf("Details action: got %v", ar.Details)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty file, got %q", string(got))
	}

	// Idempotent: file now exists, Check reports no change.
	cr2, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr2.NeedsChange {
		t.Error("expected no change on second Check")
	}
}

func TestFileTouchIdempotentWhenExists(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "existing", "content\n")

	s, err := NewFileTouchBuilder(testFileTouchMctx())(path, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when file already exists")
	}
}

func TestFileTouchMakeDirs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a", "b", "stamp")

	s, err := NewFileTouchBuilder(testFileTouchMctx())(path, map[string]any{"makedirs": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file created: %v", err)
	}
}

func TestFileTouchNoMakeDirsFails(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "missing", "stamp")

	s, err := NewFileTouchBuilder(testFileTouchMctx())(path, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err == nil {
		t.Error("expected error when parent dir is missing and makedirs is unset")
	}
}

func TestFileTouchExistingRunsTouchCommand(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "existing", "content\n")

	fakeCmd := exectest.NewFakeCommandExec()
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: &exec.OSFileExec{}, Command: fakeCmd}}

	s, err := NewFileTouchBuilder(mctx)(path, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed for mtime update")
	}
	if ar.Details["action"] != "mtime" {
		t.Errorf("Details action: got %v", ar.Details)
	}
	calls := fakeCmd.Calls()
	if len(calls) != 1 || calls[0].Command != "touch" {
		t.Fatalf("expected one touch call, got %v", calls)
	}
	if len(calls[0].Args) != 1 || calls[0].Args[0] != path {
		t.Errorf("touch args: got %v", calls[0].Args)
	}
	// Existing file content must survive the touch.
	got, _ := os.ReadFile(path)
	if string(got) != "content\n" {
		t.Errorf("content: got %q", string(got))
	}
}

func TestFileTouchExistingWithoutCommandProvider(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "existing", "content\n")

	s, err := NewFileTouchBuilder(testFileTouchMctx())(path, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected no change when file exists and no command provider is set")
	}
}

func TestFileTouchRevert(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "stamp")

	s, err := NewFileTouchBuilder(testFileTouchMctx())(path, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed on revert of created file")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("expected file removed on revert")
	}
}

func TestFileTouchRevertExistingIsNoop(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "existing", "content\n")

	fakeCmd := exectest.NewFakeCommandExec()
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: &exec.OSFileExec{}, Command: fakeCmd}}
	s, err := NewFileTouchBuilder(mctx)(path, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected no change on revert of pre-existing file")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("pre-existing file must not be removed: %v", err)
	}
}
