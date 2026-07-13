package starmod

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
// the Loader re-parses the file and re-registers all its modules.
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
}

// NewLoader creates a new Starlark module loader.
func NewLoader(cfg LoaderConfig) *Loader {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Loader{
		config: cfg,
		loaded: make(map[string]time.Time),
		cache:  make(map[string]starlark.StringDict),
		specs:  make(map[string]*modschema.Spec),
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
	if err != nil || !info.IsDir() {
		return 0, nil
	}

	entries, err := os.ReadDir(modulesDir)
	if err != nil {
		return 0, fmt.Errorf("starmod: read %s: %w", modulesDir, err)
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
		if strings.HasSuffix(name, "_check") {
			base := strings.TrimSuffix(name, "_check")
			checkFns[base] = fn
		} else if strings.HasSuffix(name, "_revert") {
			base := strings.TrimSuffix(name, "_revert")
			revertFns[base] = fn
		} else {
			applyFns[name] = fn
		}
	}

	// Collect optional parameter-declaration dicts (PARAMS module-global and
	// per-function <fn>_params) for self-documentation.
	moduleParams, fnParams := collectParamDicts(globals)

	count := 0
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

		l.registerStarModule(registry, moduleName, spec, builder)
		l.config.Logger.Info("starmod: registered module",
			"module", moduleName,
			"has_check", checkFns[name] != nil,
			"has_revert", revertFns[name] != nil,
			"has_spec", spec != nil,
		)
		count++
	}

	return count, nil
}

// registerStarModule installs a Starlark module's builder and, when available,
// its self-documentation spec into the state Registry, and records the fresh
// spec in the loader's own store.
//
// The state Registry's RegisterSpec is STRICT (a duplicate module name is an
// error) and has no replace API, whereas Starlark registration is inherently
// dynamic: a hot-reload re-registers the same name, and a formula module
// overrides a global one of the same name. On such a duplicate we fall back to
// plain Register so the builder-override and hot-reload behaviors are preserved
// exactly as before. The loader's own specs map is always refreshed, so
// LoadedSpec (and any consumer that reads it) sees the latest docs even though
// Registry.Describe cannot be refreshed through the current public API — see the
// FRAMEWORK DEVIATION reported for this track.
func (l *Loader) registerStarModule(registry *state.Registry, moduleName string, spec *modschema.Spec, inner state.Builder) {
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
	l.mu.Unlock()
}

// validatingBuilder wraps a Starlark builder so that, when the module declared
// its parameters (PARAMS / <fn>_params), each construction runs the compiled
// schema's Decode purely to surface unknown-key warnings through the configured
// DecodeOptions. The decoded proto is discarded — the Starlark module reads the
// raw config — and a type/required mismatch is logged at debug, never fatal
// (the module is dynamically typed).
func (l *Loader) validatingBuilder(spec *modschema.Spec, inner state.Builder) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if _, err := spec.Decode(id, config, spec.NewParams(), l.decodeOpts()); err != nil {
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
