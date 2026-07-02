package modules

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
)

func testFileBlockReplaceMctx() *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File: &exec.OSFileExec{},
		},
	}
}

func TestFileBlockReplaceName(t *testing.T) {
	mctx := testFileBlockReplaceMctx()
	builder := NewFileBlockReplaceBuilder(mctx)
	s, err := builder("/etc/conf", map[string]any{"content": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "file.blockreplace:/etc/conf" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestFileBlockReplacePrimaryParamDefault(t *testing.T) {
	mctx := testFileBlockReplaceMctx()
	builder := NewFileBlockReplaceBuilder(mctx)
	s, err := builder("/etc/conf", map[string]any{"content": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	fbr := s.(*FileBlockReplace)
	if fbr.Path != "/etc/conf" {
		t.Errorf("Path: got %q, want /etc/conf", fbr.Path)
	}
}

func TestFileBlockReplaceDefaultMarkers(t *testing.T) {
	mctx := testFileBlockReplaceMctx()
	builder := NewFileBlockReplaceBuilder(mctx)
	s, err := builder("/etc/conf", map[string]any{"content": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	fbr := s.(*FileBlockReplace)
	if fbr.MarkerStart != defaultMarkerStart {
		t.Errorf("MarkerStart: got %q, want %q", fbr.MarkerStart, defaultMarkerStart)
	}
	if fbr.MarkerEnd != defaultMarkerEnd {
		t.Errorf("MarkerEnd: got %q, want %q", fbr.MarkerEnd, defaultMarkerEnd)
	}
}

func TestFileBlockReplaceRequisites(t *testing.T) {
	mctx := testFileBlockReplaceMctx()
	builder := NewFileBlockReplaceBuilder(mctx)
	s, err := builder("test", map[string]any{
		"content":   "hello",
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
}

func TestFileBlockReplaceCheckNeedsChange(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "test.conf")

	content := "before\n# START managed zone\nold content\n# END managed zone\nafter\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileBlockReplaceMctx()
	builder := NewFileBlockReplaceBuilder(mctx)
	s, err := builder(filePath, map[string]any{"content": "new content"})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when block content differs")
	}
}

func TestFileBlockReplaceCheckNoChange(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "test.conf")

	content := "before\n# START managed zone\ndesired content\n# END managed zone\nafter\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileBlockReplaceMctx()
	builder := NewFileBlockReplaceBuilder(mctx)
	s, err := builder(filePath, map[string]any{"content": "desired content"})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change when block content already correct, diff: %s", cr.Diff)
	}
}

func TestFileBlockReplaceCheckMissingBlockNoAppend(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "test.conf")
	if err := os.WriteFile(filePath, []byte("no markers here\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileBlockReplaceMctx()
	builder := NewFileBlockReplaceBuilder(mctx)
	s, err := builder(filePath, map[string]any{"content": "hello", "append_if_not_found": false})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when markers missing and append_if_not_found is false")
	}
}

func TestFileBlockReplaceCheckMissingBlockWithAppend(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "test.conf")
	if err := os.WriteFile(filePath, []byte("no markers here\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileBlockReplaceMctx()
	builder := NewFileBlockReplaceBuilder(mctx)
	s, err := builder(filePath, map[string]any{"content": "hello", "append_if_not_found": true})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when markers missing and append_if_not_found is true")
	}
}

func TestFileBlockReplaceApplyReplace(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "test.conf")

	initial := "before\n# START managed zone\nold content\n# END managed zone\nafter\n"
	if err := os.WriteFile(filePath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileBlockReplaceMctx()
	builder := NewFileBlockReplaceBuilder(mctx)
	s, err := builder(filePath, map[string]any{"content": "new content"})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after replace")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	result := string(data)
	if !strings.Contains(result, "new content") {
		t.Error("expected new content in result")
	}
	if strings.Contains(result, "old content") {
		t.Error("expected old content to be replaced")
	}
	if !strings.Contains(result, "before") {
		t.Error("expected 'before' to be preserved")
	}
	if !strings.Contains(result, "after") {
		t.Error("expected 'after' to be preserved")
	}
}

func TestFileBlockReplaceApplyAppend(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "test.conf")
	if err := os.WriteFile(filePath, []byte("existing content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileBlockReplaceMctx()
	builder := NewFileBlockReplaceBuilder(mctx)
	s, err := builder(filePath, map[string]any{"content": "managed content", "append_if_not_found": true})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after append")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	result := string(data)
	if !strings.Contains(result, "existing content") {
		t.Error("expected existing content to be preserved")
	}
	if !strings.Contains(result, defaultMarkerStart) {
		t.Error("expected start marker in result")
	}
	if !strings.Contains(result, "managed content") {
		t.Error("expected managed content in result")
	}
	if !strings.Contains(result, defaultMarkerEnd) {
		t.Error("expected end marker in result")
	}
}

func TestFileBlockReplaceApplyCustomMarkers(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "test.conf")

	initial := "# BEGIN block\nold\n# END block\n"
	if err := os.WriteFile(filePath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileBlockReplaceMctx()
	builder := NewFileBlockReplaceBuilder(mctx)
	s, err := builder(filePath, map[string]any{
		"content":      "new",
		"marker_start": "# BEGIN block",
		"marker_end":   "# END block",
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed with custom markers")
	}

	data, _ := os.ReadFile(filePath)
	if !strings.Contains(string(data), "new") {
		t.Error("expected 'new' in result")
	}
}

func TestFileBlockReplaceRevert(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "test.conf")

	initial := "before\n# START managed zone\nold content\n# END managed zone\nafter\n"
	if err := os.WriteFile(filePath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileBlockReplaceMctx()
	builder := NewFileBlockReplaceBuilder(mctx)
	s, err := builder(filePath, map[string]any{"content": "new content"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after revert")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != initial {
		t.Errorf("reverted content does not match original:\ngot:  %q\nwant: %q", data, initial)
	}
}

var _ state.State = (*FileBlockReplace)(nil)
