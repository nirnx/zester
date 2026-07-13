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

// TestLoader_UnloadAllThenFreshLoaderReload pins review-round-5 item 1: when
// the peel switches states directories it replaces the loader but keeps the
// REGISTRY. The old loader must purge its registrations first (UnloadAll) —
// otherwise the fresh loader's empty ownership ledger sees the old Starlark
// names as non-Starlark and shadow-refuses every reload, freezing them until
// restart. After the purge, a fresh loader on a new tree must register the
// same module name without refusal, old-tree-only modules must be gone, and
// non-loader (built-in) registrations must survive untouched.
func TestLoader_UnloadAllThenFreshLoaderReload(t *testing.T) {
	oldDir := t.TempDir()
	writeStarFile(t, filepath.Join(oldDir, "_modules"), "nginx.star", `
def configured(id, config):
    return {"changed": False, "diff": "old-tree"}
`)
	writeStarFile(t, filepath.Join(oldDir, "_modules"), "legacy.star", `
def only_in_old_tree(id, config):
    return {"changed": True}
`)
	newDir := t.TempDir()
	writeStarFile(t, filepath.Join(newDir, "_modules"), "nginx.star", `
def configured(id, config):
    return {"changed": False, "diff": "new-tree"}
`)

	registry := state.NewRegistry()
	registry.Register("pkg.installed", func(id string, cfg map[string]any) (state.State, error) {
		return nil, nil
	})

	oldLoader, _ := testLoader(t, oldDir)
	if n, err := oldLoader.LoadGlobal(registry); err != nil || n != 2 {
		t.Fatalf("old-tree load: n=%d err=%v, want 2/nil", n, err)
	}

	// The states-dir switch sequence: purge, then a FRESH loader on the new dir.
	oldLoader.UnloadAll(registry)
	if registry.Has("legacy.only_in_old_tree") {
		t.Error("old-tree module survived UnloadAll")
	}
	if registry.Has("nginx.configured") {
		t.Error("nginx.configured survived UnloadAll (must re-register from the new tree)")
	}
	if !registry.Has("pkg.installed") {
		t.Fatal("UnloadAll removed a non-loader (built-in) registration")
	}

	newLoader, _ := testLoader(t, newDir)
	if n, err := newLoader.LoadGlobal(registry); err != nil || n != 1 {
		t.Fatalf("new-tree load: n=%d err=%v, want 1/nil — a leftover registration shadow-refused the reload", n, err)
	}
	s, err := registry.Build("nginx.configured", "t", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := s.Apply(context.Background()); err != nil || r.Diff != "new-tree" {
		t.Errorf("post-switch: diff=%q err=%v, want new-tree", r.Diff, err)
	}

	// Regression shape without the purge: a fresh loader over a live registry
	// refuses everything — proving the switch path NEEDS UnloadAll.
	stale, _ := testLoader(t, newDir)
	registryWithGhost := state.NewRegistry()
	registryWithGhost.Register("nginx.configured", func(id string, cfg map[string]any) (state.State, error) {
		return nil, nil
	})
	if n, err := stale.LoadGlobal(registryWithGhost); err != nil || n != 0 {
		t.Fatalf("control: fresh loader over a foreign registration registered n=%d err=%v, want 0 (shadow refusal)", n, err)
	}
}

// TestLoaderReload_FailedLoadRetriesWithoutMtimeChange pins the round-5
// verification note: a .star that fails to execute must be RETRIED on the next
// load pass even though its mtime is unchanged — post-switch (old
// registrations purged) a skipped failure would leave the module gone until a
// republish or restart. The error must surface on every pass, and a fixed file
// (mtime bumped, as any real edit/republish produces) must then load.
func TestLoaderReload_FailedLoadRetriesWithoutMtimeChange(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	path := filepath.Join(modulesDir, "app.star")
	writeStarFile(t, modulesDir, "app.star", `
def deployed(id, config)
    return {"changed": True}
`) // missing ':' — syntax error

	loader, _ := testLoader(t, dir)
	registry := state.NewRegistry()
	if _, err := loader.LoadGlobal(registry); err == nil {
		t.Fatal("first load of a broken file did not error")
	}
	// Unchanged file: the pass must RETRY (and re-report), not silently skip.
	if _, err := loader.LoadGlobal(registry); err == nil {
		t.Fatal("second load pass silently skipped the still-broken file")
	}

	writeStarFile(t, modulesDir, "app.star", `
def deployed(id, config):
    return {"changed": True}
`)
	bumpMtime(t, path)
	if n, err := loader.LoadGlobal(registry); err != nil || n != 1 {
		t.Fatalf("load after fix: n=%d err=%v, want 1/nil", n, err)
	}
	if !registry.Has("app.deployed") {
		t.Error("fixed module not registered")
	}
}
