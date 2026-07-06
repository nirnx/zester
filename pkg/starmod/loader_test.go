package starmod_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/starmod"
	"github.com/nirnx/zester/pkg/state"
)

func testLoader(t *testing.T, statesDir string) (*starmod.Loader, *exec.ModuleContext) {
	t.Helper()
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Package: exectest.NewFakePackageExec("test"),
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
		Facts:  map[string]any{"os": "linux"},
		Logger: slog.Default(),
	}
	loader := starmod.NewLoader(starmod.LoaderConfig{
		StatesDir:     statesDir,
		ModuleContext: mctx,
		Logger:        slog.Default(),
	})
	return loader, mctx
}

func writeStarFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoader_SingleFileSingleFunction(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	writeStarFile(t, modulesDir, "nginx.star", `
def configured(id, config):
    return {"changed": True}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	count, err := loader.LoadGlobal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}

	// Verify the module is registered.
	s, err := registry.Build("nginx.configured", "test", map[string]any{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if s.Name() != "nginx.configured:test" {
		t.Errorf("Name = %q", s.Name())
	}
}

func TestLoader_SingleFileMultipleFunctions(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	writeStarFile(t, modulesDir, "nginx.star", `
def configured(id, config):
    return {"changed": True}

def running(id, config):
    return {"changed": False}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	count, _ := loader.LoadGlobal(registry)
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}

	// Both modules should be registered.
	if _, err := registry.Build("nginx.configured", "test", map[string]any{}); err != nil {
		t.Errorf("nginx.configured: %v", err)
	}
	if _, err := registry.Build("nginx.running", "test", map[string]any{}); err != nil {
		t.Errorf("nginx.running: %v", err)
	}
}

func TestLoader_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	writeStarFile(t, modulesDir, "nginx.star", `
def configured(id, config):
    return {"changed": True}
`)
	writeStarFile(t, modulesDir, "postgres.star", `
def running(id, config):
    return {"changed": False}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	count, _ := loader.LoadGlobal(registry)
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}

	if _, err := registry.Build("nginx.configured", "test", map[string]any{}); err != nil {
		t.Errorf("nginx.configured: %v", err)
	}
	if _, err := registry.Build("postgres.running", "test", map[string]any{}); err != nil {
		t.Errorf("postgres.running: %v", err)
	}
}

func TestLoader_SyntaxError(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")

	// Good file.
	writeStarFile(t, modulesDir, "good.star", `
def hello(id, config):
    return {"changed": False}
`)
	// Bad file.
	writeStarFile(t, modulesDir, "bad.star", `
def broken(
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	count, err := loader.LoadGlobal(registry)
	// Should load the good file and skip the bad one.
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
	if err == nil {
		t.Error("expected non-nil error for syntax error")
	}

	// Good module should still be registered.
	if _, err := registry.Build("good.hello", "test", map[string]any{}); err != nil {
		t.Errorf("good.hello: %v", err)
	}
}

func TestLoader_PrivateHelpers(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	writeStarFile(t, modulesDir, "mymod.star", `
def _helper():
    return "internal"

def public_action(id, config):
    _helper()
    return {"changed": True}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	count, _ := loader.LoadGlobal(registry)
	if count != 1 {
		t.Errorf("count = %d, want 1 (only public_action)", count)
	}

	// _helper should not be registered.
	_, err := registry.Build("mymod._helper", "test", map[string]any{})
	if err == nil {
		t.Error("_helper should not be registered")
	}
}

func TestLoader_OnlyCheckWithoutApply(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	writeStarFile(t, modulesDir, "orphan.star", `
def something_check(id, config):
    return {"needs_change": False}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	count, _ := loader.LoadGlobal(registry)
	if count != 0 {
		t.Errorf("count = %d, want 0 (check without apply)", count)
	}
}

func TestLoader_WithCheckAndRevert(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	writeStarFile(t, modulesDir, "mymod.star", `
def configured(id, config):
    return {"changed": True}

def configured_check(id, config):
    return {"needs_change": True, "diff": "test"}

def configured_revert(id, config):
    return {"changed": True, "diff": "reverted"}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	count, _ := loader.LoadGlobal(registry)
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}

	s, _ := registry.Build("mymod.configured", "test", map[string]any{})

	// Check should work.
	check, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !check.NeedsChange {
		t.Error("NeedsChange should be true")
	}

	// Revert should work.
	revert, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !revert.Changed {
		t.Error("Changed should be true")
	}
}

func TestLoader_EmptyModulesDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "_modules"), 0o755)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	count, err := loader.LoadGlobal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestLoader_MissingModulesDir(t *testing.T) {
	dir := t.TempDir()

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	count, err := loader.LoadGlobal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestLoader_Idempotent(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	writeStarFile(t, modulesDir, "nginx.star", `
def configured(id, config):
    return {"changed": True}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	count1, _ := loader.LoadGlobal(registry)
	count2, _ := loader.LoadGlobal(registry)

	if count1 != 1 {
		t.Errorf("first load count = %d, want 1", count1)
	}
	if count2 != 0 {
		t.Errorf("second load count = %d, want 0 (idempotent)", count2)
	}
}

func TestLoader_HotReload(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")

	// Write the initial version of the module.
	writeStarFile(t, modulesDir, "nginx.star", `
def configured(id, config):
    return {"changed": True, "diff": "v1"}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	count, _ := loader.LoadGlobal(registry)
	if count != 1 {
		t.Fatalf("first load count = %d, want 1", count)
	}

	// Run the v1 module.
	s, _ := registry.Build("nginx.configured", "test", map[string]any{})
	result, _ := s.Apply(context.Background())
	if result.Diff != "v1" {
		t.Fatalf("Diff = %q, want v1", result.Diff)
	}

	// Overwrite with v2. Advance mtime by 1 second to guarantee the
	// filesystem reports a newer timestamp (some filesystems have
	// second-level granularity).
	time.Sleep(10 * time.Millisecond)
	filePath := filepath.Join(modulesDir, "nginx.star")
	newContent := []byte(`
def configured(id, config):
    return {"changed": True, "diff": "v2"}
`)
	if err := os.WriteFile(filePath, newContent, 0o644); err != nil {
		t.Fatal(err)
	}
	// Bump mtime explicitly to avoid filesystem granularity issues.
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(filePath, future, future)

	// Reload — should detect the newer mtime and re-parse.
	count2, _ := loader.LoadGlobal(registry)
	if count2 != 1 {
		t.Errorf("hot reload count = %d, want 1 (re-registered)", count2)
	}

	// Run the v2 module.
	s2, _ := registry.Build("nginx.configured", "test", map[string]any{})
	result2, _ := s2.Apply(context.Background())
	if result2.Diff != "v2" {
		t.Errorf("Diff after reload = %q, want v2", result2.Diff)
	}
}

func TestLoader_HotReload_SharedHelper(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")

	// Helper file used via load().
	writeStarFile(t, modulesDir, "helpers.star", `
def version():
    return "v1"
`)
	writeStarFile(t, modulesDir, "app.star", `
load("helpers.star", "version")

def deployed(id, config):
    return {"changed": True, "diff": version()}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	loader.LoadGlobal(registry)

	s, _ := registry.Build("app.deployed", "test", map[string]any{})
	result, _ := s.Apply(context.Background())
	if result.Diff != "v1" {
		t.Fatalf("Diff = %q, want v1", result.Diff)
	}

	// Update both files.
	time.Sleep(10 * time.Millisecond)
	helpersPath := filepath.Join(modulesDir, "helpers.star")
	appPath := filepath.Join(modulesDir, "app.star")

	os.WriteFile(helpersPath, []byte(`
def version():
    return "v2"
`), 0o644)
	os.WriteFile(appPath, []byte(`
load("helpers.star", "version")

def deployed(id, config):
    return {"changed": True, "diff": version()}
`), 0o644)

	// Bump mtimes.
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(helpersPath, future, future)
	os.Chtimes(appPath, future, future)

	// Reload — should invalidate the load() cache and pick up new helper.
	count, _ := loader.LoadGlobal(registry)
	if count < 1 {
		t.Errorf("hot reload count = %d, want >= 1", count)
	}

	s2, _ := registry.Build("app.deployed", "test", map[string]any{})
	result2, _ := s2.Apply(context.Background())
	if result2.Diff != "v2" {
		t.Errorf("Diff after reload = %q, want v2", result2.Diff)
	}
}

func TestLoader_LoadDir_FormulaSpecific(t *testing.T) {
	dir := t.TempDir()
	formulaDir := filepath.Join(dir, "webserver", "_modules")
	writeStarFile(t, formulaDir, "nginx.star", `
def configured(id, config):
    return {"changed": True}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	count, _ := loader.LoadDir(filepath.Join(dir, "webserver"), registry)
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}

	s, err := registry.Build("nginx.configured", "test", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "nginx.configured:test" {
		t.Errorf("Name = %q", s.Name())
	}
}

func TestLoader_FormulaOverridesGlobal(t *testing.T) {
	dir := t.TempDir()

	// Global module.
	globalDir := filepath.Join(dir, "_modules")
	writeStarFile(t, globalDir, "nginx.star", `
def configured(id, config):
    return {"changed": False, "diff": "global"}
`)

	// Formula-specific override.
	formulaDir := filepath.Join(dir, "webserver", "_modules")
	writeStarFile(t, formulaDir, "nginx.star", `
def configured(id, config):
    return {"changed": True, "diff": "formula"}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	// Load global first, then formula.
	loader.LoadGlobal(registry)
	loader.LoadDir(filepath.Join(dir, "webserver"), registry)

	s, _ := registry.Build("nginx.configured", "test", map[string]any{})
	result, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Diff != "formula" {
		t.Errorf("Diff = %q, want 'formula' (override)", result.Diff)
	}
}

func TestLoader_EndToEnd_RunnerRun(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	writeStarFile(t, modulesDir, "hello.star", `
def world(id, config):
    msg = config.get("message", "default")
    return {"changed": False, "details": {"message": msg}}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	count, _ := loader.LoadGlobal(registry)
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	// Build and run through the state runner.
	s, err := registry.Build("hello.world", "greeting", map[string]any{
		"message": "hi there",
	})
	if err != nil {
		t.Fatal(err)
	}

	runner := state.NewRunner(slog.Default())
	result, err := runner.Run(context.Background(), []state.State{s}, state.ModeApply)
	if err != nil {
		t.Fatal(err)
	}

	if result.Failed != 0 {
		t.Errorf("failed = %d", result.Failed)
	}

	sr, ok := result.States["hello.world:greeting"]
	if !ok {
		t.Fatal("state result not found")
	}
	if sr.Changed {
		t.Error("should not be changed")
	}
	if sr.Details["message"] != "hi there" {
		t.Errorf("message = %q", sr.Details["message"])
	}
}

func TestLoader_LoadBetweenStarFiles(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")

	writeStarFile(t, modulesDir, "helpers.star", `
def make_result(changed):
    return {"changed": changed}
`)

	writeStarFile(t, modulesDir, "mymod.star", `
load("helpers.star", "make_result")

def action(id, config):
    return make_result(True)
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	_, err := loader.LoadGlobal(registry)
	if err != nil {
		t.Fatal(err)
	}

	s, err := registry.Build("mymod.action", "test", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("Changed should be true")
	}
}

func TestUniqueParentDirs(t *testing.T) {
	statesDir := "/data/states"

	dirs := starmod.UniqueParentDirs([]string{
		"/data/states/webserver/init.zy",
		"/data/states/webserver/config.zy",
		"/data/states/common/packages.zy",
	}, statesDir)

	// Should have: /data/states/webserver, /data/states/common, /data/states
	found := make(map[string]bool)
	for _, d := range dirs {
		found[d] = true
	}

	if !found["/data/states/webserver"] {
		t.Error("missing webserver dir")
	}
	if !found["/data/states/common"] {
		t.Error("missing common dir")
	}
	if !found[statesDir] {
		t.Error("missing statesDir itself")
	}
}
