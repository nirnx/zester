package modules

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/state"
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

func TestFileBlockReplaceFreshInstanceRevertIsNoOp(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "sshd_config")
	original := "Port 22\n# START managed zone\nold\n# END managed zone\nUseDNS no\n"
	if err := os.WriteFile(filePath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	s, err := NewFileBlockReplaceBuilder(testFileBlockReplaceMctx())(filePath, map[string]any{
		"content": "new",
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
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("file must survive a fresh-instance revert: %v", err)
	}
	if string(got) != original {
		t.Errorf("content after revert: got %q, want %q", string(got), original)
	}
}

func TestFileBlockReplaceRevertRemovesCreatedFile(t *testing.T) {
	ctx := context.Background()
	filePath := filepath.Join(t.TempDir(), "new.conf")

	s, err := NewFileBlockReplaceBuilder(testFileBlockReplaceMctx())(filePath, map[string]any{
		"content": "hello", "append_if_not_found": true,
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
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Error("expected created file removed on same-instance revert")
	}
}

func TestFileBlockReplaceApplyRecomputesExistencePerInvocation(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "cfg.conf")
	initial := "top\n# START managed zone\nold\n# END managed zone\nbottom\n"
	if err := os.WriteFile(filePath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	s, err := NewFileBlockReplaceBuilder(testFileBlockReplaceMctx())(filePath, map[string]any{
		"content": "new", "append_if_not_found": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ar.Details["action"] != "replaced" {
		t.Fatalf("first apply action: got %q, want %q", ar.Details["action"], "replaced")
	}

	// The file vanishes between invocations (e.g. between retry attempts).
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}

	ar2, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ar2.Details["action"] != "created" {
		t.Errorf("apply on a now-missing file: action got %q, want %q (existence must be re-derived per invocation, not read from a stale instance flag)",
			ar2.Details["action"], "created")
	}

	// The first invocation's genuine backup still wins on revert.
	rr, err := s.Revert(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !rr.Changed {
		t.Error("expected Changed on revert")
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != initial {
		t.Errorf("revert content: got %q, want original %q", string(got), initial)
	}
}

func TestFileBlockReplaceReadErrorFailsCheckAndApply(t *testing.T) {
	ctx := context.Background()
	original := "# START managed zone\nold\n# END managed zone\n"
	fake := exectest.NewFakeFileExec()
	fake.PreCreate("/etc/cfg.conf", []byte(original), 0644)
	fake.SetReadError("/etc/cfg.conf", errors.New("input/output error"))
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: fake}}
	s, err := NewFileBlockReplaceBuilder(mctx)("/etc/cfg.conf", map[string]any{
		"content": "new", "append_if_not_found": true,
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
	if got, _ := fake.GetFile("/etc/cfg.conf"); string(got) != original {
		t.Errorf("file must not be modified on a read error: got %q", string(got))
	}
}

var _ state.State = (*FileBlockReplace)(nil)
