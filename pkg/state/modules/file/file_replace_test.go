package filemod

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

func testFileReplaceMctx() *exec.ModuleContext {
	return &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: &exec.OSFileExec{}}}
}

func TestFileReplaceName(t *testing.T) {
	s, err := NewFileReplaceBuilder(testFileReplaceMctx(), modschema.DecodeOptions{})("/etc/x", map[string]any{
		"pattern": "a", "repl": "b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "file.replace:/etc/x" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestFileReplaceInvalidPattern(t *testing.T) {
	_, err := NewFileReplaceBuilder(testFileReplaceMctx(), modschema.DecodeOptions{})("x", map[string]any{
		"pattern": "(unclosed", "repl": "b",
	})
	if err == nil {
		t.Fatal("expected error for invalid pattern")
	}
}

func TestFileReplaceMissingPattern(t *testing.T) {
	_, err := NewFileReplaceBuilder(testFileReplaceMctx(), modschema.DecodeOptions{})("x", map[string]any{"repl": "b"})
	if err == nil {
		t.Fatal("expected error for missing pattern")
	}
}

func TestFileReplaceOps(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		initial string
		config  map[string]any
		want    string
		// secondNeedsChange marks inputs that are inherently non-idempotent:
		// the pattern still matches after replacement and produces different
		// output on a re-run. Salt's file.replace has the same caveat.
		secondNeedsChange bool
	}{
		{
			name:    "simple replace",
			initial: "level=info\n",
			config:  map[string]any{"pattern": `level=\w+`, "repl": "level=debug"},
			want:    "level=debug\n",
		},
		{
			name:    "backreference expansion",
			initial: "listen_port=8080\n",
			config:  map[string]any{"pattern": `listen_(\w+)=8080`, "repl": "listen_$1=9090"},
			want:    "listen_port=9090\n",
		},
		{
			name:    "count limits replacements",
			initial: "x x x\n",
			config:  map[string]any{"pattern": "x", "repl": "y", "count": 2},
			want:    "y y x\n",
			// The third "x" still matches after Apply, so a re-run
			// legitimately reports pending changes.
			secondNeedsChange: true,
		},
		{
			name:    "append if not found",
			initial: "alpha\n",
			config:  map[string]any{"pattern": "^beta$", "repl": "beta", "append_if_not_found": true},
			want:    "alpha\nbeta\n",
		},
		{
			name:    "prepend if not found",
			initial: "alpha\n",
			config:  map[string]any{"pattern": "^beta$", "repl": "beta", "prepend_if_not_found": true},
			want:    "beta\nalpha\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempFile(t, "f.txt", tc.initial)
			s, err := NewFileReplaceBuilder(testFileReplaceMctx(), modschema.DecodeOptions{})(path, tc.config)
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
			if string(got) != tc.want {
				t.Errorf("content: got %q want %q", string(got), tc.want)
			}

			cr2, err := s.Check(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if cr2.NeedsChange != tc.secondNeedsChange {
				t.Errorf("second Check NeedsChange: got %v want %v", cr2.NeedsChange, tc.secondNeedsChange)
			}
		})
	}
}

func TestFileReplaceCreatesMissingWithAppend(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "new.conf")
	s, err := NewFileReplaceBuilder(testFileReplaceMctx(), modschema.DecodeOptions{})(path, map[string]any{
		"pattern": "^enabled$", "repl": "enabled", "append_if_not_found": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "enabled\n" {
		t.Errorf("content: got %q", string(got))
	}
}

func TestFileReplaceRevert(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "r.txt", "level=info\n")
	s, err := NewFileReplaceBuilder(testFileReplaceMctx(), modschema.DecodeOptions{})(path, map[string]any{
		"pattern": `level=\w+`, "repl": "level=debug",
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
	if string(got) != "level=info\n" {
		t.Errorf("revert content: got %q", string(got))
	}
}

func TestFileReplaceRevertCreatedToleratesMissing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "new.conf")
	s, err := NewFileReplaceBuilder(testFileReplaceMctx(), modschema.DecodeOptions{})(path, map[string]any{
		"pattern": `level=\w+`, "repl": "level=debug", "append_if_not_found": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Apply on a missing file creates it via append_if_not_found.
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected Apply to create the file: %v", err)
	}

	// The created file vanished externally; revert must tolerate it —
	// file-absent already IS the reverted state (same semantics as file.copy).
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revert(ctx); err != nil {
		t.Fatalf("Revert must tolerate an already-missing created file: %v", err)
	}
}

func TestFileReplaceFreshInstanceRevertIsNoOp(t *testing.T) {
	ctx := context.Background()
	original := "level=info\nother=1\n"
	path := writeTempFile(t, "app.conf", original)
	s, err := NewFileReplaceBuilder(testFileReplaceMctx(), modschema.DecodeOptions{})(path, map[string]any{
		"pattern": `level=\w+`, "repl": "level=debug",
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

func TestFileReplaceRevertRemovesCreatedFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "new.conf")
	s, err := NewFileReplaceBuilder(testFileReplaceMctx(), modschema.DecodeOptions{})(path, map[string]any{
		"pattern": "^level=.*$", "repl": "level=debug", "append_if_not_found": true,
	})
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
		t.Error("expected Changed reverting a file created by this instance's Apply")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected created file removed on same-instance revert")
	}
}

func TestFileReplaceReadErrorFailsCheckAndApply(t *testing.T) {
	ctx := context.Background()
	original := "important data\nlevel=info\n"
	fake := exectest.NewFakeFileExec()
	fake.PreCreate("/etc/app.conf", []byte(original), 0644)
	fake.SetReadError("/etc/app.conf", errors.New("input/output error"))
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: fake}}
	s, err := NewFileReplaceBuilder(mctx, modschema.DecodeOptions{})("/etc/app.conf", map[string]any{
		"pattern": `level=\w+`, "repl": "level=debug", "append_if_not_found": true,
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
	// A transient read error must never truncate the file to just the
	// not-found content.
	if got, _ := fake.GetFile("/etc/app.conf"); string(got) != original {
		t.Errorf("file must not be modified on a read error: got %q", string(got))
	}
}

var _ state.State = (*FileReplace)(nil)

// TestFileReplaceContract replays the permanent differential contract fixtures
// against the migrated fileReplaceSpec decoder — including the flagged BD-1 (a
// msgpack-delivered sized-int count is honored), BD-2 (a CLI numeric-string
// count and bool-string append_if_not_found are honored), BD-6 (a wrong-typed
// name is coerced/rejected), and BD-7 (integer bool coercion) divergences.
func TestFileReplaceContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var f FileReplace
		if _, err := fileReplaceSpec.Decode(id, config, &f, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &f, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/file.replace.yaml")
}
