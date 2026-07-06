package peeld

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/proto"
)

// TestResolveStatesDirWith covers the C2 retry helper: a ReadDir landing in
// the state-file cache's atomic-swap window (ENOENT or transiently empty)
// must be retried before falling back to the baked dir.
func TestResolveStatesDirWith(t *testing.T) {
	// Real DirEntry values for the "populated" result.
	populated := t.TempDir()
	if err := os.WriteFile(filepath.Join(populated, "x.zy"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(populated)
	if err != nil || len(entries) == 0 {
		t.Fatalf("test setup: %v", err)
	}

	t.Run("transient ENOENT is retried, cache wins", func(t *testing.T) {
		calls, sleeps := 0, 0
		readDir := func(string) ([]os.DirEntry, error) {
			calls++
			if calls < 3 {
				return nil, os.ErrNotExist // mid-swap window
			}
			return entries, nil
		}
		dir, fellBack := resolveStatesDirWith("/cache", "/baked", 5, time.Millisecond,
			readDir, func(time.Duration) { sleeps++ })
		if fellBack || dir != "/cache" {
			t.Fatalf("dir=%q fellBack=%v, want /cache false", dir, fellBack)
		}
		if calls != 3 || sleeps != 2 {
			t.Errorf("calls=%d sleeps=%d, want 3/2", calls, sleeps)
		}
	})

	t.Run("transient empty result is retried", func(t *testing.T) {
		calls := 0
		readDir := func(string) ([]os.DirEntry, error) {
			calls++
			if calls == 1 {
				return nil, nil // exists but momentarily empty
			}
			return entries, nil
		}
		dir, fellBack := resolveStatesDirWith("/cache", "/baked", 5, time.Millisecond,
			readDir, func(time.Duration) {})
		if fellBack || dir != "/cache" {
			t.Fatalf("dir=%q fellBack=%v, want /cache false", dir, fellBack)
		}
	})

	t.Run("persistently empty falls back after all attempts", func(t *testing.T) {
		calls, sleeps := 0, 0
		readDir := func(string) ([]os.DirEntry, error) {
			calls++
			return nil, os.ErrNotExist
		}
		dir, fellBack := resolveStatesDirWith("/cache", "/baked", 5, time.Millisecond,
			readDir, func(time.Duration) { sleeps++ })
		if !fellBack || dir != "/baked" {
			t.Fatalf("dir=%q fellBack=%v, want /baked true", dir, fellBack)
		}
		if calls != 5 || sleeps != 4 {
			t.Errorf("calls=%d sleeps=%d, want 5/4", calls, sleeps)
		}
	})

	t.Run("populated cache returns immediately without sleeping", func(t *testing.T) {
		sleeps := 0
		dir, fellBack := resolveStatesDirWith("/cache", "/baked", 5, time.Millisecond,
			func(string) ([]os.DirEntry, error) { return entries, nil },
			func(time.Duration) { sleeps++ })
		if fellBack || dir != "/cache" || sleeps != 0 {
			t.Fatalf("dir=%q fellBack=%v sleeps=%d, want /cache false 0", dir, fellBack, sleeps)
		}
	})
}

// TestExecModuleLazyStatesEngine covers C4: a KV-only deployment with no
// baked states dir must boot with a nil states engine (non-fatal), return a
// clear error for state compilation (exec worker AND scheduler paths), and
// build the engine lazily once state files appear in the cache.
func TestExecModuleLazyStatesEngine(t *testing.T) {
	a := newTestAgent(t)
	tmp := t.TempDir()
	cache := filepath.Join(tmp, "cache") // does not exist yet (pre-sync)
	a.cfg.StatesCache = cache
	a.bakedStatesDir = filepath.Join(tmp, "baked") // never exists (KV-only image)
	a.client = &bus.Client{}                       // JetStream() only feeds the (unused) basket fn
	a.mctx = exec.NewModuleContext(&exec.ProviderSet{}, map[string]any{}, nil, discardLogger())

	// Boot-equivalent (Run local phase): no states dir anywhere is NOT fatal.
	a.setupStatesEngine()
	if a.statesEng != nil {
		t.Fatal("states engine built with no states dirs present")
	}
	if a.effectiveStatesDir != "" {
		t.Fatalf("effectiveStatesDir = %q, want empty for lazy rebuild", a.effectiveStatesDir)
	}

	// State compilation against the nil engine returns the clear error.
	resp, err := a.execModule(context.Background(), proto.ExecRequest{Module: "state.highstate"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != statesEngineUnavailableMsg {
		t.Fatalf("error = %q, want %q", resp.Error, statesEngineUnavailableMsg)
	}

	// The scheduler path tolerates it too (clear error, no panic).
	if res := a.schedExec(context.Background(), "state.apply", map[string]any{"state": "ping"}); res.Error != statesEngineUnavailableMsg {
		t.Fatalf("scheduler error = %q, want %q", res.Error, statesEngineUnavailableMsg)
	}

	// Renderer guard: nil engine fails a template render with an error, not a
	// nil-pointer panic.
	if _, rErr := a.renderStateTemplate("t", "{{ 1 }}", nil); rErr == nil {
		t.Error("renderStateTemplate on nil engine did not error")
	}

	// State files appear (connected-phase cache sync): the engine builds
	// lazily on the next execution and the state applies.
	if err := os.MkdirAll(filepath.Join(cache, "ping"), 0o755); err != nil {
		t.Fatal(err)
	}
	stateFile := "ping-it:\n  test.ping:\n    - name: ping-it\n"
	if err := os.WriteFile(filepath.Join(cache, "ping", "init.zy"), []byte(stateFile), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err = a.execModule(context.Background(), proto.ExecRequest{Module: "state.apply", Args: map[string]any{"state": "ping"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != "" || !resp.Success {
		t.Fatalf("state.apply after cache fill: success=%v error=%q results=%+v", resp.Success, resp.Error, resp.Results)
	}
	if a.statesEng == nil || a.effectiveStatesDir != cache {
		t.Errorf("engine not switched to cache dir: eng=%v dir=%q", a.statesEng != nil, a.effectiveStatesDir)
	}
}

// TestSettingsForExec covers the fail-closed decision logic for state
// execution settings (finding 29).
func TestSettingsForExec(t *testing.T) {
	fresh := map[string]any{"key": "fresh"}
	cached := map[string]any{"key": "cached"}
	resolveErr := errors.New("kv unavailable")

	t.Run("resolve success uses fresh settings", func(t *testing.T) {
		got, stale, err := settingsForExec(fresh, nil, cached)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stale {
			t.Error("stale = true, want false")
		}
		if got["key"] != "fresh" {
			t.Errorf("settings = %v, want fresh", got)
		}
	})

	t.Run("resolve success with empty settings is not an error", func(t *testing.T) {
		got, stale, err := settingsForExec(nil, nil, cached)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stale {
			t.Error("stale = true, want false")
		}
		if got != nil {
			t.Errorf("settings = %v, want nil (legitimately empty)", got)
		}
	})

	t.Run("resolve failure falls back to cached", func(t *testing.T) {
		got, stale, err := settingsForExec(nil, resolveErr, cached)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !stale {
			t.Error("stale = false, want true")
		}
		if got["key"] != "cached" {
			t.Errorf("settings = %v, want cached", got)
		}
	})

	t.Run("resolve failure without cache fails closed", func(t *testing.T) {
		got, stale, err := settingsForExec(nil, resolveErr, nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, resolveErr) {
			t.Errorf("error %v does not wrap resolve error", err)
		}
		if got != nil || stale {
			t.Errorf("got settings=%v stale=%v, want nil/false", got, stale)
		}
	})
}
