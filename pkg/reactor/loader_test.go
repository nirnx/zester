package reactor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
)

// syncBuffer is a goroutine-safe bytes.Buffer for slog capture.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// publishFiles writes the settings-shaped manifest protocol into kv:
// file keys, then _manifest, then a _revision bump.
func publishFiles(t *testing.T, kv bus.KV, files map[string]string) {
	t.Helper()
	ctx := context.Background()
	entries := make([]manifestEntry, 0, len(files))
	for key, content := range files {
		if _, err := kv.Put(ctx, key, []byte(content)); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
		sum := sha256.Sum256([]byte(content))
		entries = append(entries, manifestEntry{Key: key, SHA256: hex.EncodeToString(sum[:])})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	data, err := bus.Encode(entries)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	if _, err := kv.Put(ctx, KeyManifest, data); err != nil {
		t.Fatalf("put manifest: %v", err)
	}
	if err := bus.BumpRevision(ctx, kv); err != nil {
		t.Fatalf("bump revision: %v", err)
	}
}

const loaderTop = `
reactor:
  - 'web-*/deploy/*':
      - reactor.deploy
`

const loaderReaction = `
notify:
  log:
    message: deploy on {{ event.peel }}
`

func loaderFixtureFiles() map[string]string {
	return map[string]string{
		TopKey:              loaderTop,
		"reactor/deploy.zy": loaderReaction,
	}
}

func newTestLoader(t *testing.T, kv bus.KV, hooks *loaderHooks) *Loader {
	t.Helper()
	logger, _ := testLogger()
	cfg := LoaderConfig{KV: kv, Logger: logger, Debounce: 10 * time.Millisecond, Jitter: time.Nanosecond}
	if hooks != nil {
		cfg.OnRulesLoaded = hooks.loaded
		cfg.OnRuleError = hooks.errored
	}
	l, err := NewLoader(cfg)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	return l
}

type loaderHooks struct {
	mu       sync.Mutex
	loadedNs []int
	errors   int
}

func (h *loaderHooks) loaded(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loadedNs = append(h.loadedNs, n)
}

func (h *loaderHooks) errored() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errors++
}

func (h *loaderHooks) counts() ([]int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int(nil), h.loadedNs...), h.errors
}

func TestLoaderLoadsRules(t *testing.T) {
	kv := bustest.NewFakeKV(bus.BucketReactorFiles, 0)
	publishFiles(t, kv, loaderFixtureFiles())

	hooks := &loaderHooks{}
	l := newTestLoader(t, kv, hooks)
	if err := l.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	rs := l.RuleSet()
	if len(rs.Rules) != 1 || rs.Rules[0].Ref != "reactor.deploy" {
		t.Fatalf("rules: %+v", rs.Rules)
	}
	if _, ok := rs.File("reactor.deploy"); !ok {
		t.Fatal("reaction file must be in the snapshot")
	}
	if l.LastError() != nil {
		t.Errorf("LastError: %v", l.LastError())
	}
	loaded, errs := hooks.counts()
	if len(loaded) != 1 || loaded[0] != 1 || errs != 0 {
		t.Errorf("hooks: loaded=%v errors=%d", loaded, errs)
	}
}

func TestLoaderEmptyBucketIsCleanNoOp(t *testing.T) {
	kv := bustest.NewFakeKV(bus.BucketReactorFiles, 0)
	l := newTestLoader(t, kv, nil)

	if err := l.Load(context.Background()); err != nil {
		t.Fatalf("empty bucket must load cleanly: %v", err)
	}
	if rs := l.RuleSet(); len(rs.Rules) != 0 || len(rs.Files) != 0 {
		t.Errorf("want empty rule set, got %+v", rs)
	}
}

func TestLoaderContentWithoutManifestIsTorn(t *testing.T) {
	kv := bustest.NewFakeKV(bus.BucketReactorFiles, 0)
	// Establish a good snapshot first (the LKG).
	publishFiles(t, kv, loaderFixtureFiles())
	hooks := &loaderHooks{}
	l := newTestLoader(t, kv, hooks)
	if err := l.Load(context.Background()); err != nil {
		t.Fatalf("initial load: %v", err)
	}

	// Simulate a torn publish: content present, manifest missing.
	if err := kv.Delete(context.Background(), KeyManifest); err != nil {
		t.Fatalf("delete manifest: %v", err)
	}
	err := l.Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "torn or tampered") {
		t.Fatalf("want torn-publish error, got %v", err)
	}

	// Last-known-good retained; error surfaced via LastError and hook.
	if rs := l.RuleSet(); len(rs.Rules) != 1 {
		t.Errorf("LKG rules must be retained, got %+v", rs.Rules)
	}
	if l.LastError() == nil {
		t.Error("LastError must report the failed load")
	}
	if _, errs := hooks.counts(); errs != 1 {
		t.Errorf("OnRuleError count: %d", errs)
	}
}

func TestLoaderHashMismatchKeepsLKG(t *testing.T) {
	kv := bustest.NewFakeKV(bus.BucketReactorFiles, 0)
	publishFiles(t, kv, loaderFixtureFiles())
	l := newTestLoader(t, kv, nil)
	if err := l.Load(context.Background()); err != nil {
		t.Fatalf("initial load: %v", err)
	}

	// Corrupt a file without updating the manifest.
	if _, err := kv.Put(context.Background(), "reactor/deploy.zy", []byte("tampered")); err != nil {
		t.Fatalf("put: %v", err)
	}
	err := l.Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("want hash-mismatch error, got %v", err)
	}
	if got, _ := l.RuleSet().File("reactor.deploy"); string(got) != loaderReaction {
		t.Error("LKG file content must be retained")
	}
}

func TestLoaderListedButMissingKeepsLKG(t *testing.T) {
	kv := bustest.NewFakeKV(bus.BucketReactorFiles, 0)
	publishFiles(t, kv, loaderFixtureFiles())
	l := newTestLoader(t, kv, nil)
	if err := l.Load(context.Background()); err != nil {
		t.Fatalf("initial load: %v", err)
	}

	if err := kv.Delete(context.Background(), "reactor/deploy.zy"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	err := l.Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("want listed-but-missing error, got %v", err)
	}
	if len(l.RuleSet().Rules) != 1 {
		t.Error("LKG rules must be retained")
	}
}

func TestLoaderAcceptsWrappedManifestShape(t *testing.T) {
	kv := bustest.NewFakeKV(bus.BucketReactorFiles, 0)
	ctx := context.Background()

	files := loaderFixtureFiles()
	var entries []manifestEntry
	for key, content := range files {
		if _, err := kv.Put(ctx, key, []byte(content)); err != nil {
			t.Fatalf("put: %v", err)
		}
		sum := sha256.Sum256([]byte(content))
		entries = append(entries, manifestEntry{Key: key, SHA256: hex.EncodeToString(sum[:])})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	// statefiles.Manifest wire shape: {files: [...]}.
	wrapped := struct {
		Files []manifestEntry `msgpack:"files"`
	}{Files: entries}
	data, err := bus.Encode(wrapped)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := kv.Put(ctx, KeyManifest, data); err != nil {
		t.Fatalf("put manifest: %v", err)
	}

	l := newTestLoader(t, kv, nil)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("wrapped manifest must load: %v", err)
	}
	if len(l.RuleSet().Rules) != 1 {
		t.Errorf("rules: %+v", l.RuleSet().Rules)
	}
}

func TestLoaderBadTopFileKeepsLKG(t *testing.T) {
	kv := bustest.NewFakeKV(bus.BucketReactorFiles, 0)
	publishFiles(t, kv, loaderFixtureFiles())
	l := newTestLoader(t, kv, nil)
	if err := l.Load(context.Background()); err != nil {
		t.Fatalf("initial load: %v", err)
	}

	bad := loaderFixtureFiles()
	bad[TopKey] = "reactor:\n  - broken: [\n"
	publishFiles(t, kv, bad)
	if err := l.Load(context.Background()); err == nil {
		t.Fatal("bad top file must fail the load")
	}
	if got := l.RuleSet(); len(got.Rules) != 1 || got.Rules[0].Ref != "reactor.deploy" {
		t.Errorf("LKG must be retained, got %+v", got.Rules)
	}
}

func TestLoaderMissingRefKeepsLKG(t *testing.T) {
	kv := bustest.NewFakeKV(bus.BucketReactorFiles, 0)
	publishFiles(t, kv, loaderFixtureFiles())
	l := newTestLoader(t, kv, nil)
	if err := l.Load(context.Background()); err != nil {
		t.Fatalf("initial load: %v", err)
	}

	publishFiles(t, kv, map[string]string{
		TopKey: "reactor:\n  - 'a/*':\n      - reactor.ghost\n",
	})
	err := l.Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing reaction file") {
		t.Fatalf("want missing-ref error, got %v", err)
	}
	if len(l.RuleSet().Rules) != 1 {
		t.Error("LKG must be retained")
	}
}

func TestLoaderMissingTopFileYieldsNoRules(t *testing.T) {
	kv := bustest.NewFakeKV(bus.BucketReactorFiles, 0)
	publishFiles(t, kv, map[string]string{"reactor/deploy.zy": loaderReaction})

	l := newTestLoader(t, kv, nil)
	if err := l.Load(context.Background()); err != nil {
		t.Fatalf("missing top must not error: %v", err)
	}
	rs := l.RuleSet()
	if len(rs.Rules) != 0 {
		t.Errorf("want no rules, got %+v", rs.Rules)
	}
	if len(rs.Files) != 1 {
		t.Errorf("reaction files still load, got %d", len(rs.Files))
	}
}

func TestLoaderRejectsPathEscapingManifestKey(t *testing.T) {
	kv := bustest.NewFakeKV(bus.BucketReactorFiles, 0)
	ctx := context.Background()
	entries := []manifestEntry{{Key: "../etc/passwd", SHA256: strings.Repeat("0", 64)}}
	data, err := bus.Encode(entries)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := kv.Put(ctx, KeyManifest, data); err != nil {
		t.Fatalf("put: %v", err)
	}

	l := newTestLoader(t, kv, nil)
	if err := l.Load(ctx); err == nil || !strings.Contains(err.Error(), "path-escaping") {
		t.Fatalf("want path-escape rejection, got %v", err)
	}
}

func TestLoaderWatchTriggersDebouncedReload(t *testing.T) {
	kv := bustest.NewFakeKV(bus.BucketReactorFiles, 0)
	publishFiles(t, kv, loaderFixtureFiles())

	l := newTestLoader(t, kv, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	l.Start(ctx)

	if len(l.RuleSet().Rules) != 1 {
		t.Fatal("initial load must be synchronous")
	}

	// Publish a second rule and bump the revision.
	updated := loaderFixtureFiles()
	updated[TopKey] = loaderTop + "  - 'a/*':\n      - reactor.deploy\n"
	publishFiles(t, kv, updated)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(l.RuleSet().Rules) == 2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("hot reload did not land; rules=%d", len(l.RuleSet().Rules))
}

func TestLoaderEnrollLintWarnings(t *testing.T) {
	kv := bustest.NewFakeKV(bus.BucketReactorFiles, 0)
	publishFiles(t, kv, map[string]string{
		TopKey: "reactor:\n  - '*':\n      - reactor.autoapprove\n",
		"reactor/autoapprove.zy": `
approve:
  enroll.approve:
    - id: '{{ data.get("id", "") }}'
    - require_peel: '*'
`,
	})

	logger, buf := testLogger()
	l, err := NewLoader(LoaderConfig{KV: kv, Logger: logger})
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	if err := l.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	logs := buf.String()
	if !strings.Contains(logs, "bare-star glob") {
		t.Errorf("want bare-star glob warning, logs:\n%s", logs)
	}
	if !strings.Contains(logs, "bare-star require_peel") {
		t.Errorf("want bare-star require_peel warning, logs:\n%s", logs)
	}
}
