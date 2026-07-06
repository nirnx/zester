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
	"github.com/nirnx/zester/pkg/state"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// LoaderConfig holds the configuration for the Starlark module loader.
type LoaderConfig struct {
	StatesDir     string              // root states directory
	ModuleContext *exec.ModuleContext // execution providers for builtins
	Logger        *slog.Logger
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
	}
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

		registry.Register(moduleName, builder)
		l.config.Logger.Info("starmod: registered module",
			"module", moduleName,
			"has_check", checkFns[name] != nil,
			"has_revert", revertFns[name] != nil,
		)
		count++
	}

	return count, nil
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
