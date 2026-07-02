package starmod_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/exec/exectest"
	"github.com/ptorbus/zester/pkg/starmod"
	"github.com/ptorbus/zester/pkg/state"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// parseStarlarkFunctions executes Starlark source and returns named functions.
func parseStarlarkFunctions(t *testing.T, mctx *exec.ModuleContext, source string) starlark.StringDict {
	t.Helper()
	builtins := starmod.MakeBuiltins(mctx, mctx.Facts, mctx.Settings)
	thread := &starlark.Thread{Name: "test"}
	thread.SetLocal("context", context.Background())
	globals, err := starlark.ExecFileOptions(
		&syntax.FileOptions{},
		thread, "test.star", source, builtins,
	)
	if err != nil {
		t.Fatalf("parse starlark: %v", err)
	}
	return globals
}

func TestStarlarkState_Name(t *testing.T) {
	mctx := testMctx()
	globals := parseStarlarkFunctions(t, mctx, `
def configured(id, config):
    return {"changed": True}
`)

	builder := starmod.NewStarlarkBuilder(
		"nginx.configured",
		globals["configured"].(*starlark.Function),
		nil, nil, nil, mctx,
	)

	s, err := builder("web-config", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "nginx.configured:web-config" {
		t.Errorf("Name() = %q", s.Name())
	}
}

func TestStarlarkState_Requisites(t *testing.T) {
	mctx := testMctx()
	globals := parseStarlarkFunctions(t, mctx, `
def configured(id, config):
    return {"changed": True}
`)

	builder := starmod.NewStarlarkBuilder(
		"nginx.configured",
		globals["configured"].(*starlark.Function),
		nil, nil, nil, mctx,
	)

	s, err := builder("test", map[string]any{
		"require":   []any{"pkg.installed:nginx"},
		"watch":     []any{"file.managed:/etc/nginx"},
		"onchanges": []any{"cmd.run:build"},
		"onfail":    []any{"cmd.run:fallback"},
	})
	if err != nil {
		t.Fatal(err)
	}

	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:nginx" {
		t.Errorf("Require = %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/nginx" {
		t.Errorf("Watch = %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:build" {
		t.Errorf("OnChanges = %v", reqs.OnChanges)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:fallback" {
		t.Errorf("OnFail = %v", reqs.OnFail)
	}
}

func TestStarlarkState_CheckWithFunction(t *testing.T) {
	mctx := testMctx()
	globals := parseStarlarkFunctions(t, mctx, `
def configured(id, config):
    return {"changed": True}

def configured_check(id, config):
    return {"needs_change": True, "diff": "content differs"}
`)

	builder := starmod.NewStarlarkBuilder(
		"nginx.configured",
		globals["configured"].(*starlark.Function),
		globals["configured_check"].(*starlark.Function),
		nil, nil, mctx,
	)

	s, _ := builder("test", map[string]any{"path": "/etc/nginx/nginx.conf"})
	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsChange {
		t.Error("NeedsChange should be true")
	}
	if result.Diff != "content differs" {
		t.Errorf("Diff = %q", result.Diff)
	}
}

func TestStarlarkState_CheckWithoutFunction(t *testing.T) {
	mctx := testMctx()
	globals := parseStarlarkFunctions(t, mctx, `
def configured(id, config):
    return {"changed": True}
`)

	builder := starmod.NewStarlarkBuilder(
		"nginx.configured",
		globals["configured"].(*starlark.Function),
		nil, nil, nil, mctx,
	)

	s, _ := builder("test", map[string]any{})
	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsChange {
		t.Error("NeedsChange should be true when no check function")
	}
}

func TestStarlarkState_Apply(t *testing.T) {
	mctx := testMctx()
	globals := parseStarlarkFunctions(t, mctx, `
def configured(id, config):
    file_write(config["path"], config["content"], 0o644)
    return {"changed": True, "diff": "wrote config"}
`)

	builder := starmod.NewStarlarkBuilder(
		"nginx.configured",
		globals["configured"].(*starlark.Function),
		nil, nil, nil, mctx,
	)

	s, _ := builder("test", map[string]any{
		"path":    "/etc/test.conf",
		"content": "hello",
	})

	result, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("Changed should be true")
	}
	if result.Diff != "wrote config" {
		t.Errorf("Diff = %q", result.Diff)
	}

	// Verify the file was actually written via the fake.
	fake := mctx.File.(*exectest.FakeFileExec)
	data, ok := fake.GetFile("/etc/test.conf")
	if !ok {
		t.Error("file not written")
	}
	if string(data) != "hello" {
		t.Errorf("file content = %q", string(data))
	}
}

func TestStarlarkState_ApplyWithDetails(t *testing.T) {
	mctx := testMctx()
	globals := parseStarlarkFunctions(t, mctx, `
def configured(id, config):
    return {"changed": True, "diff": "ok", "details": {"key": "value"}}
`)

	builder := starmod.NewStarlarkBuilder(
		"nginx.configured",
		globals["configured"].(*starlark.Function),
		nil, nil, nil, mctx,
	)

	s, _ := builder("test", map[string]any{})
	result, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Details["key"] != "value" {
		t.Errorf("Details = %v", result.Details)
	}
}

func TestStarlarkState_RevertWithFunction(t *testing.T) {
	mctx := testMctx()
	fake := mctx.File.(*exectest.FakeFileExec)
	fake.PreCreate("/etc/test.conf", []byte("old"), 0o644)

	globals := parseStarlarkFunctions(t, mctx, `
def configured(id, config):
    return {"changed": True}

def configured_revert(id, config):
    file_remove(config["path"])
    return {"changed": True, "diff": "removed config"}
`)

	builder := starmod.NewStarlarkBuilder(
		"nginx.configured",
		globals["configured"].(*starlark.Function),
		nil,
		globals["configured_revert"].(*starlark.Function),
		nil, mctx,
	)

	s, _ := builder("test", map[string]any{"path": "/etc/test.conf"})
	result, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("Changed should be true")
	}
	if fake.Exists("/etc/test.conf") {
		t.Error("file should be removed")
	}
}

func TestStarlarkState_RevertWithoutFunction(t *testing.T) {
	mctx := testMctx()
	globals := parseStarlarkFunctions(t, mctx, `
def configured(id, config):
    return {"changed": True}
`)

	builder := starmod.NewStarlarkBuilder(
		"nginx.configured",
		globals["configured"].(*starlark.Function),
		nil, nil, nil, mctx,
	)

	s, _ := builder("test", map[string]any{})
	result, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Error("Changed should be false for no-op revert")
	}
}

func TestStarlarkState_RuntimeError(t *testing.T) {
	mctx := testMctx()
	globals := parseStarlarkFunctions(t, mctx, `
def configured(id, config):
    return config["nonexistent_key"]
`)

	builder := starmod.NewStarlarkBuilder(
		"nginx.configured",
		globals["configured"].(*starlark.Function),
		nil, nil, nil, mctx,
	)

	s, _ := builder("test", map[string]any{})
	_, err := s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from Starlark runtime")
	}
}

func TestStarlarkState_ImplementsInterface(t *testing.T) {
	mctx := testMctx()
	globals := parseStarlarkFunctions(t, mctx, `
def configured(id, config):
    return {"changed": False}
`)

	builder := starmod.NewStarlarkBuilder(
		"nginx.configured",
		globals["configured"].(*starlark.Function),
		nil, nil, nil, mctx,
	)

	s, _ := builder("test", map[string]any{})

	// Verify it implements state.State.
	var _ state.State = s

	// Run through full lifecycle.
	ctx := context.Background()
	check, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !check.NeedsChange {
		t.Error("NeedsChange should be true (no check fn)")
	}

	apply, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if apply.Changed {
		t.Error("Changed should be false")
	}

	revert, err := s.Revert(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if revert.Changed {
		t.Error("Changed should be false (no revert fn)")
	}
}

func TestStarlarkState_CheckNoChange(t *testing.T) {
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
			Package: exectest.NewFakePackageExec("test"),
		},
		Logger: slog.Default(),
	}
	fake := mctx.File.(*exectest.FakeFileExec)
	fake.PreCreate("/etc/test.conf", []byte("correct content"), 0o644)

	globals := parseStarlarkFunctions(t, mctx, `
def configured(id, config):
    return {"changed": False}

def configured_check(id, config):
    content = file_read(config["path"])
    if content == config["content"]:
        return {"needs_change": False}
    return {"needs_change": True, "diff": "content differs"}
`)

	builder := starmod.NewStarlarkBuilder(
		"test.configured",
		globals["configured"].(*starlark.Function),
		globals["configured_check"].(*starlark.Function),
		nil, nil, mctx,
	)

	s, _ := builder("test", map[string]any{
		"path":    "/etc/test.conf",
		"content": "correct content",
	})

	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.NeedsChange {
		t.Error("NeedsChange should be false when content matches")
	}
}
