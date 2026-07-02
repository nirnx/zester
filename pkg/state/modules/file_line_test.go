package modules

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
)

func testFileLineMctx() *exec.ModuleContext {
	return &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: &exec.OSFileExec{}}}
}

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFileLineName(t *testing.T) {
	s, err := NewFileLineBuilder(testFileLineMctx())("/etc/motd", map[string]any{"content": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "file.line:/etc/motd" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestFileLineRequisites(t *testing.T) {
	s, err := NewFileLineBuilder(testFileLineMctx())("test", map[string]any{
		"content":   "x",
		"require":   []any{"pkg.installed:nginx"},
		"watch":     []any{"file.managed:/etc/a"},
		"onchanges": []any{"cmd.run:b"},
		"onfail":    []any{"cmd.run:c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := s.Reqs()
	if len(r.Require) != 1 || len(r.Watch) != 1 || len(r.OnChanges) != 1 || len(r.OnFail) != 1 {
		t.Errorf("requisites not parsed: %+v", r)
	}
}

func TestFileLineMissingProvider(t *testing.T) {
	_, err := NewFileLineBuilder(&exec.ModuleContext{})("x", map[string]any{"content": "a"})
	if err == nil {
		t.Fatal("expected error when File provider is nil")
	}
}

func TestFileLineOps(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		initial string
		config  map[string]any
		want    string
	}{
		{
			name:    "ensure appends missing line",
			initial: "a\nb\n",
			config:  map[string]any{"content": "c"},
			want:    "a\nb\nc\n",
		},
		{
			name:    "ensure replaces via match",
			initial: "port=80\nhost=x\n",
			config:  map[string]any{"content": "port=443", "match": "^port="},
			want:    "port=443\nhost=x\n",
		},
		{
			name:    "insert after",
			initial: "a\nb\nc\n",
			config:  map[string]any{"content": "new", "mode": "insert", "after": "^b$"},
			want:    "a\nb\nnew\nc\n",
		},
		{
			name:    "insert before",
			initial: "a\nb\nc\n",
			config:  map[string]any{"content": "new", "mode": "insert", "before": "^c$"},
			want:    "a\nb\nnew\nc\n",
		},
		{
			name:    "delete matching line",
			initial: "a\nremove-me\nc\n",
			config:  map[string]any{"content": "", "mode": "delete", "match": "remove-me"},
			want:    "a\nc\n",
		},
		{
			name:    "replace only when match present",
			initial: "key old\n",
			config:  map[string]any{"content": "key new", "mode": "replace", "match": "^key "},
			want:    "key new\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempFile(t, "f.txt", tc.initial)
			s, err := NewFileLineBuilder(testFileLineMctx())(path, tc.config)
			if err != nil {
				t.Fatal(err)
			}

			cr, err := s.Check(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !cr.NeedsChange {
				t.Fatal("expected NeedsChange before apply")
			}

			if _, err := s.Apply(ctx); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != tc.want {
				t.Errorf("content: got %q want %q", string(got), tc.want)
			}

			// Idempotency.
			cr2, err := s.Check(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if cr2.NeedsChange {
				t.Errorf("expected no change on second Check")
			}
		})
	}
}

func TestFileLineCreatesMissingFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "new.conf")
	s, err := NewFileLineBuilder(testFileLineMctx())(path, map[string]any{"content": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange for missing file")
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello\n" {
		t.Errorf("content: got %q", string(got))
	}
}

func TestFileLineDeleteMissingFileNoop(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "absent.conf")
	s, err := NewFileLineBuilder(testFileLineMctx())(path, map[string]any{
		"content": "x", "mode": "delete", "match": "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("delete on missing file should not need change")
	}
}

func TestFileLineRevert(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "r.txt", "a\nb\n")
	s, err := NewFileLineBuilder(testFileLineMctx())(path, map[string]any{"content": "c"})
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
	if string(got) != "a\nb\n" {
		t.Errorf("revert content: got %q", string(got))
	}
}

var _ state.State = (*FileLine)(nil)
