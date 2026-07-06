package facts

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
)

func TestWatch_ReconnectsAfterChannelClose(t *testing.T) {
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

	// Give watcher time to initialize.
	time.Sleep(100 * time.Millisecond)

	// Publish a value and verify the callback fires.
	kv, err := bus.GetBucket(ctx, js, bus.BucketFacts)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	_, err = bus.KVPut(ctx, kv, "web-01", Facts{"os": map[string]any{"name": "linux"}})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	select {
	case id := <-updates:
		if id != "web-01" {
			t.Fatalf("expected web-01, got %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first update")
	}

	// Simulate cluster failure: close all watchers.
	fakeKV := js.GetBucket(bus.BucketFacts)
	fakeKV.CloseWatchers()

	// Wait for the retry loop to reconnect.
	time.Sleep(2 * time.Second)

	// Drain replayed values from the reconnected watcher (WatchAll replays
	// all current entries on startup before switching to live mode).
drainLoop:
	for {
		select {
		case <-updates:
		case <-time.After(500 * time.Millisecond):
			break drainLoop
		}
	}

	// Publish another value and verify the reconnected watcher picks it up.
	_, err = bus.KVPut(ctx, kv, "web-02", Facts{"os": map[string]any{"name": "freebsd"}})
	if err != nil {
		t.Fatalf("put after reconnect: %v", err)
	}

	select {
	case id := <-updates:
		if id != "web-02" {
			t.Fatalf("expected web-02, got %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for update after reconnect")
	}

	// Verify old watcher was cleaned up and a new one is active.
	if count := fakeKV.WatcherCount(); count != 1 {
		t.Fatalf("expected 1 active watcher, got %d", count)
	}
}

func TestWatch_CancelDuringReconnect(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	cancel, err := Watch(ctx, js, func(peelID string, facts Facts) {}, slog.Default())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Simulate cluster failure.
	fakeKV := js.GetBucket(bus.BucketFacts)
	fakeKV.CloseWatchers()

	// Cancel while the goroutine is in the retry loop.
	time.Sleep(200 * time.Millisecond)
	cancel()

	// Give the goroutine time to exit cleanly.
	time.Sleep(500 * time.Millisecond)

	// After cancel, no new watchers should be created.
	if count := fakeKV.WatcherCount(); count != 0 {
		t.Fatalf("expected 0 active watchers after cancel, got %d", count)
	}
}
