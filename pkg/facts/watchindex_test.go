package facts

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
)

// waitFor polls cond until it returns true or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", msg)
}

func TestWatchIntoIndex(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	kv, err := bus.GetBucket(ctx, js, bus.BucketFacts)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	// Pre-populate one peel to verify the initial WatchAll replay seeds the index.
	if _, err := bus.KVPut(ctx, kv, "web-01", Facts{
		"os":      map[string]any{"name": "linux", "arch": "amd64"},
		"network": map[string]any{"hostname": "web-01"},
	}); err != nil {
		t.Fatalf("put web-01: %v", err)
	}

	idx := NewIndex()
	if idx.Seeded() {
		t.Fatal("a fresh index must not report seeded")
	}
	cancel, err := WatchIntoIndex(ctx, js, idx, slog.Default())
	if err != nil {
		t.Fatalf("watch into index: %v", err)
	}
	defer cancel()

	waitFor(t, 2*time.Second, func() bool {
		return len(idx.PeelIDs()) == 1
	}, "initial replay to seed index")

	// The end-of-replay sentinel marks the index seeded — the signal the
	// reactor's resolve gate relies on to distinguish "empty because the
	// fleet is empty" from "empty because the replay has not landed yet".
	waitFor(t, 2*time.Second, idx.Seeded, "index to be marked seeded after the initial replay")

	// A put after the watch started must be applied.
	if _, err := bus.KVPut(ctx, kv, "db-01", Facts{
		"os":      map[string]any{"name": "freebsd", "arch": "amd64"},
		"network": map[string]any{"hostname": "db-01"},
	}); err != nil {
		t.Fatalf("put db-01: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return len(idx.PeelIDs()) == 2
	}, "db-01 to appear in index")

	// The inverted index must answer fact matches.
	if got := idx.Match("os.name", "linux"); len(got) != 1 || got[0] != "web-01" {
		t.Fatalf("Match(os.name, linux) = %v, want [web-01]", got)
	}

	// Raw facts must round-trip as nested maps (for the target matchers).
	raw := idx.RawFacts("db-01")
	if raw == nil {
		t.Fatal("RawFacts(db-01) = nil")
	}
	osMap, ok := raw["os"].(map[string]any)
	if !ok || osMap["name"] != "freebsd" {
		t.Fatalf("RawFacts(db-01) os.name = %v, want freebsd", raw["os"])
	}

	// An update must replace, not accumulate.
	if _, err := bus.KVPut(ctx, kv, "web-01", Facts{
		"os":      map[string]any{"name": "linux", "arch": "arm64"},
		"network": map[string]any{"hostname": "web-01"},
	}); err != nil {
		t.Fatalf("update web-01: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		got := idx.Match("os.arch", "arm64")
		return len(got) == 1 && got[0] == "web-01"
	}, "web-01 arch update to be indexed")
	if got := idx.Match("os.arch", "amd64"); len(got) != 1 || got[0] != "db-01" {
		t.Fatalf("stale amd64 entry for web-01 not removed: %v", got)
	}

	// A KV delete must remove the peel from the index.
	if err := kv.Delete(ctx, "db-01"); err != nil {
		t.Fatalf("delete db-01: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		ids := idx.PeelIDs()
		return len(ids) == 1 && ids[0] == "web-01"
	}, "db-01 to be removed from index after delete")

	if idx.RawFacts("db-01") != nil {
		t.Fatal("RawFacts(db-01) should be nil after delete")
	}
}

func TestWatchIntoIndexNilIndex(t *testing.T) {
	js := bustest.NewFakeJS()
	if _, err := WatchIntoIndex(context.Background(), js, nil, slog.Default()); err == nil {
		t.Fatal("expected error for nil index")
	}
}

func TestIndexRawFactsAccessors(t *testing.T) {
	idx := NewIndex()

	if idx.RawFacts("nope") != nil {
		t.Fatal("RawFacts on empty index should be nil")
	}
	if got := idx.AllRawFacts(); len(got) != 0 {
		t.Fatalf("AllRawFacts on empty index = %v, want empty", got)
	}

	idx.Update("web-01", Facts{"os": map[string]any{"name": "linux"}})
	idx.Update("db-01", Facts{"os": map[string]any{"name": "freebsd"}})

	all := idx.AllRawFacts()
	if len(all) != 2 {
		t.Fatalf("AllRawFacts len = %d, want 2", len(all))
	}
	osMap, ok := all["web-01"]["os"].(map[string]any)
	if !ok || osMap["name"] != "linux" {
		t.Fatalf("AllRawFacts web-01 os = %v", all["web-01"]["os"])
	}

	idx.Remove("web-01")
	if idx.RawFacts("web-01") != nil {
		t.Fatal("RawFacts(web-01) should be nil after Remove")
	}
	if len(idx.AllRawFacts()) != 1 {
		t.Fatal("AllRawFacts should have 1 entry after Remove")
	}

	// The outer map is a copy: mutating it must not affect the index.
	all2 := idx.AllRawFacts()
	delete(all2, "db-01")
	if idx.RawFacts("db-01") == nil {
		t.Fatal("mutating AllRawFacts copy must not affect the index")
	}
}
