package modules

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
)

func testFileReplaceMctx() *exec.ModuleContext {
	return &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: &exec.OSFileExec{}}}
}

func TestFileReplaceName(t *testing.T) {
	s, err := NewFileReplaceBuilder(testFileReplaceMctx())("/etc/x", map[string]any{
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
	_, err := NewFileReplaceBuilder(testFileReplaceMctx())("x", map[string]any{
		"pattern": "(unclosed", "repl": "b",
	})
	if err == nil {
		t.Fatal("expected error for invalid pattern")
	}
}

func TestFileReplaceMissingPattern(t *testing.T) {
	_, err := NewFileReplaceBuilder(testFileReplaceMctx())("x", map[string]any{"repl": "b"})
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
			s, err := NewFileReplaceBuilder(testFileReplaceMctx())(path, tc.config)
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
	s, err := NewFileReplaceBuilder(testFileReplaceMctx())(path, map[string]any{
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
	s, err := NewFileReplaceBuilder(testFileReplaceMctx())(path, map[string]any{
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

var _ state.State = (*FileReplace)(nil)
