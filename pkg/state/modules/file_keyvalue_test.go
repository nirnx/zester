package modules

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

func testFileKVMctx() *exec.ModuleContext {
	return &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: &exec.OSFileExec{}}}
}

func TestFileKeyValueName(t *testing.T) {
	s, err := NewFileKeyValueBuilder(testFileKVMctx())("/etc/os-release", map[string]any{
		"key": "NAME", "value": "Zester",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "file.keyvalue:/etc/os-release" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestFileKeyValueNoEntries(t *testing.T) {
	_, err := NewFileKeyValueBuilder(testFileKVMctx())("x", map[string]any{})
	if err == nil {
		t.Fatal("expected error when no entries provided")
	}
}

func TestFileKeyValueUpdateExisting(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "sysctl.conf", "net.ipv4.ip_forward=0\nother=1\n")
	s, err := NewFileKeyValueBuilder(testFileKVMctx())(path, map[string]any{
		"key": "net.ipv4.ip_forward", "value": 1,
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
	if string(got) != "net.ipv4.ip_forward=1\nother=1\n" {
		t.Errorf("content: got %q", string(got))
	}

	cr2, _ := s.Check(ctx)
	if cr2.NeedsChange {
		t.Error("expected idempotent second check")
	}
}

func TestFileKeyValueAppendNew(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "env", "EXISTING=1\n")
	s, err := NewFileKeyValueBuilder(testFileKVMctx())(path, map[string]any{
		"entries": map[string]any{"NEWKEY": "val"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "EXISTING=1\nNEWKEY=val\n" {
		t.Errorf("content: got %q", string(got))
	}
}

func TestFileKeyValueCustomSeparatorTolerant(t *testing.T) {
	ctx := context.Background()
	// Existing line uses spaced separator; matcher tolerates surrounding spaces.
	path := writeTempFile(t, "sysctl", "kernel.pid_max = 4096\n")
	s, err := NewFileKeyValueBuilder(testFileKVMctx())(path, map[string]any{
		"key": "kernel.pid_max", "value": "4096", "separator": " = ",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change for already-correct spaced kv, diff=%q", cr.Diff)
	}
}

func TestFileKeyValueCreatesMissingFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "new.conf")
	s, err := NewFileKeyValueBuilder(testFileKVMctx())(path, map[string]any{
		"entries": map[string]any{"A": "1", "B": "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "A=1\nB=2\n" {
		t.Errorf("content: got %q", string(got))
	}
}

func TestFileKeyValueRevert(t *testing.T) {
	ctx := context.Background()
	original := "K=old\n"
	path := writeTempFile(t, "kv", original)
	s, err := NewFileKeyValueBuilder(testFileKVMctx())(path, map[string]any{
		"key": "K", "value": "new",
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

var _ state.State = (*FileKeyValue)(nil)
