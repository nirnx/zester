package peeld

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nirnx/zester/internal/config"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/proto"
)

// startupStatesRequests: valid forms expand to exec requests, every invalid
// combination fails LOUDLY (boot-fatal) — a typo'd value must never silently
// skip the boot-time apply.
func TestStartupStatesRequests(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		reqs, err := startupStatesRequests(&config.PeelConfig{})
		if err != nil || reqs != nil {
			t.Fatalf("reqs=%v err=%v, want nil/nil", reqs, err)
		}
	})

	t.Run("highstate", func(t *testing.T) {
		reqs, err := startupStatesRequests(&config.PeelConfig{StartupStates: "highstate"})
		if err != nil {
			t.Fatal(err)
		}
		if len(reqs) != 1 || reqs[0].Module != "state.highstate" {
			t.Fatalf("reqs = %+v, want one state.highstate request", reqs)
		}
	})

	t.Run("sls list in order", func(t *testing.T) {
		reqs, err := startupStatesRequests(&config.PeelConfig{
			StartupStates:  "sls",
			StartupSLSList: []string{"common", "webserver.tls"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(reqs) != 2 {
			t.Fatalf("reqs = %+v, want 2", reqs)
		}
		for i, want := range []string{"common", "webserver.tls"} {
			if reqs[i].Module != "state.apply" || reqs[i].Args["state"] != want {
				t.Errorf("req %d = %+v, want state.apply %q", i, reqs[i], want)
			}
		}
	})

	for name, cfg := range map[string]config.PeelConfig{
		"unknown value":             {StartupStates: "top"},
		"sls without list":          {StartupStates: "sls"},
		"list without sls":          {StartupSLSList: []string{"common"}},
		"highstate with list":       {StartupStates: "highstate", StartupSLSList: []string{"common"}},
		"empty ref in list":         {StartupStates: "sls", StartupSLSList: []string{"common", " "}},
		"capitalized (not lenient)": {StartupStates: "Highstate"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := startupStatesRequests(&cfg); err == nil {
				t.Fatalf("config %+v did not error", cfg)
			}
		})
	}
}

// TestStartupNotReady pins the retry classifier against the EXACT response
// errors the exec path produces: the two infrastructure-not-ready shapes retry
// (both built from shared constants — rewording either would silently turn a
// bootstrap wait into a terminal answer), everything else is terminal.
func TestStartupNotReady(t *testing.T) {
	retry := []proto.ExecResponse{
		{Error: statesEngineUnavailableMsg},
		{Error: settingsResolveFailedPrefix + "resolve: no NATS connection"},
	}
	for _, r := range retry {
		if !startupNotReady(r) {
			t.Errorf("startupNotReady(%q) = false, want retry", r.Error)
		}
	}

	terminal := []proto.ExecResponse{
		{}, // success
		{Error: "highstate: no states matched in top.zy"},
		{Error: "compile state: read x/init.zy: no such file or directory"},
		{Error: "settings resolution failed"}, // prefix WITHOUT the trailing space+detail
		{Error: "peel busy: execution queue full"},
	}
	for _, r := range terminal {
		if startupNotReady(r) {
			t.Errorf("startupNotReady(%q) = true, want terminal", r.Error)
		}
	}
}

// recordingLogHandler is a minimal slog.Handler capturing message strings —
// the seam the retry test uses to assert a not-ready attempt ACTUALLY
// happened (a timing-based sleep could silently degrade to the zero-retry
// success case).
type recordingLogHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *recordingLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	return nil
}
func (h *recordingLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingLogHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingLogHandler) has(msg string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.msgs {
		if strings.Contains(m, msg) {
			return true
		}
	}
	return false
}

// newStartupTestAgent builds a test agent with an (optional) pre-published
// states cache. The sync grace is shrunk to a millisecond: unit tests never
// run the connected phase, so the gate opens via the offline grace path
// unless a test closes the sync channel itself.
func newStartupTestAgent(t *testing.T, withStates bool) *Agent {
	t.Helper()
	a := newTestAgent(t)
	tmp := t.TempDir()
	cache := filepath.Join(tmp, "cache")
	a.cfg.StatesCache = cache
	a.bakedStatesDir = filepath.Join(tmp, "baked")
	a.client = &bus.Client{}
	a.mctx = exec.NewModuleContext(&exec.ProviderSet{}, map[string]any{}, nil, discardLogger())
	a.startupRetryBase = time.Millisecond
	a.startupRetryCap = 5 * time.Millisecond
	a.startupSyncGrace = time.Millisecond
	a.setupStatesEngine()
	if withStates {
		writeStatesTree(t, cache, "ping")
	}
	return a
}

// writeStatesTree ATOMICALLY publishes a states tree (top.zy targeting every
// peel + one test.ping tree per ref) at dir: the tree is staged in a sibling
// temp dir and renamed into place, mirroring the production cache's atomic
// swap — the exec path's directory prober must never observe a partial tree
// (a partial tree compiles to a TERMINAL error, not a retried one).
func writeStatesTree(t *testing.T, dir string, refs ...string) {
	t.Helper()
	stage, err := os.MkdirTemp(filepath.Dir(dir), ".stage-*")
	if err != nil {
		t.Fatal(err)
	}
	top := "base:\n  '*':\n"
	for _, ref := range refs {
		top += "    - " + ref + "\n"
		if err := os.MkdirAll(filepath.Join(stage, ref), 0o755); err != nil {
			t.Fatal(err)
		}
		state := fmt.Sprintf("%s-it:\n  test.ping:\n    - name: %s-it\n", ref, ref)
		if err := os.WriteFile(filepath.Join(stage, ref, "init.zy"), []byte(state), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stage, "top.zy"), []byte(top), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stage, dir); err != nil {
		t.Fatal(err)
	}
}

// The highstate form applies the top-file-matched tree exactly once.
func TestRunStartupStates_Highstate(t *testing.T) {
	a := newStartupTestAgent(t, true)
	reqs, err := startupStatesRequests(&config.PeelConfig{StartupStates: "highstate"})
	if err != nil {
		t.Fatal(err)
	}

	resps := a.runStartupStates(context.Background(), reqs)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	r := resps[0]
	if r.Error != "" || !r.Success || len(r.Results) != 1 {
		t.Fatalf("startup highstate: success=%v error=%q results=%d", r.Success, r.Error, len(r.Results))
	}
}

// The sls form applies each listed ref in the configured order — the two
// refs are DISTINCT trees so the per-response result names pin the order
// observably (identical refs would make any order pass).
func TestRunStartupStates_SLSList(t *testing.T) {
	a := newStartupTestAgent(t, false)
	writeStatesTree(t, a.cfg.StatesCache, "ping", "pong")
	reqs, err := startupStatesRequests(&config.PeelConfig{
		StartupStates:  "sls",
		StartupSLSList: []string{"pong", "ping"},
	})
	if err != nil {
		t.Fatal(err)
	}

	resps := a.runStartupStates(context.Background(), reqs)
	if len(resps) != 2 {
		t.Fatalf("got %d responses, want 2", len(resps))
	}
	for i, wantState := range []string{"pong-it", "ping-it"} {
		r := resps[i]
		if r.Error != "" || !r.Success || len(r.Results) != 1 {
			t.Fatalf("startup sls %d: success=%v error=%q results=%d", i, r.Success, r.Error, len(r.Results))
		}
		if !strings.Contains(r.Results[0].Name, wantState) {
			t.Errorf("startup sls %d ran %q, want the %q tree (list order)", i, r.Results[0].Name, wantState)
		}
	}
}

// The bootstrap case: the peel boots BEFORE any state files exist (no
// engine), runStartupStates retries, the state-file publish lands, and the
// apply converges — no restart, no operator dispatch. The tree is written
// only AFTER a not-ready retry is OBSERVED (via the log seam), so the test
// provably exercises the retry arm instead of degrading to the plain-success
// case under scheduling delay.
func TestRunStartupStates_RetriesUntilStatesArrive(t *testing.T) {
	a := newStartupTestAgent(t, false) // no states yet — engine unavailable
	rec := &recordingLogHandler{}
	a.logger = slog.New(rec)
	reqs, err := startupStatesRequests(&config.PeelConfig{StartupStates: "highstate"})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan []proto.ExecResponse, 1)
	go func() { done <- a.runStartupStates(context.Background(), reqs) }()

	// Wait until at least one not-ready attempt has actually happened.
	deadline := time.Now().Add(10 * time.Second)
	for !rec.has("startup states: peel not ready yet, will retry") {
		if time.Now().After(deadline) {
			t.Fatal("no not-ready retry was ever logged")
		}
		time.Sleep(2 * time.Millisecond)
	}
	// NOW publish the tree (atomically) — the next attempt must converge.
	writeStatesTree(t, a.cfg.StatesCache, "ping")

	select {
	case resps := <-done:
		if len(resps) != 1 || resps[0].Error != "" || !resps[0].Success {
			t.Fatalf("startup states after retry: %+v", resps)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runStartupStates never converged after state files appeared")
	}
}

// The first-sync gate: with a populated (stale) BAKED tree the engine builds
// immediately, but the startup run must WAIT for the first state-file sync
// and then apply the FRESH cache tree — never terminal-fail against, or
// silently apply, the pre-sync baked copy.
func TestRunStartupStates_WaitsForFirstSync(t *testing.T) {
	a := newStartupTestAgent(t, false)
	a.startupSyncGrace = time.Minute // the gate must open via the sync signal
	// The baked fallback holds a STALE layout: the 'stale' tree only.
	writeStatesTree(t, a.bakedStatesDir, "stale")
	// The configured startup ref exists ONLY in the fresh (post-sync) tree.
	reqs, err := startupStatesRequests(&config.PeelConfig{
		StartupStates:  "sls",
		StartupSLSList: []string{"fresh"},
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan []proto.ExecResponse, 1)
	go func() { done <- a.runStartupStates(context.Background(), reqs) }()

	// Gate closed: nothing may run against the baked tree.
	select {
	case resps := <-done:
		t.Fatalf("startup states ran before the first sync: %+v", resps)
	case <-time.After(100 * time.Millisecond):
	}

	// The first sync lands: the cache now holds the fresh tree.
	writeStatesTree(t, a.cfg.StatesCache, "fresh")
	a.markStatesSynced()

	select {
	case resps := <-done:
		if len(resps) != 1 || resps[0].Error != "" || !resps[0].Success {
			t.Fatalf("startup states after sync: %+v", resps)
		}
		if !strings.Contains(resps[0].Results[0].Name, "fresh-it") {
			t.Errorf("ran %q, want the fresh cache tree", resps[0].Results[0].Name)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runStartupStates never ran after the first sync")
	}
}

// Offline-first: when no sync ever lands (NATS down), the grace expires and
// the run proceeds with the local (baked) tree.
func TestRunStartupStates_GraceProceedsWithLocalTree(t *testing.T) {
	a := newStartupTestAgent(t, false)
	writeStatesTree(t, a.bakedStatesDir, "baked")
	reqs, err := startupStatesRequests(&config.PeelConfig{
		StartupStates:  "sls",
		StartupSLSList: []string{"baked"},
	})
	if err != nil {
		t.Fatal(err)
	}

	resps := a.runStartupStates(context.Background(), reqs)
	if len(resps) != 1 || resps[0].Error != "" || !resps[0].Success {
		t.Fatalf("offline startup states: %+v", resps)
	}
	if !strings.Contains(resps[0].Results[0].Name, "baked-it") {
		t.Errorf("ran %q, want the baked tree (offline-first)", resps[0].Results[0].Name)
	}
}

// A genuine ANSWER — here: nothing matches in top.zy — is reported once and
// never retried (only infrastructure-not-ready responses retry).
func TestRunStartupStates_NoMatchIsTerminal(t *testing.T) {
	a := newStartupTestAgent(t, true)
	// Rewrite the top file so nothing targets this peel.
	if err := os.WriteFile(filepath.Join(a.cfg.StatesCache, "top.zy"),
		[]byte("base:\n  'no-such-peel':\n    - ping\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reqs, err := startupStatesRequests(&config.PeelConfig{StartupStates: "highstate"})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan []proto.ExecResponse, 1)
	go func() { done <- a.runStartupStates(context.Background(), reqs) }()
	select {
	case resps := <-done:
		if len(resps) != 1 || resps[0].Error == "" {
			t.Fatalf("no-match run = %+v, want the terminal no-states-matched error", resps)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runStartupStates retried a terminal response")
	}
}

// Context cancellation stops the retry loop (peel shutdown mid-bootstrap).
func TestRunStartupStates_CancelStopsRetry(t *testing.T) {
	a := newStartupTestAgent(t, false) // engine never becomes available
	a.startupRetryBase = time.Hour     // park the loop in its backoff wait
	reqs, _ := startupStatesRequests(&config.PeelConfig{StartupStates: "highstate"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []proto.ExecResponse, 1)
	go func() { done <- a.runStartupStates(ctx, reqs) }()
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case resps := <-done:
		if len(resps) != 0 {
			t.Fatalf("cancelled run returned %+v, want none", resps)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runStartupStates did not stop on context cancellation")
	}
}
