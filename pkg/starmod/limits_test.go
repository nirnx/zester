package starmod_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/starmod"
	"github.com/nirnx/zester/pkg/state"
	"go.starlark.net/starlark"
)

// spinModule is an "infinite" pure-Starlark loop (the default FileOptions
// reject `while`, and 2^62 iterations outlive any test) with no builtin call
// inside it — exactly the shape that used to wedge the peel's serialized exec
// worker, because nothing ever consulted the "context" thread-local.
const spinModule = `
def spin(id, config):
    for _ in range(1 << 62):
        pass
    return {"changed": True}

def spin_check(id, config):
    for _ in range(1 << 62):
        pass
    return {"needs_change": True}

def spin_revert(id, config):
    for _ in range(1 << 62):
        pass
    return {"changed": True}
`

// loadSpin loads spinModule through a Loader configured with maxSteps and
// returns the built state.
func loadSpin(t *testing.T, maxSteps uint64) state.State {
	t.Helper()
	dir := t.TempDir()
	writeStarFile(t, filepath.Join(dir, "_modules"), "hang.star", spinModule)

	_, mctx := testLoader(t, dir)
	loader := starmod.NewLoader(starmod.LoaderConfig{
		StatesDir:         dir,
		ModuleContext:     mctx,
		MaxExecutionSteps: maxSteps, // 0 = DefaultMaxExecutionSteps
	})
	registry := state.NewRegistry()
	if _, err := loader.LoadGlobal(registry); err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	s, err := registry.Build("hang.spin", "test", map[string]any{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return s
}

// callWithin runs fn and fails the test when it has not returned within limit:
// a hung Starlark call must never block the caller indefinitely.
func callWithin(t *testing.T, limit time.Duration, fn func() error) error {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- fn() }()
	select {
	case err := <-errCh:
		return err
	case <-time.After(limit):
		t.Fatalf("Starlark call did not return within %s", limit)
		return nil
	}
}

// TestStarlarkState_ContextCancellationInterruptsLoop pins fix (2a): a
// Check/Apply/Revert stuck in a pure-Starlark loop returns promptly with an
// error once the execution context is cancelled (job cancel / timeout). The
// step budget is set effectively unlimited so that ONLY the cancellation
// watcher can stop the loop (the 50M default is exhausted in ~100ms on a
// modern machine, which would mask a broken watcher).
func TestStarlarkState_ContextCancellationInterruptsLoop(t *testing.T) {
	s := loadSpin(t, 1<<62)

	calls := map[string]func(ctx context.Context) error{
		"apply": func(ctx context.Context) error { _, err := s.Apply(ctx); return err },
		"check": func(ctx context.Context) error { _, err := s.Check(ctx); return err },
		"revert": func(ctx context.Context) error {
			_, err := s.Revert(ctx)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			start := time.Now()
			err := callWithin(t, 5*time.Second, func() error { return call(ctx) })
			if err == nil {
				t.Fatal("expected an error from the cancelled call, got nil")
			}
			if !strings.Contains(err.Error(), "cancel") {
				t.Errorf("error = %q, want it to mention cancellation", err)
			}
			if !strings.Contains(err.Error(), "hang.spin") {
				t.Errorf("error = %q, want it to name the module", err)
			}
			if !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
				t.Errorf("error = %q, want it to carry the context error", err)
			}
			if elapsed := time.Since(start); elapsed > 3*time.Second {
				t.Errorf("call took %s after a 200ms deadline; cancellation was not prompt", elapsed)
			}
		})
	}
}

// TestStarlarkState_AlreadyCancelledContext verifies a context that is dead
// before the call starts still fails the call (no stuck first instruction).
func TestStarlarkState_AlreadyCancelledContext(t *testing.T) {
	s := loadSpin(t, 1<<62)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := callWithin(t, 5*time.Second, func() error { _, err := s.Apply(ctx); return err })
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("error = %v, want a cancellation error", err)
	}
}

// TestStarlarkState_StepLimitExceeded pins fix (2b) on the run path: a small
// configured MaxExecutionSteps fails the loop with an error naming the step
// limit, even though the context is never cancelled.
func TestStarlarkState_StepLimitExceeded(t *testing.T) {
	const limit = 10_000
	s := loadSpin(t, limit)

	err := callWithin(t, 5*time.Second, func() error { _, err := s.Apply(context.Background()); return err })
	if err == nil {
		t.Fatal("expected a step-limit error, got nil")
	}
	for _, want := range []string{"step limit", "10000", "hang.spin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
	var evalErr *starlark.EvalError
	if !errors.As(err, &evalErr) {
		t.Errorf("error = %T, want a wrapped *starlark.EvalError", err)
	}
}

// TestLoader_StepLimitAtLoadTime pins fix (2b) at load time: top-level module
// code that loops forever fails ONLY that file (skipped with the step-limit
// error), other files still register, and peel startup is not hung. A
// load()ed helper that spins at its top level fails its importer the same way.
func TestLoader_StepLimitAtLoadTime(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	writeStarFile(t, modulesDir, "good.star", `
def hello(id, config):
    return {"changed": False}
`)
	// Default FileOptions reject top-level control flow, so the spin is a
	// helper function CALLED at the top level — still top-level module code.
	writeStarFile(t, modulesDir, "spin_top.star", `
def _spin():
    for _ in range(1 << 62):
        pass

_spin()

def never(id, config):
    return {"changed": True}
`)
	writeStarFile(t, modulesDir, "spin_helper.star", `
def _spin():
    for _ in range(1 << 62):
        pass

_spin()
HELPER = 1
`)
	writeStarFile(t, modulesDir, "imports_spinner.star", `
load("spin_helper.star", "HELPER")

def uses_helper(id, config):
    return {"changed": HELPER == 1}
`)

	_, mctx := testLoader(t, dir)
	loader := starmod.NewLoader(starmod.LoaderConfig{
		StatesDir:         dir,
		ModuleContext:     mctx,
		MaxExecutionSteps: 10_000,
	})
	registry := state.NewRegistry()

	var count int
	var err error
	done := make(chan struct{})
	go func() {
		count, err = loader.LoadGlobal(registry)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("LoadGlobal hung on a spinning top-level module")
	}

	if err == nil || !strings.Contains(err.Error(), "step limit") {
		t.Errorf("LoadGlobal error = %v, want a step-limit error", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (only good.hello registers)", count)
	}
	if _, err := registry.Build("good.hello", "test", map[string]any{}); err != nil {
		t.Errorf("good.hello: %v", err)
	}
	if registry.Has("spin_top.never") {
		t.Error("spin_top.never registered despite its file exceeding the step limit")
	}
	if registry.Has("imports_spinner.uses_helper") {
		t.Error("imports_spinner.uses_helper registered despite its load()ed helper exceeding the step limit")
	}
}

// TestStarlarkState_DefaultBudgetAllowsNormalModules verifies that fix (2b)
// does not punish legitimate work: a module doing a real (bounded, six-figure)
// loop completes under the default budget, both through the Loader (zero config
// = default) and through NewStarlarkBuilder without options; and the default
// is the documented 50 million.
func TestStarlarkState_DefaultBudgetAllowsNormalModules(t *testing.T) {
	if starmod.DefaultMaxExecutionSteps != 50_000_000 {
		t.Fatalf("DefaultMaxExecutionSteps = %d, want 50000000", starmod.DefaultMaxExecutionSteps)
	}

	const busy = `
def sum_up(id, config):
    total = 0
    for i in range(200000):
        total += i
    return {"changed": True, "diff": str(total)}
`
	dir := t.TempDir()
	writeStarFile(t, filepath.Join(dir, "_modules"), "work.star", busy)
	loader, mctx := testLoader(t, dir)
	registry := state.NewRegistry()
	if _, err := loader.LoadGlobal(registry); err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	s, err := registry.Build("work.sum_up", "test", map[string]any{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res, err := s.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply via loader defaults: %v", err)
	}
	if !res.Changed || res.Diff != "19999900000" {
		t.Errorf("result = %+v, want changed with the loop's sum", res)
	}

	globals := parseStarlarkFunctions(t, mctx, busy)
	builder := starmod.NewStarlarkBuilder("work.sum_up", globals["sum_up"].(*starlark.Function), nil, nil, nil, mctx)
	direct, err := builder("test", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := direct.Apply(context.Background()); err != nil {
		t.Fatalf("Apply via option-less builder: %v", err)
	}
}
