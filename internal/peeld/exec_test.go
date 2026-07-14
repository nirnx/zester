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
	"github.com/nirnx/zester/pkg/starmod"
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

// TestExecModule_StatesDirSwitchReloadsStarlark pins the round-5 P1 call-site
// sequence in execModule's dir-switch branch: purge the OLD loader's
// registrations (UnloadAll) BEFORE constructing the replacement loader, then
// LoadGlobal the new tree. Without the purge — or with it misordered after the
// `a.starLoader = NewLoader(...)` assignment — the fresh loader's empty
// ownership ledger shadow-refuses every previously loaded Starlark name, so
// the OLD builder stays live and this test's post-switch diff assertion fails.
func TestExecModule_StatesDirSwitchReloadsStarlark(t *testing.T) {
	a := newTestAgent(t)
	tmp := t.TempDir()
	baked := filepath.Join(tmp, "baked")
	cache := filepath.Join(tmp, "cache") // empty at boot: baked wins
	a.cfg.StatesCache = cache
	a.bakedStatesDir = baked
	a.client = &bus.Client{}
	a.mctx = exec.NewModuleContext(&exec.ProviderSet{}, map[string]any{}, nil, discardLogger())

	writeStar := func(root, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, "_modules"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "_modules", "app.star"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeStar(baked, "def deployed(id, config):\n    return {\"changed\": False, \"diff\": \"old-tree\"}\n")

	// Boot-equivalent sequence (agent.go local phase): engine + loader over
	// the baked tree.
	a.setupStatesEngine()
	if a.effectiveStatesDir != baked {
		t.Fatalf("effectiveStatesDir = %q, want baked %q", a.effectiveStatesDir, baked)
	}
	a.starLoader = starmod.NewLoader(starmod.LoaderConfig{
		StatesDir:     a.effectiveStatesDir,
		ModuleContext: a.mctx,
		Logger:        discardLogger(),
		DecodeOptions: a.decodeOpts,
	})
	if _, err := a.starLoader.LoadGlobal(a.registry); err != nil {
		t.Fatal(err)
	}
	if !a.registry.Has("app.deployed") {
		t.Fatal("boot loader did not register app.deployed")
	}

	// The KV cache fills (connected-phase sync) with a DIFFERENT app.star; the
	// next execution's dir-resolution switches to it.
	writeStar(cache, "def deployed(id, config):\n    return {\"changed\": False, \"diff\": \"new-tree\"}\n")

	resp, err := a.execModule(context.Background(), proto.ExecRequest{Module: "test.echo", ID: "hi"})
	if err != nil || resp.Error != "" {
		t.Fatalf("switch-triggering exec: err=%v respErr=%q", err, resp.Error)
	}
	if a.effectiveStatesDir != cache {
		t.Fatalf("effectiveStatesDir = %q, want cache %q — switch did not happen", a.effectiveStatesDir, cache)
	}

	s, err := a.registry.Build("app.deployed", "x", map[string]any{})
	if err != nil {
		t.Fatalf("app.deployed unregistered after the switch (shadow refusal — UnloadAll missing/misordered): %v", err)
	}
	r, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Diff != "new-tree" {
		t.Errorf("post-switch diff = %q, want new-tree (the NEW tree's builder must be live)", r.Diff)
	}
}

// TestStateApplyBareIsHighstate pins the Salt-parity alias on the
// authoritative dispatch path: a state.apply request WITHOUT a 'state' arg —
// from any producer (CLI, REST API, reactor action, scheduler entry) — runs
// the full highstate instead of erroring, identically to an explicit
// state.highstate request. An explicit state still selects only that tree.
func TestStateApplyBareIsHighstate(t *testing.T) {
	a := newTestAgent(t)
	tmp := t.TempDir()
	cache := filepath.Join(tmp, "cache")
	a.cfg.StatesCache = cache
	a.bakedStatesDir = filepath.Join(tmp, "baked")
	a.client = &bus.Client{}
	a.mctx = exec.NewModuleContext(&exec.ProviderSet{}, map[string]any{}, nil, discardLogger())

	// Engine setup runs BEFORE the cache exists (the proven lazy-build
	// ordering from TestExecModuleLazyStatesEngine): the first execution below
	// builds the engine AND the Starlark loader from the freshly written tree.
	a.setupStatesEngine()

	// A state top file matching every peel, plus the ping tree it references.
	if err := os.MkdirAll(filepath.Join(cache, "ping"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "top.zy"),
		[]byte("base:\n  '*':\n    - ping\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "ping", "init.zy"),
		[]byte("ping-it:\n  test.ping:\n    - name: ping-it\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Bare state.apply (nil args) == highstate: succeeds and runs the
	// top-file-matched tree.
	bare, err := a.execModule(context.Background(), proto.ExecRequest{Module: "state.apply"})
	if err != nil {
		t.Fatal(err)
	}
	if bare.Error != "" || !bare.Success {
		t.Fatalf("bare state.apply: success=%v error=%q, want highstate alias to run", bare.Success, bare.Error)
	}

	// Identical to the explicit form (same compiled state set).
	hs, err := a.execModule(context.Background(), proto.ExecRequest{Module: "state.highstate"})
	if err != nil {
		t.Fatal(err)
	}
	if hs.Error != "" || !hs.Success {
		t.Fatalf("state.highstate: success=%v error=%q", hs.Success, hs.Error)
	}
	if len(bare.Results) != len(hs.Results) {
		t.Fatalf("bare state.apply ran %d states, state.highstate ran %d — alias diverged", len(bare.Results), len(hs.Results))
	}
	for i := range bare.Results {
		if bare.Results[i].Name != hs.Results[i].Name {
			t.Errorf("result %d: bare=%q highstate=%q", i, bare.Results[i].Name, hs.Results[i].Name)
		}
	}

	// An empty-string 'state' arg aliases too (the CLI's `state.apply state=`
	// and older producers sending the zero value).
	empty, err := a.execModule(context.Background(),
		proto.ExecRequest{Module: "state.apply", Args: map[string]any{"state": ""}})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Error != "" || !empty.Success || len(empty.Results) != len(hs.Results) {
		t.Fatalf("state.apply state=\"\": success=%v error=%q results=%d, want the highstate alias", empty.Success, empty.Error, len(empty.Results))
	}

	// Control: an explicit state reference still compiles ONLY that tree.
	one, err := a.execModule(context.Background(),
		proto.ExecRequest{Module: "state.apply", Args: map[string]any{"state": "ping"}})
	if err != nil {
		t.Fatal(err)
	}
	if one.Error != "" || !one.Success || len(one.Results) != 1 {
		t.Fatalf("state.apply ping: success=%v error=%q results=%d, want exactly the named tree", one.Success, one.Error, len(one.Results))
	}

	// Salt's kwarg name (and what pre-fix reactor masters emit for
	// `dispatch.state: {sls: ...}`): args["mods"] selects the named tree, it
	// must NEVER alias to the highstate.
	mods, err := a.execModule(context.Background(),
		proto.ExecRequest{Module: "state.apply", Args: map[string]any{"mods": "ping"}})
	if err != nil {
		t.Fatal(err)
	}
	if mods.Error != "" || !mods.Success || len(mods.Results) != 1 {
		t.Fatalf("state.apply mods=ping: success=%v error=%q results=%d, want exactly the named tree", mods.Success, mods.Error, len(mods.Results))
	}

	// Producers that carry the state only in the request ID (reactor
	// `local.state.apply` sugar, `dispatch.module` state_id, REST state_id):
	// the ID selects the named tree.
	byID, err := a.execModule(context.Background(),
		proto.ExecRequest{Module: "state.apply", ID: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if byID.Error != "" || !byID.Success || len(byID.Results) != 1 {
		t.Fatalf("state.apply ID=ping: success=%v error=%q results=%d, want exactly the named tree", byID.Success, byID.Error, len(byID.Results))
	}

	// The literal ID "highstate" is the module's own synthetic alias ID (the
	// CLI bare form ships it) — it must go to the highstate branch, never be
	// compiled as a tree named "highstate".
	synth, err := a.execModule(context.Background(),
		proto.ExecRequest{Module: "state.apply", ID: "highstate"})
	if err != nil {
		t.Fatal(err)
	}
	if synth.Error != "" || !synth.Success || len(synth.Results) != len(hs.Results) {
		t.Fatalf("state.apply ID=highstate: success=%v error=%q results=%d, want the highstate alias (%d)", synth.Success, synth.Error, len(synth.Results), len(hs.Results))
	}
}
