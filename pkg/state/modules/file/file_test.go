package filemod

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/template"
)

func testFileMctx() *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File:    &exec.OSFileExec{},
			Command: exectest.NewFakeCommandExec(),
		},
	}
}

// newFileManagedTest builds a FileManaged state through the migrated builder
// with a caller-supplied file provider and render function — the seam the
// pre-migration inner constructor (newFileManaged) offered. It threads an
// empty decode policy; the builder decodes through fileManagedSpec.
func newFileManagedTest(id string, config map[string]any, file exec.FileExec,
	render func(string, string, map[string]any) (string, error)) (state.State, error) {
	mctx := &exec.ModuleContext{
		ProviderSet:    exec.ProviderSet{File: file},
		RenderTemplate: render,
	}
	return NewFileManagedBuilder(mctx, modschema.DecodeOptions{})(id, config)
}

func TestFileManagedCreateNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	mctx := testFileMctx()
	builder := NewFileManagedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("test", map[string]any{
		"path":    path,
		"content": "hello world",
		"mode":    "0644",
	})
	if err != nil {
		t.Fatalf("NewFileManagedBuilder: %v", err)
	}

	ctx := context.Background()

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for nonexistent file")
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ar.Changed {
		t.Error("expected Changed after apply")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("content: got %q, want %q", string(data), "hello world")
	}

	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0644 {
		t.Errorf("mode: got %o, want 0644", info.Mode().Perm())
	}

	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatalf("Check after apply: %v", err)
	}
	if cr.NeedsChange {
		t.Error("expected no change needed after apply")
	}
}

func TestFileManagedUpdateContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")

	if err := os.WriteFile(path, []byte("old content"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileMctx()
	builder := NewFileManagedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("test", map[string]any{
		"path":    path,
		"content": "new content",
		"mode":    "0644",
	})
	if err != nil {
		t.Fatalf("NewFileManagedBuilder: %v", err)
	}

	ctx := context.Background()

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for different content")
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ar.Changed {
		t.Error("expected Changed")
	}

	data, _ := os.ReadFile(path)
	if string(data) != "new content" {
		t.Errorf("content: got %q, want %q", string(data), "new content")
	}
}

func TestFileManagedRevertToOriginal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "revert.txt")

	if err := os.WriteFile(path, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileMctx()
	builder := NewFileManagedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("test", map[string]any{
		"path":    path,
		"content": "modified",
		"mode":    "0644",
	})
	if err != nil {
		t.Fatalf("NewFileManagedBuilder: %v", err)
	}

	ctx := context.Background()

	_, err = s.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	rr, err := s.Revert(ctx)
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if !rr.Changed {
		t.Error("expected Changed after revert")
	}

	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Errorf("content after revert: got %q, want %q", string(data), "original")
	}
}

func TestFileManagedRevertNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	mctx := testFileMctx()
	builder := NewFileManagedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("test", map[string]any{
		"path":    path,
		"content": "new file",
		"mode":    "0644",
	})
	if err != nil {
		t.Fatalf("NewFileManagedBuilder: %v", err)
	}

	ctx := context.Background()

	_, err = s.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	_, err = s.Revert(ctx)
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should be removed after revert")
	}
}

func TestFileManagedSource(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.txt")
	destPath := filepath.Join(dir, "dest.txt")

	if err := os.WriteFile(sourcePath, []byte("from source"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileMctx()
	builder := NewFileManagedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("test", map[string]any{
		"path":   destPath,
		"source": sourcePath,
		"mode":   "0600",
	})
	if err != nil {
		t.Fatalf("NewFileManagedBuilder: %v", err)
	}

	ctx := context.Background()
	_, err = s.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	data, _ := os.ReadFile(destPath)
	if string(data) != "from source" {
		t.Errorf("content: got %q, want %q", string(data), "from source")
	}

	info, _ := os.Stat(destPath)
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode: got %o, want 0600", info.Mode().Perm())
	}
}

func TestFileManagedModeChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mode.txt")

	if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileMctx()
	builder := NewFileManagedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("test", map[string]any{
		"path":    path,
		"content": "content",
		"mode":    "0755",
	})
	if err != nil {
		t.Fatalf("NewFileManagedBuilder: %v", err)
	}

	ctx := context.Background()
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for mode difference")
	}
}

func TestFileManagedName(t *testing.T) {
	mctx := testFileMctx()
	builder := NewFileManagedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("my-file", map[string]any{
		"path":    "/tmp/test",
		"content": "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "file.managed:my-file" {
		t.Errorf("Name: got %q, want %q", s.Name(), "file.managed:my-file")
	}
}

func TestFileManagedRequires(t *testing.T) {
	mctx := testFileMctx()
	builder := NewFileManagedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("test", map[string]any{
		"path":    "/tmp/test",
		"content": "x",
		"require": []any{"pkg.installed:nginx"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:nginx" {
		t.Errorf("Reqs().Require: got %v, want [pkg.installed:nginx]", reqs.Require)
	}
}

func TestFileManagedRequisites(t *testing.T) {
	mctx := testFileMctx()
	builder := NewFileManagedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("test", map[string]any{
		"path":      "/tmp/test",
		"content":   "x",
		"require":   []any{"pkg.installed:nginx"},
		"watch":     []any{"file.managed:/etc/ssl/cert.pem"},
		"onchanges": []any{"cmd.run:build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:nginx" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/ssl/cert.pem" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:build" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
}

// testRenderFunc creates a template render closure backed by a real template.Engine.
func testRenderFunc(t *testing.T) func(string, string, map[string]any) (string, error) {
	t.Helper()
	eng, err := template.NewEngine(template.EngineConfig{
		BasePath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return func(name, source string, extra map[string]any) (string, error) {
		return eng.RenderString(name, source, template.RenderContext{
			Extra: extra,
		})
	}
}

func TestFileManagedSourceTemplate(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.conf")
	destPath := filepath.Join(dir, "dest.conf")

	if err := os.WriteFile(sourcePath, []byte("worker_processes {{ worker_count }};"), 0644); err != nil {
		t.Fatal(err)
	}

	render := testRenderFunc(t)
	s, err := newFileManagedTest("test-tpl", map[string]any{
		"path":     destPath,
		"source":   sourcePath,
		"template": true,
		"context":  map[string]any{"worker_count": "4"},
	}, &exec.OSFileExec{}, render)
	if err != nil {
		t.Fatalf("newFileManaged: %v", err)
	}

	ctx := context.Background()
	_, err = s.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "4") {
		t.Errorf("expected rendered output to contain '4', got %q", got)
	}
	if strings.Contains(got, "{{ worker_count }}") {
		t.Errorf("expected template variable to be rendered, got raw %q", got)
	}
	if got != "worker_processes 4;" {
		t.Errorf("content: got %q, want %q", got, "worker_processes 4;")
	}
}

func TestFileManagedSourceTemplateContext(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.conf")
	destPath := filepath.Join(dir, "dest.conf")

	if err := os.WriteFile(sourcePath, []byte("host={{ hostname }} port={{ port }}"), 0644); err != nil {
		t.Fatal(err)
	}

	render := testRenderFunc(t)
	s, err := newFileManagedTest("ctx-test", map[string]any{
		"path":     destPath,
		"source":   sourcePath,
		"template": "jinja",
		"context": map[string]any{
			"hostname": "web01",
			"port":     "8080",
		},
	}, &exec.OSFileExec{}, render)
	if err != nil {
		t.Fatalf("newFileManaged: %v", err)
	}

	ctx := context.Background()
	_, err = s.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(data)
	if got != "host=web01 port=8080" {
		t.Errorf("content: got %q, want %q", got, "host=web01 port=8080")
	}
}

func TestFileManagedSourceTemplateDefaults(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.conf")
	destPath := filepath.Join(dir, "dest.conf")

	if err := os.WriteFile(sourcePath, []byte("timeout={{ timeout }}"), 0644); err != nil {
		t.Fatal(err)
	}

	render := testRenderFunc(t)
	s, err := newFileManagedTest("defaults-test", map[string]any{
		"path":     destPath,
		"source":   sourcePath,
		"template": true,
		"defaults": map[string]any{"timeout": "30"},
	}, &exec.OSFileExec{}, render)
	if err != nil {
		t.Fatalf("newFileManaged: %v", err)
	}

	ctx := context.Background()
	_, err = s.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(data)
	if got != "timeout=30" {
		t.Errorf("content: got %q, want %q", got, "timeout=30")
	}
}

func TestFileManagedSourceTemplateContextOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.conf")
	destPath := filepath.Join(dir, "dest.conf")

	if err := os.WriteFile(sourcePath, []byte("port={{ port }} workers={{ workers }}"), 0644); err != nil {
		t.Fatal(err)
	}

	render := testRenderFunc(t)
	s, err := newFileManagedTest("override-test", map[string]any{
		"path":     destPath,
		"source":   sourcePath,
		"template": true,
		"defaults": map[string]any{
			"port":    "80",
			"workers": "2",
		},
		"context": map[string]any{
			"port": "443",
		},
	}, &exec.OSFileExec{}, render)
	if err != nil {
		t.Fatalf("newFileManaged: %v", err)
	}

	ctx := context.Background()
	_, err = s.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(data)
	// context overrides port from "80" to "443", workers uses default "2"
	if got != "port=443 workers=2" {
		t.Errorf("content: got %q, want %q", got, "port=443 workers=2")
	}
}

func TestFileManagedSourceNoTemplate(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.conf")
	destPath := filepath.Join(dir, "dest.conf")

	rawContent := "value={{ var }}"
	if err := os.WriteFile(sourcePath, []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	render := testRenderFunc(t)
	// template is not set (defaults to false)
	s, err := newFileManagedTest("no-tpl", map[string]any{
		"path":   destPath,
		"source": sourcePath,
	}, &exec.OSFileExec{}, render)
	if err != nil {
		t.Fatalf("newFileManaged: %v", err)
	}

	ctx := context.Background()
	_, err = s.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(data)
	if got != rawContent {
		t.Errorf("expected raw copy without template rendering, got %q, want %q", got, rawContent)
	}
}

func TestFileManagedContentTemplate(t *testing.T) {
	dir := t.TempDir()
	destPath := filepath.Join(dir, "dest.conf")

	render := testRenderFunc(t)
	s, err := newFileManagedTest("content-tpl", map[string]any{
		"path":     destPath,
		"content":  "Hello {{ name }}!",
		"template": true,
		"context":  map[string]any{"name": "World"},
	}, &exec.OSFileExec{}, render)
	if err != nil {
		t.Fatalf("newFileManaged: %v", err)
	}

	ctx := context.Background()
	_, err = s.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(data)
	if got != "Hello World!" {
		t.Errorf("content: got %q, want %q", got, "Hello World!")
	}
}

func TestFileManagedTemplateRenderError(t *testing.T) {
	dir := t.TempDir()
	destPath := filepath.Join(dir, "dest.conf")

	render := testRenderFunc(t)
	s, err := newFileManagedTest("bad-tpl", map[string]any{
		"path":     destPath,
		"content":  "{{ unclosed",
		"template": true,
	}, &exec.OSFileExec{}, render)
	if err != nil {
		t.Fatalf("newFileManaged: %v", err)
	}

	ctx := context.Background()
	_, err = s.Apply(ctx)
	if err == nil {
		t.Fatal("expected error from bad template syntax, got nil")
	}
	if !strings.Contains(err.Error(), "template") && !strings.Contains(err.Error(), "render") {
		t.Errorf("error should mention template/render, got: %v", err)
	}
}

func TestFileManagedTemplateNoRenderFunc(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.conf")
	destPath := filepath.Join(dir, "dest.conf")

	rawContent := "value={{ var }}"
	if err := os.WriteFile(sourcePath, []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	// template: true but render func is nil — graceful degradation to raw copy
	s, err := newFileManagedTest("nil-render", map[string]any{
		"path":     destPath,
		"source":   sourcePath,
		"template": true,
	}, &exec.OSFileExec{}, nil)
	if err != nil {
		t.Fatalf("newFileManaged: %v", err)
	}

	ctx := context.Background()
	_, err = s.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(data)
	if got != rawContent {
		t.Errorf("expected raw copy when render is nil, got %q, want %q", got, rawContent)
	}
}

// testCurrentUserGroup returns the current user/group names and numeric ids —
// names that user.Lookup/LookupGroup resolve on any host running the tests.
func testCurrentUserGroup(t *testing.T) (userName string, uid int, groupName string, gid int) {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Skipf("user.Current: %v", err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Skipf("user.LookupGroupId(%s): %v", u.Gid, err)
	}
	uid, err = strconv.Atoi(u.Uid)
	if err != nil {
		t.Skipf("non-numeric uid %q", u.Uid)
	}
	gid, err = strconv.Atoi(u.Gid)
	if err != nil {
		t.Skipf("non-numeric gid %q", u.Gid)
	}
	return u.Username, uid, g.Name, gid
}

func TestFileManagedCheckOwnershipDrift(t *testing.T) {
	userName, uid, groupName, gid := testCurrentUserGroup(t)

	fake := exectest.NewFakeFileExec()
	path := "/etc/app.conf"
	fake.PreCreate(path, []byte("content"), 0644)
	fake.SetOwner(path, uid+1, gid+1)

	s, err := newFileManagedTest("owned", map[string]any{
		"path":    path,
		"content": "content",
		"mode":    "0644",
		"user":    userName,
		"group":   groupName,
	}, fake, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange when ownership drifts and content+mode match")
	}
	if !strings.Contains(cr.Diff, "owner") && !strings.Contains(cr.Diff, "group") {
		t.Errorf("diff should mention ownership: %q", cr.Diff)
	}

	// Converged ownership: no change.
	fake.SetOwner(path, uid, gid)
	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatalf("Check converged: %v", err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change when ownership matches, diff: %s", cr.Diff)
	}
}

func TestFileManagedCheckOwnershipUndeclared(t *testing.T) {
	fake := exectest.NewFakeFileExec()
	path := "/etc/plain.conf"
	fake.PreCreate(path, []byte("content"), 0644)
	// Arbitrary ownership: without user:/group: declared the facet never fires.
	fake.SetOwner(path, 12345, 54321)

	s, err := newFileManagedTest("plain", map[string]any{
		"path":    path,
		"content": "content",
		"mode":    "0644",
	}, fake, nil)
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if cr.NeedsChange {
		t.Errorf("undeclared ownership must not cause churn, diff: %s", cr.Diff)
	}
}

func TestFileManagedCheckGroupOnlyDrift(t *testing.T) {
	_, uid, groupName, gid := testCurrentUserGroup(t)

	fake := exectest.NewFakeFileExec()
	path := "/etc/grouponly.conf"
	fake.PreCreate(path, []byte("content"), 0644)

	s, err := newFileManagedTest("grouponly", map[string]any{
		"path":    path,
		"content": "content",
		"mode":    "0644",
		"group":   groupName,
	}, fake, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// uid drift alone is invisible when only group: is declared.
	fake.SetOwner(path, uid+7, gid)
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if cr.NeedsChange {
		t.Errorf("uid drift must not fire with only group declared, diff: %s", cr.Diff)
	}

	// gid drift fires.
	fake.SetOwner(path, uid+7, gid+1)
	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatalf("Check gid drift: %v", err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange on gid drift with group declared")
	}
}

func TestFileManagedRevertFreshInstanceNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "precious.txt")
	if err := os.WriteFile(path, []byte("precious data"), 0644); err != nil {
		t.Fatal(err)
	}

	mctx := testFileMctx()
	s, err := NewFileManagedBuilder(mctx, modschema.DecodeOptions{})("fresh", map[string]any{
		"path":    path,
		"content": "other",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Revert on a fresh instance (no Apply recorded) must be a clean no-op.
	rr, err := s.Revert(context.Background())
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if rr.Changed {
		t.Error("fresh-instance revert must not report a change")
	}
	if !strings.Contains(rr.Diff, "nothing to revert") {
		t.Errorf("diff: got %q, want nothing-to-revert notice", rr.Diff)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file destroyed by fresh-instance revert: %v", err)
	}
	if string(data) != "precious data" {
		t.Errorf("content after no-op revert: got %q", string(data))
	}
}

func TestFileManagedCheckReadErrorFails(t *testing.T) {
	fake := exectest.NewFakeFileExec()
	path := "/etc/locked.conf"
	fake.PreCreate(path, []byte("current"), 0644)
	fake.SetReadError(path, errors.New("permission denied"))

	s, err := newFileManagedTest("locked", map[string]any{
		"path":    path,
		"content": "desired",
	}, fake, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Check(context.Background()); err == nil {
		t.Error("expected Check to fail on a non-not-exist read error, not report the file as absent")
	}
}

// TestFileManagedModeConvergencePreExisting pins the round-2 fix: os.WriteFile
// applies perm at creation only, so Apply must Chmod pre-existing files or the
// mode facet churns forever (Check → Apply → Check must converge).
func TestFileManagedModeConvergencePreExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.conf")
	if err := os.WriteFile(path, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}

	s, err := NewFileManagedBuilder(testFileMctx(), modschema.DecodeOptions{})("test", map[string]any{
		"path":    path,
		"content": "content",
		"mode":    "0644",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange for mode drift on pre-existing file")
	}

	if _, err := s.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Errorf("mode after apply: got %04o, want 0644 (mode not enforced)", info.Mode().Perm())
	}

	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatalf("Check after apply: %v", err)
	}
	if cr.NeedsChange {
		t.Errorf("mode facet did not converge (Check→Apply→Check), diff: %s", cr.Diff)
	}
}

// TestFileManagedModeConvergenceDefaultMode is the exact churn scenario from
// the finding: no explicit mode declared, the built-in 0644 default fires on a
// pre-existing 0600 file — Apply must actually enforce it.
func TestFileManagedModeConvergenceDefaultMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "default-mode.conf")
	if err := os.WriteFile(path, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}

	s, err := NewFileManagedBuilder(testFileMctx(), modschema.DecodeOptions{})("test", map[string]any{
		"path":    path,
		"content": "content",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange (default 0644 vs 0600)")
	}

	if _, err := s.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Errorf("mode after apply: got %04o, want 0644", info.Mode().Perm())
	}

	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatalf("Check after apply: %v", err)
	}
	if cr.NeedsChange {
		t.Errorf("default-mode facet did not converge, diff: %s", cr.Diff)
	}
}

// TestFileManagedRevertRestoresPriorMode pins that Revert restores the
// CAPTURED prior mode, not the desired mode Apply set.
func TestFileManagedRevertRestoresPriorMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "revert-mode.conf")
	if err := os.WriteFile(path, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}

	s, err := NewFileManagedBuilder(testFileMctx(), modschema.DecodeOptions{})("test", map[string]any{
		"path":    path,
		"content": "modified",
		"mode":    "0644",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := s.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0644 {
		t.Fatalf("mode after apply: got %04o, want 0644", info.Mode().Perm())
	}

	if _, err := s.Revert(ctx); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Errorf("content after revert: got %q, want %q", string(data), "original")
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode after revert: got %04o, want captured prior 0600", info.Mode().Perm())
	}
}

// TestFileManagedReApplyKeepsFirstBackup pins first-capture-wins: a re-Apply
// on the same instance (retry:, watch-forced runs) must not clobber the
// original backup with already-applied content.
func TestFileManagedReApplyKeepsFirstBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retry.conf")
	if err := os.WriteFile(path, []byte("A"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}

	s, err := NewFileManagedBuilder(testFileMctx(), modschema.DecodeOptions{})("test", map[string]any{
		"path":    path,
		"content": "B",
		"mode":    "0644",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := s.Apply(ctx); err != nil {
		t.Fatalf("Apply #1: %v", err)
	}
	// Re-apply on the same instance: the file now holds "B" — the backup
	// memo must keep the first capture ("A", 0640).
	if _, err := s.Apply(ctx); err != nil {
		t.Fatalf("Apply #2: %v", err)
	}

	if _, err := s.Revert(ctx); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "A" {
		t.Errorf("content after revert: got %q, want original %q (backup clobbered by re-apply)", string(data), "A")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Errorf("mode after revert: got %04o, want original 0640", info.Mode().Perm())
	}
}

// TestFileManagedCreatedPrecedenceOverBackup pins that wasCreated outranks a
// later backup capture: a file this instance CREATED must be REMOVED by
// Revert even after a re-Apply saw it existing.
func TestFileManagedCreatedPrecedenceOverBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "created.conf")

	s, err := NewFileManagedBuilder(testFileMctx(), modschema.DecodeOptions{})("test", map[string]any{
		"path":    path,
		"content": "B",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := s.Apply(ctx); err != nil {
		t.Fatalf("Apply #1: %v", err)
	}
	// Re-apply: the file now exists, but it never PRE-existed — Revert must
	// still remove it, not rewrite the already-applied content back.
	if _, err := s.Apply(ctx); err != nil {
		t.Fatalf("Apply #2: %v", err)
	}

	rr, err := s.Revert(ctx)
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if !rr.Changed {
		t.Error("expected Changed on revert of created file")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("created file must be removed by revert, not rewritten")
	}
}

func TestFileManagedApplyReadErrorFails(t *testing.T) {
	fake := exectest.NewFakeFileExec()
	path := "/etc/locked.conf"
	fake.PreCreate(path, []byte("current"), 0644)
	fake.SetReadError(path, errors.New("permission denied"))

	s, err := newFileManagedTest("locked", map[string]any{
		"path":    path,
		"content": "desired",
	}, fake, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Apply(context.Background()); err == nil {
		t.Fatal("expected Apply to fail when the prior content cannot be captured")
	}

	// The file must not have been overwritten.
	data, ok := fake.GetFile(path)
	if !ok || string(data) != "current" {
		t.Errorf("file overwritten despite read error: got %q", string(data))
	}
}
