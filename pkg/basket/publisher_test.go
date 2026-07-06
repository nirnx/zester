package basket_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/basket"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
)

func testSetup(t *testing.T) *bustest.FakeJS {
	t.Helper()
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatal(err)
	}
	return js
}

func TestPublisher_InitialPublish(t *testing.T) {
	js := testSetup(t)
	ctx := context.Background()

	pub, err := basket.NewPublisher(basket.PublisherConfig{
		PeelID: "web-01",
		JS:     js,
		Functions: map[string]time.Duration{
			"default_ipv4": 5 * time.Minute,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	factsFn := func() map[string]any {
		return map[string]any{
			"default_ipv4": "10.0.1.50",
		}
	}

	if err := pub.Start(ctx, factsFn); err != nil {
		t.Fatal(err)
	}
	defer pub.Stop()

	// Verify the value was written to KV.
	kv := js.GetBucket(bus.BucketBasket)
	if kv == nil {
		t.Fatal("basket bucket not found")
	}

	var val any
	if err := bus.KVGet(ctx, kv, "web-01.default_ipv4", &val); err != nil {
		t.Fatalf("KVGet failed: %v", err)
	}
	if val != "10.0.1.50" {
		t.Errorf("got %v, want 10.0.1.50", val)
	}
}

func TestPublisher_NestedFactLookup(t *testing.T) {
	js := testSetup(t)
	ctx := context.Background()

	pub, err := basket.NewPublisher(basket.PublisherConfig{
		PeelID: "web-01",
		JS:     js,
		Functions: map[string]time.Duration{
			"network.hostname": 5 * time.Minute,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	factsFn := func() map[string]any {
		return map[string]any{
			"network": map[string]any{
				"hostname": "web-01.example.com",
			},
		}
	}

	if err := pub.Start(ctx, factsFn); err != nil {
		t.Fatal(err)
	}
	defer pub.Stop()

	kv := js.GetBucket(bus.BucketBasket)
	var val any
	if err := bus.KVGet(ctx, kv, "web-01.network.hostname", &val); err != nil {
		t.Fatalf("KVGet failed: %v", err)
	}
	if val != "web-01.example.com" {
		t.Errorf("got %v, want web-01.example.com", val)
	}
}

func TestPublisher_MissingFactSkipped(t *testing.T) {
	js := testSetup(t)
	ctx := context.Background()

	pub, err := basket.NewPublisher(basket.PublisherConfig{
		PeelID: "web-01",
		JS:     js,
		Functions: map[string]time.Duration{
			"nonexistent_key": 5 * time.Minute,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	factsFn := func() map[string]any {
		return map[string]any{
			"default_ipv4": "10.0.1.50",
		}
	}

	if err := pub.Start(ctx, factsFn); err != nil {
		t.Fatal(err)
	}
	defer pub.Stop()

	// Key should not exist in KV since fact was not found.
	kv := js.GetBucket(bus.BucketBasket)
	var val any
	err = bus.KVGet(ctx, kv, "web-01.nonexistent_key", &val)
	if err == nil {
		t.Error("expected error for missing key, got nil")
	}
}

func TestNewPublisher_Validation(t *testing.T) {
	js := testSetup(t)

	tests := []struct {
		name    string
		cfg     basket.PublisherConfig
		wantErr bool
	}{
		{
			name:    "missing PeelID",
			cfg:     basket.PublisherConfig{JS: js},
			wantErr: true,
		},
		{
			name:    "missing JS",
			cfg:     basket.PublisherConfig{PeelID: "web-01"},
			wantErr: true,
		},
		{
			name: "valid config",
			cfg: basket.PublisherConfig{
				PeelID:    "web-01",
				JS:        js,
				Functions: map[string]time.Duration{"default_ipv4": 5 * time.Minute},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := basket.NewPublisher(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewPublisher() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseFunctions(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]any
		want     int
	}{
		{
			name:     "nil settings",
			settings: nil,
			want:     0,
		},
		{
			name:     "no basket_functions key",
			settings: map[string]any{"log_level": "info"},
			want:     0,
		},
		{
			name: "valid functions",
			settings: map[string]any{
				"basket_functions": map[string]any{
					"default_ipv4":     "5m",
					"network.hostname": "10m",
				},
			},
			want: 2,
		},
		{
			name: "invalid duration uses default",
			settings: map[string]any{
				"basket_functions": map[string]any{
					"default_ipv4": "not-a-duration",
				},
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := basket.ParseFunctions(tt.settings)
			if len(result) != tt.want {
				t.Errorf("ParseFunctions() returned %d functions, want %d", len(result), tt.want)
			}
		})
	}
}

func TestParseFunctions_DefaultDuration(t *testing.T) {
	settings := map[string]any{
		"basket_functions": map[string]any{
			"default_ipv4": "not-valid",
		},
	}
	result := basket.ParseFunctions(settings)
	if d, ok := result["default_ipv4"]; !ok || d != 5*time.Minute {
		t.Errorf("expected 5m default, got %v", d)
	}
}

func TestParseFunctions_ValidDuration(t *testing.T) {
	settings := map[string]any{
		"basket_functions": map[string]any{
			"default_ipv4": "10m",
		},
	}
	result := basket.ParseFunctions(settings)
	if d, ok := result["default_ipv4"]; !ok || d != 10*time.Minute {
		t.Errorf("expected 10m, got %v", d)
	}
}

// countingKV wraps a bus.KV, counting Put calls and optionally
// failing them, so tests can observe the publisher's hash-skip behavior.
type countingKV struct {
	bus.KV

	mu      sync.Mutex
	puts    int
	failPut bool
}

func (c *countingKV) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	c.mu.Lock()
	fail := c.failPut
	c.mu.Unlock()
	if fail {
		return 0, errors.New("injected put failure")
	}
	rev, err := c.KV.Put(ctx, key, value)
	if err == nil {
		c.mu.Lock()
		c.puts++
		c.mu.Unlock()
	}
	return rev, err
}

func (c *countingKV) putCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.puts
}

func (c *countingKV) setFailPut(fail bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failPut = fail
}

// wrappedJS intercepts KeyValue lookups for one bucket, returning the
// countingKV wrapper instead of the raw fake bucket.
type wrappedJS struct {
	bus.JetStreamAPI
	bucket string
	kv     *countingKV
}

func (w *wrappedJS) KeyValue(ctx context.Context, bucket string) (bus.KV, error) {
	inner, err := w.JetStreamAPI.KeyValue(ctx, bucket)
	if err != nil {
		return nil, err
	}
	if bucket == w.bucket {
		if w.kv.KV == nil {
			w.kv.KV = inner
		}
		return w.kv, nil
	}
	return inner, nil
}

func countingSetup(t *testing.T) (*bustest.FakeJS, *wrappedJS) {
	t.Helper()
	js := testSetup(t)
	return js, &wrappedJS{JetStreamAPI: js, bucket: bus.BucketBasket, kv: &countingKV{}}
}

func TestPublisher_RefreshSkipsUnchangedValue(t *testing.T) {
	_, wjs := countingSetup(t)
	ctx := context.Background()

	pub, err := basket.NewPublisher(basket.PublisherConfig{
		PeelID: "web-01",
		JS:     wjs,
		Functions: map[string]time.Duration{
			"default_ipv4": 20 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	factsFn := func() map[string]any {
		return map[string]any{"default_ipv4": "10.0.1.50"}
	}

	if err := pub.Start(ctx, factsFn); err != nil {
		t.Fatal(err)
	}
	defer pub.Stop()

	// Let several refresh ticks elapse with identical data.
	time.Sleep(150 * time.Millisecond)

	if got := wjs.kv.putCount(); got != 1 {
		t.Errorf("put count = %d, want exactly 1 (unchanged value must skip KV write)", got)
	}
}

func TestPublisher_RefreshPublishesChangedValue(t *testing.T) {
	js, wjs := countingSetup(t)
	ctx := context.Background()

	var mu sync.Mutex
	ip := "10.0.1.50"
	factsFn := func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return map[string]any{"default_ipv4": ip}
	}

	pub, err := basket.NewPublisher(basket.PublisherConfig{
		PeelID: "web-01",
		JS:     wjs,
		Functions: map[string]time.Duration{
			"default_ipv4": 20 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := pub.Start(ctx, factsFn); err != nil {
		t.Fatal(err)
	}
	defer pub.Stop()

	if got := wjs.kv.putCount(); got != 1 {
		t.Fatalf("put count after initial publish = %d, want 1", got)
	}

	mu.Lock()
	ip = "10.0.1.99"
	mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for wjs.kv.putCount() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for changed value to be published")
		}
		time.Sleep(10 * time.Millisecond)
	}

	kv := js.GetBucket(bus.BucketBasket)
	var val any
	if err := bus.KVGet(ctx, kv, "web-01.default_ipv4", &val); err != nil {
		t.Fatalf("KVGet failed: %v", err)
	}
	if val != "10.0.1.99" {
		t.Errorf("got %v, want 10.0.1.99", val)
	}
}

func TestPublisher_FailedPutRetriedNextTick(t *testing.T) {
	js, wjs := countingSetup(t)
	ctx := context.Background()

	// Initial publish fails; the hash must not be cached so the
	// refresh loop retries the same (unchanged) value.
	wjs.kv.setFailPut(true)

	pub, err := basket.NewPublisher(basket.PublisherConfig{
		PeelID: "web-01",
		JS:     wjs,
		Functions: map[string]time.Duration{
			"default_ipv4": 20 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	factsFn := func() map[string]any {
		return map[string]any{"default_ipv4": "10.0.1.50"}
	}

	if err := pub.Start(ctx, factsFn); err != nil {
		t.Fatal(err)
	}
	defer pub.Stop()

	if got := wjs.kv.putCount(); got != 0 {
		t.Fatalf("put count after failed initial publish = %d, want 0", got)
	}

	wjs.kv.setFailPut(false)

	// If the failed put had cached the hash, every subsequent tick would
	// skip the write and the key would never appear.
	kv := js.GetBucket(bus.BucketBasket)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var val any
		if err := bus.KVGet(ctx, kv, "web-01.default_ipv4", &val); err == nil {
			if val != "10.0.1.50" {
				t.Fatalf("got %v, want 10.0.1.50", val)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for retried publish after failed put")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := wjs.kv.putCount(); got != 1 {
		t.Errorf("put count after retry = %d, want 1", got)
	}
}
