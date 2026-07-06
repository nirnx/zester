package settings_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/settings"
)

// disableWatchJitter turns off the deterministic reconnect jitter for the
// duration of a test (reconnect tests wait ~2s; the default spread is up to
// 60s) and restores the package default afterwards.
func disableWatchJitter(t *testing.T) {
	t.Helper()
	settings.SetWatchJitter("watch-test", 0)
	t.Cleanup(func() {
		settings.SetWatchJitter("", settings.DefaultWatchReconnectJitter)
	})
}

func TestWatchRawFiles_ReconnectsAfterChannelClose(t *testing.T) {
	disableWatchJitter(t)
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	updates := make(chan struct{}, 10)
	cancel, err := settings.WatchRawFiles(ctx, js, func() {
		updates <- struct{}{}
	}, slog.Default())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer cancel()

	time.Sleep(100 * time.Millisecond)

	// Bump revision to trigger the watcher (it watches _revision, not all keys).
	kv, err := bus.GetBucket(ctx, js, bus.BucketSettingsFiles)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if err := bus.BumpRevision(ctx, kv); err != nil {
		t.Fatalf("bump revision: %v", err)
	}

	select {
	case <-updates:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first update")
	}

	// Simulate cluster failure.
	fakeKV := js.GetBucket(bus.BucketSettingsFiles)
	fakeKV.CloseWatchers()
	time.Sleep(2 * time.Second)

	// Drain replayed values from the reconnected watcher.
drainRawFiles:
	for {
		select {
		case <-updates:
		case <-time.After(500 * time.Millisecond):
			break drainRawFiles
		}
	}

	// Bump revision again after reconnect.
	if err := bus.BumpRevision(ctx, kv); err != nil {
		t.Fatalf("bump revision after reconnect: %v", err)
	}

	select {
	case <-updates:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for update after reconnect")
	}

	if count := fakeKV.WatcherCount(); count != 1 {
		t.Fatalf("expected 1 active watcher, got %d", count)
	}
}

func TestWatchRawFiles_IgnoresNonRevisionKeys(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	updates := make(chan struct{}, 10)
	cancel, err := settings.WatchRawFiles(ctx, js, func() {
		updates <- struct{}{}
	}, slog.Default())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer cancel()

	time.Sleep(100 * time.Millisecond)

	// Write a regular file (not _revision) — should NOT trigger callback.
	kv, err := bus.GetBucket(ctx, js, bus.BucketSettingsFiles)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if _, err := kv.Put(ctx, "test.zy", []byte("key: value")); err != nil {
		t.Fatalf("put: %v", err)
	}

	select {
	case <-updates:
		t.Fatal("callback should not fire for non-revision key writes")
	case <-time.After(500 * time.Millisecond):
		// expected — no callback
	}

	// Now bump revision — should trigger callback.
	if err := bus.BumpRevision(ctx, kv); err != nil {
		t.Fatalf("bump revision: %v", err)
	}

	select {
	case <-updates:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for revision-triggered update")
	}
}

func TestWatchSecrets_ReconnectsAfterChannelClose(t *testing.T) {
	disableWatchJitter(t)
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	peelID := "peel-01"

	updates := make(chan struct{}, 10)
	cancel, err := settings.WatchSecrets(ctx, js, peelID, func() {
		updates <- struct{}{}
	}, slog.Default())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer cancel()

	time.Sleep(100 * time.Millisecond)

	// Publish a secret for this peel.
	kv, err := bus.GetBucket(ctx, js, bus.BucketSecrets)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if _, err := kv.Put(ctx, peelID, []byte(`{"db_pass":"secret"}`)); err != nil {
		t.Fatalf("put: %v", err)
	}

	select {
	case <-updates:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first update")
	}

	// Simulate cluster failure.
	fakeKV := js.GetBucket(bus.BucketSecrets)
	fakeKV.CloseWatchers()
	time.Sleep(2 * time.Second)

	// Drain replayed values from the reconnected watcher.
drainSecrets:
	for {
		select {
		case <-updates:
		case <-time.After(500 * time.Millisecond):
			break drainSecrets
		}
	}

	// Publish again after reconnect.
	if _, err := kv.Put(ctx, peelID, []byte(`{"db_pass":"newsecret"}`)); err != nil {
		t.Fatalf("put after reconnect: %v", err)
	}

	select {
	case <-updates:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for update after reconnect")
	}

	if count := fakeKV.WatcherCount(); count != 1 {
		t.Fatalf("expected 1 active watcher, got %d", count)
	}
}

func TestWatchMasterCurvePub_ReconnectsAfterChannelClose(t *testing.T) {
	disableWatchJitter(t)
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	updates := make(chan string, 10)
	cancel, err := settings.WatchMasterCurvePub(ctx, js, func(pub string) {
		updates <- pub
	}, slog.Default())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer cancel()

	time.Sleep(100 * time.Millisecond)

	// Publish the master curve key.
	kv, err := bus.GetBucket(ctx, js, bus.BucketSecrets)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if _, err := kv.Put(ctx, settings.MasterCurvePubKey, []byte("curve-key-v1")); err != nil {
		t.Fatalf("put: %v", err)
	}

	select {
	case pub := <-updates:
		if pub != "curve-key-v1" {
			t.Fatalf("expected curve-key-v1, got %s", pub)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first update")
	}

	// Simulate cluster failure.
	fakeKV := js.GetBucket(bus.BucketSecrets)
	fakeKV.CloseWatchers()
	time.Sleep(2 * time.Second)

	// Drain any replayed values from the reconnected watcher (it replays
	// the current key value on startup before switching to live mode).
drainLoop:
	for {
		select {
		case <-updates:
		case <-time.After(500 * time.Millisecond):
			break drainLoop
		}
	}

	// Publish updated key after reconnect.
	if _, err := kv.Put(ctx, settings.MasterCurvePubKey, []byte("curve-key-v2")); err != nil {
		t.Fatalf("put after reconnect: %v", err)
	}

	select {
	case pub := <-updates:
		if pub != "curve-key-v2" {
			t.Fatalf("expected curve-key-v2, got %s", pub)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for update after reconnect")
	}

	if count := fakeKV.WatcherCount(); count != 1 {
		t.Fatalf("expected 1 active watcher, got %d", count)
	}
}

// TestWatchSecretsAndCurve_DispatchesBothCallbacks verifies that the combined
// watcher fires onSecrets for the per-peel key, onCurve for the master curve
// pub key, ignores unrelated keys, and uses a SINGLE underlying watcher
// (one JetStream consumer instead of two).
func TestWatchSecretsAndCurve_DispatchesBothCallbacks(t *testing.T) {
	disableWatchJitter(t)
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	peelID := "peel-01"
	secretsUpdates := make(chan struct{}, 10)
	curveUpdates := make(chan string, 10)

	cancel, err := settings.WatchSecretsAndCurve(ctx, js, peelID,
		func() { secretsUpdates <- struct{}{} },
		func(pub string) { curveUpdates <- pub },
		slog.Default())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer cancel()

	fakeKV := js.GetBucket(bus.BucketSecrets)
	if count := fakeKV.WatcherCount(); count != 1 {
		t.Fatalf("combined watch should register exactly 1 watcher, got %d", count)
	}

	time.Sleep(100 * time.Millisecond)

	kv, err := bus.GetBucket(ctx, js, bus.BucketSecrets)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	// Per-peel secrets key change → onSecrets.
	if _, err := kv.Put(ctx, peelID, []byte(`{"db_pass":"secret"}`)); err != nil {
		t.Fatalf("put secrets: %v", err)
	}
	select {
	case <-secretsUpdates:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for onSecrets")
	}

	// Master curve pub key change → onCurve with the value.
	if _, err := kv.Put(ctx, settings.MasterCurvePubKey, []byte("curve-key-v1")); err != nil {
		t.Fatalf("put curve pub: %v", err)
	}
	select {
	case pub := <-curveUpdates:
		if pub != "curve-key-v1" {
			t.Fatalf("onCurve pub = %q, want curve-key-v1", pub)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for onCurve")
	}

	// Unrelated key (another peel's secrets) → neither callback fires.
	if _, err := kv.Put(ctx, "peel-99", []byte(`{"x":"y"}`)); err != nil {
		t.Fatalf("put other peel: %v", err)
	}
	select {
	case <-secretsUpdates:
		t.Fatal("onSecrets fired for another peel's key")
	case pub := <-curveUpdates:
		t.Fatalf("onCurve fired for another peel's key: %q", pub)
	case <-time.After(500 * time.Millisecond):
		// expected — no callback
	}
}

// TestWatchSecretsAndCurve_ReconnectsAfterChannelClose verifies the combined
// watcher survives a JetStream consumer loss and keeps dispatching both
// callbacks after reconnecting.
func TestWatchSecretsAndCurve_ReconnectsAfterChannelClose(t *testing.T) {
	disableWatchJitter(t)
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	peelID := "peel-01"
	secretsUpdates := make(chan struct{}, 10)
	curveUpdates := make(chan string, 10)

	cancel, err := settings.WatchSecretsAndCurve(ctx, js, peelID,
		func() { secretsUpdates <- struct{}{} },
		func(pub string) { curveUpdates <- pub },
		slog.Default())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer cancel()

	time.Sleep(100 * time.Millisecond)

	kv, err := bus.GetBucket(ctx, js, bus.BucketSecrets)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if _, err := kv.Put(ctx, peelID, []byte(`{"a":"1"}`)); err != nil {
		t.Fatalf("put: %v", err)
	}
	select {
	case <-secretsUpdates:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first onSecrets")
	}

	// Simulate cluster failure.
	fakeKV := js.GetBucket(bus.BucketSecrets)
	fakeKV.CloseWatchers()
	time.Sleep(2 * time.Second)

	// Drain replayed values from the reconnected watcher.
drainCombined:
	for {
		select {
		case <-secretsUpdates:
		case <-curveUpdates:
		case <-time.After(500 * time.Millisecond):
			break drainCombined
		}
	}

	// Both keys still dispatch after reconnect.
	if _, err := kv.Put(ctx, peelID, []byte(`{"a":"2"}`)); err != nil {
		t.Fatalf("put after reconnect: %v", err)
	}
	select {
	case <-secretsUpdates:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for onSecrets after reconnect")
	}

	if _, err := kv.Put(ctx, settings.MasterCurvePubKey, []byte("curve-key-v2")); err != nil {
		t.Fatalf("put curve after reconnect: %v", err)
	}
	select {
	case pub := <-curveUpdates:
		if pub != "curve-key-v2" {
			t.Fatalf("onCurve pub = %q, want curve-key-v2", pub)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for onCurve after reconnect")
	}

	if count := fakeKV.WatcherCount(); count != 1 {
		t.Fatalf("expected 1 active watcher, got %d", count)
	}
}
