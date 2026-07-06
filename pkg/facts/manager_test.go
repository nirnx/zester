package facts

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
)

// stubCollector is a test collector that returns static data.
type stubCollector struct {
	name     string
	data     map[string]any
	interval time.Duration
	calls    atomic.Int32
}

func (c *stubCollector) Name() string            { return c.name }
func (c *stubCollector) Interval() time.Duration { return c.interval }
func (c *stubCollector) Collect(_ context.Context) (map[string]any, error) {
	c.calls.Add(1)
	return c.data, nil
}

func TestNewManagerRequiresPeelID(t *testing.T) {
	_, err := NewManager(ManagerConfig{
		JS: nil,
	})
	if err == nil {
		t.Fatal("expected error when PeelID is empty")
	}
}

func TestNewManagerRequiresJS(t *testing.T) {
	_, err := NewManager(ManagerConfig{
		PeelID: "test-01",
	})
	if err == nil {
		t.Fatal("expected error when JS is nil")
	}
}

func TestManagerStartAndGetFacts(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	osCollector := &stubCollector{
		name: "os",
		data: map[string]any{
			"name": "linux",
			"arch": "amd64",
		},
	}
	netCollector := &stubCollector{
		name: "network",
		data: map[string]any{
			"hostname": "web-01",
		},
	}

	mgr, err := NewManager(ManagerConfig{
		PeelID:     "web-01",
		JS:         js,
		Collectors: []Collector{osCollector, netCollector},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer mgr.Stop()

	// Verify collectors were called.
	if n := osCollector.calls.Load(); n != 1 {
		t.Errorf("os collector calls: got %d, want 1", n)
	}
	if n := netCollector.calls.Load(); n != 1 {
		t.Errorf("network collector calls: got %d, want 1", n)
	}

	// Verify facts are accessible.
	facts := mgr.GetFacts()
	osData, ok := facts["os"].(map[string]any)
	if !ok {
		t.Fatal("os facts not found")
	}
	if osData["name"] != "linux" {
		t.Errorf("os.name: got %v, want linux", osData["name"])
	}

	netData, ok := facts["network"].(map[string]any)
	if !ok {
		t.Fatal("network facts not found")
	}
	if netData["hostname"] != "web-01" {
		t.Errorf("network.hostname: got %v, want web-01", netData["hostname"])
	}
}

func TestManagerPublishesToKV(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	mgr, err := NewManager(ManagerConfig{
		PeelID: "web-01",
		JS:     js,
		Collectors: []Collector{
			&stubCollector{
				name: "os",
				data: map[string]any{"name": "linux"},
			},
		},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer mgr.Stop()

	// Read back from KV.
	kv, err := bus.GetBucket(ctx, js, bus.BucketFacts)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	var stored Facts
	if err := bus.KVGet(ctx, kv, "web-01", &stored); err != nil {
		t.Fatalf("kv get: %v", err)
	}

	osData, ok := stored["os"].(map[string]any)
	if !ok {
		t.Fatal("os facts not in KV")
	}
	if osData["name"] != "linux" {
		t.Errorf("KV os.name: got %v, want linux", osData["name"])
	}
}

func TestManagerScheduledCollector(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	memCollector := &stubCollector{
		name:     "memory",
		data:     map[string]any{"total": uint64(8000000000)},
		interval: 100 * time.Millisecond,
	}

	mgr, err := NewManager(ManagerConfig{
		PeelID:     "web-01",
		JS:         js,
		Collectors: []Collector{memCollector},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for a few scheduled runs.
	time.Sleep(350 * time.Millisecond)
	mgr.Stop()

	// The collector should have been called more than once (initial + scheduled).
	if n := memCollector.calls.Load(); n < 2 {
		t.Errorf("scheduled collector calls: got %d, want >= 2", n)
	}
}

func TestWatch(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	updates := make(chan string, 10)
	cancel, err := Watch(ctx, js, func(peelID string, facts Facts) {
		updates <- peelID
	}, slog.Default())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer cancel()

	// Give the watcher time to initialize.
	time.Sleep(200 * time.Millisecond)

	// Publish facts for two peels.
	kv, err := bus.GetBucket(ctx, js, bus.BucketFacts)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	_, err = bus.KVPut(ctx, kv, "web-01", Facts{"os": map[string]any{"name": "linux"}})
	if err != nil {
		t.Fatalf("kv put web-01: %v", err)
	}

	_, err = bus.KVPut(ctx, kv, "web-02", Facts{"os": map[string]any{"name": "freebsd"}})
	if err != nil {
		t.Fatalf("kv put web-02: %v", err)
	}

	// Collect updates.
	seen := make(map[string]bool)
	deadline := time.After(3 * time.Second)
	for len(seen) < 2 {
		select {
		case id := <-updates:
			seen[id] = true
		case <-deadline:
			t.Fatalf("timeout: only saw %v", seen)
		}
	}

	if !seen["web-01"] || !seen["web-02"] {
		t.Errorf("expected both peels, got %v", seen)
	}
}

func TestManagerWithWatchAndIndex(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	// Start a manager for web-01.
	mgr1, err := NewManager(ManagerConfig{
		PeelID: "web-01",
		JS:     js,
		Collectors: []Collector{
			&stubCollector{name: "os", data: map[string]any{"name": "linux", "arch": "amd64"}},
			&stubCollector{name: "network", data: map[string]any{"hostname": "web-01"}},
		},
	})
	if err != nil {
		t.Fatalf("new manager 1: %v", err)
	}

	// Start a manager for db-01.
	mgr2, err := NewManager(ManagerConfig{
		PeelID: "db-01",
		JS:     js,
		Collectors: []Collector{
			&stubCollector{name: "os", data: map[string]any{"name": "freebsd", "arch": "amd64"}},
			&stubCollector{name: "network", data: map[string]any{"hostname": "db-01"}},
		},
	})
	if err != nil {
		t.Fatalf("new manager 2: %v", err)
	}

	// Build an index on the master side.
	idx := NewIndex()
	indexed := make(chan struct{}, 10)

	cancel, err := Watch(ctx, js, func(peelID string, facts Facts) {
		idx.Update(peelID, facts)
		indexed <- struct{}{}
	}, slog.Default())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer cancel()

	// Give watcher time to initialize.
	time.Sleep(200 * time.Millisecond)

	// Start both managers (publishes to KV).
	if err := mgr1.Start(ctx); err != nil {
		t.Fatalf("start mgr1: %v", err)
	}
	defer mgr1.Stop()

	if err := mgr2.Start(ctx); err != nil {
		t.Fatalf("start mgr2: %v", err)
	}
	defer mgr2.Stop()

	// Wait for both to be indexed.
	deadline := time.After(3 * time.Second)
	indexedCount := 0
	for indexedCount < 2 {
		select {
		case <-indexed:
			indexedCount++
		case <-deadline:
			t.Fatalf("timeout waiting for index updates, got %d", indexedCount)
		}
	}

	// Query the index.
	linux := idx.Match("os.name", "linux")
	if len(linux) != 1 || linux[0] != "web-01" {
		t.Errorf("linux match: got %v, want [web-01]", linux)
	}

	amd64 := idx.Match("os.arch", "amd64")
	if len(amd64) != 2 {
		t.Errorf("amd64 match: got %v, want 2 peels", amd64)
	}

	webHosts := idx.Match("network.hostname", "web-*")
	if len(webHosts) != 1 || webHosts[0] != "web-01" {
		t.Errorf("web-* match: got %v, want [web-01]", webHosts)
	}

}
