package modules

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/state"
)

func testFileCommentMctx() *exec.ModuleContext {
	return &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: &exec.OSFileExec{}}}
}

func TestFileCommentNames(t *testing.T) {
	c, err := NewFileCommentBuilder(testFileCommentMctx())("/etc/a", map[string]any{"regex": "^x"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Name() != "file.comment:/etc/a" {
		t.Errorf("comment Name: got %q", c.Name())
	}
	u, err := NewFileUncommentBuilder(testFileCommentMctx())("/etc/a", map[string]any{"regex": "^x"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Name() != "file.uncomment:/etc/a" {
		t.Errorf("uncomment Name: got %q", u.Name())
	}
}

func TestFileCommentMissingRegex(t *testing.T) {
	_, err := NewFileCommentBuilder(testFileCommentMctx())("x", map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing regex")
	}
}

func TestFileComment(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "sshd_config", "PermitRootLogin yes\nPort 22\n")
	s, err := NewFileCommentBuilder(testFileCommentMctx())(path, map[string]any{
		"regex": "^PermitRootLogin",
	})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange")
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "#PermitRootLogin yes\nPort 22\n" {
		t.Errorf("content: got %q", string(got))
	}

	// Idempotent: already commented.
	cr2, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr2.NeedsChange {
		t.Error("expected no change when already commented")
	}
}

func TestFileUncomment(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "conf", "# net.ipv4.ip_forward=1\nother\n")
	s, err := NewFileUncommentBuilder(testFileCommentMctx())(path, map[string]any{
		"regex": "net.ipv4.ip_forward",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != " net.ipv4.ip_forward=1\nother\n" {
		t.Errorf("content: got %q", string(got))
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change after uncomment")
	}
}

func TestFileCommentCustomChar(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "ini", "debug = true\n")
	s, err := NewFileCommentBuilder(testFileCommentMctx())(path, map[string]any{
		"regex": "^debug", "char": ";",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != ";debug = true\n" {
		t.Errorf("content: got %q", string(got))
	}
}

func TestFileCommentRevert(t *testing.T) {
	ctx := context.Background()
	original := "PermitRootLogin yes\n"
	path := writeTempFile(t, "sshd", original)
	s, err := NewFileCommentBuilder(testFileCommentMctx())(path, map[string]any{
		"regex": "^PermitRootLogin",
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
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Errorf("revert content: got %q", string(got))
	}
}

func TestFileCommentFreshInstanceRevertIsNoOp(t *testing.T) {
	ctx := context.Background()
	original := "PermitRootLogin yes\n"
	path := writeTempFile(t, "sshd_config", original)
	s, err := NewFileCommentBuilder(testFileCommentMctx())(path, map[string]any{
		"regex": "^PermitRootLogin",
	})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(ctx)
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if ar.Changed {
		t.Error("fresh-instance revert must not report a change")
	}
	if ar.Diff != fsxNothingToRevert {
		t.Errorf("Diff: got %q, want %q", ar.Diff, fsxNothingToRevert)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file must survive a fresh-instance revert: %v", err)
	}
	if string(got) != original {
		t.Errorf("content after revert: got %q, want %q", string(got), original)
	}
}

func TestFileCommentMissingFileIsCleanNoChange(t *testing.T) {
	ctx := context.Background()
	fake := exectest.NewFakeFileExec()
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: fake}}
	s, err := NewFileCommentBuilder(mctx)("/etc/nope.conf", map[string]any{"regex": "^x"})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatalf("Check on a missing file must not error: %v", err)
	}
	if cr.NeedsChange {
		t.Error("Check on a missing file: want no change")
	}
	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply on a missing file must not error: %v", err)
	}
	if ar.Changed {
		t.Error("Apply on a missing file: want no change")
	}
}

func TestFileCommentReadErrorFailsCheckAndApply(t *testing.T) {
	ctx := context.Background()
	original := "PermitRootLogin yes\n"
	fake := exectest.NewFakeFileExec()
	fake.PreCreate("/etc/sshd_config", []byte(original), 0644)
	fake.SetReadError("/etc/sshd_config", errors.New("input/output error"))
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: fake}}
	s, err := NewFileCommentBuilder(mctx)("/etc/sshd_config", map[string]any{
		"regex": "^PermitRootLogin",
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
	if got, _ := fake.GetFile("/etc/sshd_config"); string(got) != original {
		t.Errorf("file must not be modified on a read error: got %q", string(got))
	}
}

var _ state.State = (*FileComment)(nil)
