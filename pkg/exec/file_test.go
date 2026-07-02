package exec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOSFileExecWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	ctx := context.Background()

	e := &OSFileExec{}

	if err := e.WriteFile(ctx, path, []byte("hello"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	data, err := e.ReadFile(ctx, path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("content: got %q, want %q", string(data), "hello")
	}
}

func TestOSFileExecStat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stat.txt")
	ctx := context.Background()

	e := &OSFileExec{}
	if err := e.WriteFile(ctx, path, []byte("x"), 0640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	info, err := e.Stat(ctx, path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0640 {
		t.Errorf("mode: got %o, want 0640", info.Mode().Perm())
	}
}

func TestOSFileExecStatNotFound(t *testing.T) {
	e := &OSFileExec{}
	_, err := e.Stat(context.Background(), "/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestOSFileExecRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "remove.txt")
	ctx := context.Background()

	e := &OSFileExec{}
	if err := e.WriteFile(ctx, path, []byte("bye"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := e.Remove(ctx, path); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should be removed")
	}
}

func TestOSFileExecMkdirAll(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c")
	ctx := context.Background()

	e := &OSFileExec{}
	if err := e.MkdirAll(ctx, nested, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory")
	}
}

func TestOSFileExecChmod(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chmod.txt")
	ctx := context.Background()

	e := &OSFileExec{}
	if err := e.WriteFile(ctx, path, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := e.Chmod(ctx, path, 0755); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("mode: got %o, want 0755", info.Mode().Perm())
	}
}

func TestOSFileExecReadNotFound(t *testing.T) {
	e := &OSFileExec{}
	_, err := e.ReadFile(context.Background(), "/nonexistent/file")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}
