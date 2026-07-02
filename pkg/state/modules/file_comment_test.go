package modules

import (
	"context"
	"os"
	"testing"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
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

var _ state.State = (*FileComment)(nil)
