package starmod

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// LoaderConfig holds the configuration for the Starlark module loader.
type LoaderConfig struct {
	StatesDir     string              // root states directory
	ModuleContext *exec.ModuleContext // execution providers for builtins
	Logger        *slog.Logger

	// DecodeOptions threads the peel's unknown-key validation policy through to
	// self-documenting Starlark modules. For a module that declared a PARAMS /
	// <fn>_params dict, unknown config keys are surfaced per this policy (the
	// peel wires Unknown: PolicyWarn + Warnf: logger). The Reserved key set is
	// owned by the loader (state.ReservedKeySet) — Starlark states run through
	// the same requisite/attribute/compiler machinery — so callers need only set
	// the policy and warn sink here. The zero value (PolicyIgnore) disables
	// warnings.
	DecodeOptions modschema.DecodeOptions
}

// Loader discovers and loads Starlark module files from _modules/ directories.
// It scans for .star files, parses them, discovers apply/check/revert functions,
// and registers them as state modules in the Registry.
//
// Hot-reload: if a .star file's modification time changes between calls,
// the Loader re-parses the file and re-registers all its modules. Functions a
// reloaded file no longer defines — and every module of a deleted file — are
// UNREGISTERED (removed modules must be neither callable nor documented),
// unless another loaded file still provides the name, in which case that
// surviving owner is re-executed so its builder becomes live again.
type Loader struct {
	config LoaderConfig
	mu     sync.Mutex
	loaded map[string]time.Time           // absolute path → last loaded mod time
	cache  map[string]starlark.StringDict // load() cache for shared .star files
	// specs is the loader's own authoritative, always-fresh record of each
	// module's captured documentation spec (module name → spec). It is refreshed
	// on every (re)load, so the hot-reload doc-freshness guarantee holds
	// independent of the state Registry's strict, replace-less RegisterSpec.
	// A nil value records a module that loaded but produced no spec.
	specs map[string]*modschema.Spec
	// fileModules records, per absolute .star file path, the set of module
	// names that file registered on its last successful load. It is the
	// ownership ledger behind removal reconciliation: a name a reloaded file
	// no longer provides (or whose file was deleted) is unregistered from the
	// Registry unless another loaded file still provides it — in which case
	// that surviving owner is re-executed so ITS builder becomes live again.
	fileModules map[string]map[string]bool
	// owned is the union of all fileModules name sets. A Registry name that
	// exists but is NOT loader-owned belongs to a built-in (or other
	// non-Starlark) registration; the loader refuses to shadow it.
	owned map[string]bool
}

// NewLoader creates a new Starlark module loader.
func NewLoader(cfg LoaderConfig) *Loader {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Loader{
		config:      cfg,
		loaded:      make(map[string]time.Time),
		cache:       make(map[string]starlark.StringDict),
		specs:       make(map[string]*modschema.Spec),
		fileModules: make(map[string]map[string]bool),
		owned:       make(map[string]bool),
	}
}

// LoadedSpec returns the loader's most-recently-captured documentation spec for
// a module. Unlike Registry.Describe (which the strict RegisterSpec cannot
// refresh on a hot-reload), this reflects the latest on-disk docstring/PARAMS
// on every reload. It reports (nil, false) for an unknown or spec-less module.
func (l *Loader) LoadedSpec(module string) (*modschema.Spec, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	spec, ok := l.specs[module]
	return spec, ok && spec != nil
}

// LoadGlobal scans {StatesDir}/_modules/ for globally available Starlark modules.
// Called once at peel startup.
func (l *Loader) LoadGlobal(registry *state.Registry) (int, error) {
	modulesDir := filepath.Join(l.config.StatesDir, "_modules")
	return l.loadModulesDir(modulesDir, registry)
}

// LoadDir scans {dir}/_modules/ for formula-specific Starlark modules.
// Called by the compiler for each directory that contained state files.
// Idempotent: re-loading the same .star file is a no-op.
func (l *Loader) LoadDir(dir string, registry *state.Registry) (int, error) {
	modulesDir := filepath.Join(dir, "_modules")
	return l.loadModulesDir(modulesDir, registry)
}

// loadModulesDir is the shared implementation for LoadGlobal and LoadDir.
func (l *Loader) loadModulesDir(modulesDir string, registry *state.Registry) (int, error) {
	info, err := os.Stat(modulesDir)
	dirExists := err == nil && info.IsDir()

	var entries []os.DirEntry
	if dirExists {
		entries, err = os.ReadDir(modulesDir)
		if err != nil {
			return 0, fmt.Errorf("starmod: read %s: %w", modulesDir, err)
		}
	}

	// Reconcile deletions FIRST: a previously loaded .star file that no longer
	// exists under this directory (including the directory itself vanishing)
	// must not keep its modules callable or documented.
	present := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".star") {
			present[filepath.Join(modulesDir, entry.Name())] = true
		}
	}
	l.unloadMissingFiles(modulesDir, present, registry)

	if !dirExists {
		return 0, nil
	}

	count := 0
	var firstErr error

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".star") {
			continue
		}

		absPath := filepath.Join(modulesDir, entry.Name())

		info, err := entry.Info()
		if err != nil {
			continue
		}
		modTime := info.ModTime()

		l.mu.Lock()
		prev, seen := l.loaded[absPath]
		if seen && !modTime.After(prev) {
			// Already loaded and file hasn't changed.
			l.mu.Unlock()
			continue
		}
		l.loaded[absPath] = modTime
		if seen {
			// File changed — invalidate load() cache for this directory
			// so that shared helpers get re-evaluated too.
			l.invalidateCacheDir(modulesDir)
			l.config.Logger.Info("starmod: reloading changed file", "file", absPath)
		}
		l.mu.Unlock()

		n, err := l.loadFile(absPath, registry)
		if err != nil {
			l.config.Logger.Warn("starmod: skipping file with errors",
				"file", absPath, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		count += n
	}

	return count, firstErr
}

// loadFile executes a single .star file and registers discovered functions.
func (l *Loader) loadFile(absPath string, registry *state.Registry) (int, error) {
	// Build builtins for this execution.
	builtins := MakeBuiltins(l.config.ModuleContext,
		l.config.ModuleContext.Facts,
		l.config.ModuleContext.Settings)

	// Set up the thread with a load function for inter-file imports.
	thread := &starlark.Thread{
		Name: absPath,
		Load: l.makeLoadFunc(builtins),
	}

	globals, err := starlark.ExecFileOptions(
		&syntax.FileOptions{},
		thread, absPath, nil, builtins,
	)
	if err != nil {
		return 0, fmt.Errorf("starmod: exec %s: %w", absPath, err)
	}

	// Derive module prefix from filename: nginx.star → "nginx"
	baseName := strings.TrimSuffix(filepath.Base(absPath), ".star")

	// Discover functions: collect all non-underscore, non-_check, non-_revert callables.
	applyFns := make(map[string]*starlark.Function)
	checkFns := make(map[string]*starlark.Function)
	revertFns := make(map[string]*starlark.Function)

	for name, val := range globals {
		fn, ok := val.(*starlark.Function)
		if !ok {
			continue
		}
		if strings.HasPrefix(name, "_") {
			continue
		}
		if base, ok := strings.CutSuffix(name, "_check"); ok {
			checkFns[base] = fn
		} else if base, ok := strings.CutSuffix(name, "_revert"); ok {
			revertFns[base] = fn
		} else {
			applyFns[name] = fn
		}
	}

	// Collect optional parameter-declaration dicts (PARAMS module-global and
	// per-function <fn>_params) for self-documentation.
	moduleParams, fnParams := collectParamDicts(globals)

	count := 0
	registered := make(map[string]bool, len(applyFns))
	for name, applyFn := range applyFns {
		moduleName := baseName + "." + name

		builder := NewStarlarkBuilder(
			moduleName,
			applyFn,
			checkFns[name],
			revertFns[name],
			builtins,
			l.config.ModuleContext,
		)

		// Resolve the declaration dict: per-function wins over module-global.
		paramsDict := fnParams[name]
		if paramsDict == nil {
			paramsDict = moduleParams
		}

		// Capture documentation (docstring + Args: + PARAMS) into a modschema
		// Spec. A capture failure is never fatal — the module still registers
		// plainly, just without a schema.
		spec, specErr := buildStarSpec(moduleName, applyFn, paramsDict)
		if specErr != nil {
			l.config.Logger.Warn("starmod: documentation capture failed; registering without a spec",
				"module", moduleName, "error", specErr)
			spec = nil
		}

		if !l.registerStarModule(registry, moduleName, spec, builder, absPath) {
			continue
		}
		l.config.Logger.Info("starmod: registered module",
			"module", moduleName,
			"has_check", checkFns[name] != nil,
			"has_revert", revertFns[name] != nil,
			"has_spec", spec != nil,
		)
		registered[moduleName] = true
		count++
	}

	// The file executed successfully: reconcile module names it registered on a
	// PREVIOUS load but no longer provides. (On an exec error above, the old
	// registrations deliberately stay live — last-known-good semantics.)
	l.reconcileRemoved(absPath, registered, registry)

	return count, nil
}

// unloadMissingFiles unregisters the modules of previously loaded .star files
// that no longer exist. Files directly under modulesDir are checked against
// present (this pass's directory listing); every OTHER previously loaded file
// is stat-checked, so an orphaned formula _modules dir — one no compiled state
// references anymore, whose own LoadDir will never run again — is reconciled
// on the NEXT load pass of ANY directory. Only a confirmed not-exist counts as
// deleted; a transient stat error never unregisters.
func (l *Loader) unloadMissingFiles(modulesDir string, present map[string]bool, registry *state.Registry) {
	l.mu.Lock()
	known := make([]string, 0, len(l.loaded))
	for p := range l.loaded {
		known = append(known, p)
	}
	l.mu.Unlock()
	sort.Strings(known)

	var deleted []string
	for _, p := range known {
		if filepath.Dir(p) == modulesDir {
			if !present[p] {
				deleted = append(deleted, p)
			}
			continue
		}
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			deleted = append(deleted, p)
		}
	}
	if len(deleted) == 0 {
		return
	}

	l.mu.Lock()
	dirs := make(map[string]bool)
	for _, p := range deleted {
		delete(l.loaded, p)
		dirs[filepath.Dir(p)] = true
	}
	for d := range dirs {
		l.invalidateCacheDir(d)
	}
	l.mu.Unlock()

	for _, p := range deleted {
		l.config.Logger.Info("starmod: module file deleted; unregistering its modules", "file", p)
		l.reconcileRemoved(p, nil, registry)
		l.mu.Lock()
		delete(l.fileModules, p)
		l.mu.Unlock()
	}
}

// reconcileRemoved updates absPath's ownership ledger entry to newNames and
// handles every module name the file previously provided but no longer does:
// if another loaded file still provides the name, that surviving owner file is
// re-executed so its builder becomes live again (formula/global layering);
// otherwise the name is unregistered from the Registry and forgotten.
func (l *Loader) reconcileRemoved(absPath string, newNames map[string]bool, registry *state.Registry) {
	l.mu.Lock()
	old := l.fileModules[absPath]
	l.fileModules[absPath] = newNames

	var gone []string
	reload := make(map[string]bool)
	for name := range old {
		if newNames[name] {
			continue
		}
		survivor := ""
		for f, names := range l.fileModules {
			if f != absPath && names[name] {
				survivor = f
				break
			}
		}
		if survivor != "" {
			reload[survivor] = true
			continue
		}
		gone = append(gone, name)
		delete(l.owned, name)
		delete(l.specs, name)
	}
	l.mu.Unlock()

	sort.Strings(gone)
	for _, name := range gone {
		if registry.Unregister(name) {
			l.config.Logger.Info("starmod: unregistered removed module",
				"module", name, "file", absPath)
		}
	}

	survivors := make([]string, 0, len(reload))
	for f := range reload {
		survivors = append(survivors, f)
	}
	sort.Strings(survivors)
	for _, f := range survivors {
		l.config.Logger.Info("starmod: re-executing surviving owner of removed module override", "file", f)
		if _, err := l.loadFile(f, registry); err != nil {
			l.config.Logger.Warn("starmod: surviving owner re-execution failed; its previous registrations stay live",
				"file", f, "error", err)
		}
	}
}

// registerStarModule installs a Starlark module's builder and, when available,
// its self-documentation spec into the state Registry, and records the fresh
// spec in the loader's own store.
//
// Starlark registration is inherently dynamic — a hot-reload re-registers the
// same name and a formula module overrides a global one — so documented
// modules go through the Registry's ReplaceSpec seam (Describe/sys.doc always
// serve the LATEST docs) and undocumented ones through plain Register. The
// loader's own specs map is refreshed in step, and the ownership ledger
// (fileModules/owned) records which file provides the name so removal
// reconciliation and shadow refusal work. It reports whether the module was
// actually registered (false = shadow refusal).
func (l *Loader) registerStarModule(registry *state.Registry, moduleName string, spec *modschema.Spec, inner state.Builder, absPath string) bool {
	// Shadow refusal: a Registry name the loader has never owned belongs to a
	// built-in (or other non-Starlark) registration. A .star file must not
	// hijack it — silently replacing pkg.installed fleet-wide is a namespace
	// collision, and once the .star is removed the built-in's builder could
	// never be restored. Starlark-over-Starlark overrides (formula overrides
	// global, hot-reload) remain allowed: those names are loader-owned.
	l.mu.Lock()
	owned := l.owned[moduleName]
	l.mu.Unlock()
	if !owned && registry.Has(moduleName) {
		l.config.Logger.Error("starmod: refusing to register module: name is already registered by a non-Starlark module",
			"module", moduleName, "file", absPath)
		return false
	}

	builder := inner
	if spec != nil && !spec.OpenParams {
		builder = l.validatingBuilder(spec, inner)
	}

	if spec != nil {
		// ReplaceSpec is the framework's dynamic-registration seam (added for
		// this exact case): hot-reloads and formula-overrides-global re-register
		// the same name, and Describe/sys.doc must serve the LATEST docs.
		if err := registry.ReplaceSpec(spec, builder); err != nil {
			// Only nil-spec/name/builder validation can fail; fall back to a
			// plain Register so the module itself stays usable.
			registry.Register(moduleName, builder)
			l.config.Logger.Debug("starmod: spec registration fell back to plain register",
				"module", moduleName, "error", err)
		}
	} else {
		registry.Register(moduleName, builder)
	}

	l.mu.Lock()
	l.specs[moduleName] = spec
	l.owned[moduleName] = true
	l.mu.Unlock()
	return true
}

// validatingBuilder wraps a Starlark builder so that, when the module declared
// its parameters (PARAMS / <fn>_params), each construction runs the compiled
// schema's Decode against the configured DecodeOptions. The decoded proto is
// discarded — the Starlark module reads the raw config.
//
// Unknown-key handling honors the policy exactly like a built-in state module:
// under PolicyError (strict_params) an unknown parameter is a HARD build failure
// (the typed UnknownKeyError, naming the key and a did-you-mean suggestion — the
// typo guard applies to Starlark modules that declared PARAMS too); under
// PolicyWarn/Ignore it is logged/warned and the build proceeds. A type or
// required-field mismatch, by contrast, is ALWAYS non-fatal (logged at debug):
// the Starlark module is dynamically typed and reads the raw config itself, so a
// proto-shaped decode failure that is not an unknown key must never block
// construction — even under strict.
func (l *Loader) validatingBuilder(spec *modschema.Spec, inner state.Builder) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if _, err := spec.Decode(id, config, spec.NewParams(), l.decodeOpts()); err != nil {
			var unknown *modschema.UnknownKeyError
			if errors.As(err, &unknown) {
				// PolicyError produced an unknown-key error: fail the build. (Under
				// PolicyWarn/Ignore Decode never returns an UnknownKeyError, so this
				// branch is strict-only.)
				return nil, err
			}
			l.config.Logger.Debug("starmod: parameter validation reported issues",
				"module", spec.Module, "id", id, "error", err)
		}
		return inner(id, config)
	}
}

// decodeOpts builds the DecodeOptions for a validation pass: the caller-supplied
// policy and warn sink, plus the loader-owned reserved-key set (state requisite/
// attribute/compiler directives, never a module's own parameters) with "name"
// added (the legitimate primary-override key). A PolicyWarn without a Warnf is
// bridged to the loader's logger so warnings are never silently dropped.
func (l *Loader) decodeOpts() modschema.DecodeOptions {
	opts := l.config.DecodeOptions
	reserved := state.ReservedKeySet()
	for k := range opts.Reserved {
		reserved[k] = struct{}{}
	}
	opts.Reserved = reserved
	opts.ExtraReserved = append(append([]string(nil), opts.ExtraReserved...), "name")
	if opts.Unknown == modschema.PolicyWarn && opts.Warnf == nil {
		logger := l.config.Logger
		opts.Warnf = func(format string, args ...any) { logger.Warn(fmt.Sprintf(format, args...)) }
	}
	return opts
}

// makeLoadFunc creates a Starlark load() function that supports importing
// other .star files from the same _modules/ directory.
func (l *Loader) makeLoadFunc(builtins starlark.StringDict) func(thread *starlark.Thread, module string) (starlark.StringDict, error) {
	return func(thread *starlark.Thread, module string) (starlark.StringDict, error) {
		// Resolve the module path relative to the thread's file.
		dir := filepath.Dir(thread.CallStack().At(0).Pos.Filename())
		absPath := filepath.Join(dir, module)

		l.mu.Lock()
		if cached, ok := l.cache[absPath]; ok {
			l.mu.Unlock()
			return cached, nil
		}
		l.mu.Unlock()

		loadThread := &starlark.Thread{
			Name: absPath,
			Load: l.makeLoadFunc(builtins),
		}

		globals, err := starlark.ExecFileOptions(
			&syntax.FileOptions{},
			loadThread, absPath, nil, builtins,
		)
		if err != nil {
			return nil, err
		}

		l.mu.Lock()
		l.cache[absPath] = globals
		l.mu.Unlock()

		return globals, nil
	}
}

// invalidateCacheDir removes all load() cache entries whose paths are under dir.
// Must be called with l.mu held.
func (l *Loader) invalidateCacheDir(dir string) {
	prefix := dir + string(filepath.Separator)
	for path := range l.cache {
		if strings.HasPrefix(path, prefix) || filepath.Dir(path) == dir {
			delete(l.cache, path)
		}
	}
}

// UniqueParentDirs extracts unique parent directories from source file paths.
// For example, if sources include "webserver/init.zy" and "webserver/config.zy",
// the result includes "{statesDir}/webserver".
func UniqueParentDirs(sources []string, statesDir string) []string {
	seen := make(map[string]bool)
	var dirs []string
	for _, src := range sources {
		// Source paths may be absolute or relative to statesDir.
		var dir string
		if filepath.IsAbs(src) {
			dir = filepath.Dir(src)
		} else {
			dir = filepath.Dir(filepath.Join(statesDir, src))
		}
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	// Also include the statesDir itself for top-level _modules/.
	if !seen[statesDir] {
		dirs = append(dirs, statesDir)
	}
	return dirs
}
