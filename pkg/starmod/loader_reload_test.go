package starmod_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/state"
)

// bumpMtime advances a file's mtime past the loader's last-loaded record so
// the next load pass re-parses it (mtime granularity on some filesystems is a
// full second, so a same-second rewrite would otherwise be skipped).
func bumpMtime(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// TestLoaderReload_RemovedFunctionUnregisters pins review-round-4 item 2: a
// function removed from a reloaded .star file must be neither callable nor
// documented afterwards, while surviving functions keep working.
func TestLoaderReload_RemovedFunctionUnregisters(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	path := filepath.Join(modulesDir, "app.star")
	writeStarFile(t, modulesDir, "app.star", `
def deployed(id, config):
    """Deploy the app."""
    return {"changed": True}

def retired(id, config):
    """Retire the app."""
    return {"changed": True}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()

	if n, err := loader.LoadGlobal(registry); err != nil || n != 2 {
		t.Fatalf("initial load: n=%d err=%v, want 2/nil", n, err)
	}
	if !registry.Has("app.retired") {
		t.Fatal("app.retired not registered after initial load")
	}
	if _, ok := registry.Describe("app.retired"); !ok {
		t.Fatal("app.retired not documented after initial load")
	}

	// Rewrite the file WITHOUT retired and reload.
	writeStarFile(t, modulesDir, "app.star", `
def deployed(id, config):
    """Deploy the app."""
    return {"changed": True}
`)
	bumpMtime(t, path)
	if n, err := loader.LoadGlobal(registry); err != nil || n != 1 {
		t.Fatalf("reload: n=%d err=%v, want 1/nil", n, err)
	}

	if registry.Has("app.retired") {
		t.Error("app.retired still callable after its function was removed")
	}
	if _, ok := registry.Describe("app.retired"); ok {
		t.Error("app.retired still documented after its function was removed")
	}
	if _, ok := loader.LoadedSpec("app.retired"); ok {
		t.Error("LoadedSpec still serves app.retired after removal")
	}
	if !registry.Has("app.deployed") {
		t.Error("surviving function app.deployed lost its registration")
	}
	if _, err := registry.Build("app.deployed", "x", map[string]any{}); err != nil {
		t.Errorf("surviving function no longer builds: %v", err)
	}
}

// TestLoaderReload_DeletedFileUnregisters pins the deleted-file half of
// review-round-4 item 2: deleting a .star file must unregister every module it
// provided on the next load pass.
func TestLoaderReload_DeletedFileUnregisters(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	writeStarFile(t, modulesDir, "app.star", `
def deployed(id, config):
    return {"changed": True}
`)
	writeStarFile(t, modulesDir, "db.star", `
def migrated(id, config):
    return {"changed": True}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()
	if n, err := loader.LoadGlobal(registry); err != nil || n != 2 {
		t.Fatalf("initial load: n=%d err=%v, want 2/nil", n, err)
	}

	if err := os.Remove(filepath.Join(modulesDir, "db.star")); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadGlobal(registry); err != nil {
		t.Fatalf("reload after delete: %v", err)
	}

	if registry.Has("db.migrated") {
		t.Error("db.migrated still callable after its file was deleted")
	}
	if !registry.Has("app.deployed") {
		t.Error("unrelated module app.deployed lost its registration")
	}
}

// TestLoaderReload_DeletedModulesDirUnregisters covers the whole _modules
// directory vanishing (e.g. a statefiles sync pruning it): its modules must be
// unregistered too, not just individually deleted files.
func TestLoaderReload_DeletedModulesDirUnregisters(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	writeStarFile(t, modulesDir, "app.star", `
def deployed(id, config):
    return {"changed": True}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()
	if n, err := loader.LoadGlobal(registry); err != nil || n != 1 {
		t.Fatalf("initial load: n=%d err=%v, want 1/nil", n, err)
	}

	if err := os.RemoveAll(modulesDir); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadGlobal(registry); err != nil {
		t.Fatalf("reload after dir removal: %v", err)
	}
	if registry.Has("app.deployed") {
		t.Error("app.deployed still callable after the _modules dir was deleted")
	}
}

// TestLoaderReload_SurvivingOwnerRestored pins the layering half of removal
// reconciliation: when a formula override is deleted, the GLOBAL file that
// still provides the same module name must be re-executed so its builder
// becomes live again — the name stays registered and runs the global version.
func TestLoaderReload_SurvivingOwnerRestored(t *testing.T) {
	dir := t.TempDir()

	globalDir := filepath.Join(dir, "_modules")
	writeStarFile(t, globalDir, "nginx.star", `
def configured(id, config):
    return {"changed": False, "diff": "global"}
`)
	formulaDir := filepath.Join(dir, "webserver", "_modules")
	writeStarFile(t, formulaDir, "nginx.star", `
def configured(id, config):
    return {"changed": True, "diff": "formula"}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()
	if _, err := loader.LoadGlobal(registry); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadDir(filepath.Join(dir, "webserver"), registry); err != nil {
		t.Fatal(err)
	}

	// Override is live.
	s, err := registry.Build("nginx.configured", "t", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := s.Apply(context.Background()); err != nil || r.Diff != "formula" {
		t.Fatalf("pre-delete: diff=%q err=%v, want formula", r.Diff, err)
	}

	// Delete the formula override; reload the formula dir.
	if err := os.Remove(filepath.Join(formulaDir, "nginx.star")); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadDir(filepath.Join(dir, "webserver"), registry); err != nil {
		t.Fatal(err)
	}

	if !registry.Has("nginx.configured") {
		t.Fatal("nginx.configured unregistered although the global file still provides it")
	}
	s, err = registry.Build("nginx.configured", "t", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := s.Apply(context.Background()); err != nil || r.Diff != "global" {
		t.Errorf("post-delete: diff=%q err=%v, want global (surviving owner's builder)", r.Diff, err)
	}
}

// TestLoader_RefusesToShadowNonStarlarkModule pins the shadow-refusal guard: a
// .star file whose module name collides with an existing non-Starlark (e.g.
// built-in) registration is skipped — silently hijacking pkg.installed
// fleet-wide is a namespace collision, and after removal the built-in's
// builder could never be restored.
func TestLoader_RefusesToShadowNonStarlarkModule(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	writeStarFile(t, modulesDir, "pkg.star", `
def installed(id, config):
    return {"changed": True, "diff": "hijacked"}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()
	builtinRan := false
	registry.Register("pkg.installed", func(id string, cfg map[string]any) (state.State, error) {
		builtinRan = true
		return nil, nil
	})

	if n, err := loader.LoadGlobal(registry); err != nil {
		t.Fatalf("load: %v", err)
	} else if n != 0 {
		t.Fatalf("load registered %d modules, want 0 (shadow refused)", n)
	}

	if _, err := registry.Build("pkg.installed", "x", map[string]any{}); err != nil {
		t.Fatalf("built-in Build errored: %v", err)
	}
	if !builtinRan {
		t.Error("built-in builder did not run — the .star hijacked the registration")
	}
}

// TestLoaderReload_OrphanedFormulaDirSwept covers the dir nobody reloads: a
// formula _modules dir whose LoadDir will never run again (no compiled state
// references the formula anymore) is stat-swept on the next load pass of ANY
// directory, so its modules do not linger.
func TestLoaderReload_OrphanedFormulaDirSwept(t *testing.T) {
	dir := t.TempDir()
	writeStarFile(t, filepath.Join(dir, "_modules"), "app.star", `
def deployed(id, config):
    return {"changed": True}
`)
	formulaDir := filepath.Join(dir, "webserver", "_modules")
	writeStarFile(t, formulaDir, "extra.star", `
def tuned(id, config):
    return {"changed": True}
`)

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()
	if _, err := loader.LoadGlobal(registry); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadDir(filepath.Join(dir, "webserver"), registry); err != nil {
		t.Fatal(err)
	}
	if !registry.Has("extra.tuned") {
		t.Fatal("formula module not registered")
	}

	// The whole formula vanishes; only the GLOBAL dir is ever loaded again.
	if err := os.RemoveAll(filepath.Join(dir, "webserver")); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadGlobal(registry); err != nil {
		t.Fatal(err)
	}

	if registry.Has("extra.tuned") {
		t.Error("orphaned formula module still callable after a global load pass")
	}
	if !registry.Has("app.deployed") {
		t.Error("global module lost its registration")
	}
}
