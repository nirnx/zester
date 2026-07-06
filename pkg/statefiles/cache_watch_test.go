package statefiles_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"log/slog"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/statefiles"
)

func TestCacheWatch_ReconnectsAfterChannelClose(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	cacheDir := t.TempDir()
	cache := statefiles.NewCache(statefiles.CacheConfig{
		CacheDir:        cacheDir,
		JS:              js,
		Logger:          slog.Default(),
		SyncDebounce:    20 * time.Millisecond,
		SyncJitter:      time.Millisecond,
		RetryBackoffMin: 10 * time.Millisecond,
		RetryBackoffMax: 50 * time.Millisecond,
	})

	cancel, err := cache.Watch(ctx)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer cancel()

	time.Sleep(100 * time.Millisecond)

	// Publish a state file (files + manifest + revision bump) to trigger
	// the watcher.
	kv, err := bus.GetBucket(ctx, js, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	if _, err := pub.PublishFiles(ctx, map[string][]byte{
		"webserver/init.zy": []byte("nginx:\n  pkg.installed"),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Wait for the file to appear on disk (via full re-sync).
	deadline := time.Now().Add(2 * time.Second)
	filePath := filepath.Join(cacheDir, "webserver", "init.zy")
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filePath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("state file not cached after initial revision bump: %v", err)
	}

	// Simulate cluster failure.
	fakeKV := js.GetBucket(bus.BucketStateFiles)
	fakeKV.CloseWatchers()
	time.Sleep(2 * time.Second)

	// Publish another file after reconnect.
	if _, err := pub.PublishFiles(ctx, map[string][]byte{
		"webserver/init.zy":  []byte("nginx:\n  pkg.installed"),
		"common/packages.zy": []byte("vim:\n  pkg.installed"),
	}); err != nil {
		t.Fatalf("publish after reconnect: %v", err)
	}

	// Wait for the new file to appear on disk.
	filePath2 := filepath.Join(cacheDir, "common", "packages.zy")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filePath2); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(filePath2); err != nil {
		t.Fatalf("state file not cached after reconnect: %v", err)
	}

	if count := fakeKV.WatcherCount(); count != 1 {
		t.Fatalf("expected 1 active watcher, got %d", count)
	}
}

func TestCacheWatch_IgnoresNonRevisionKeys(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	cacheDir := t.TempDir()
	cache := statefiles.NewCache(statefiles.CacheConfig{
		CacheDir:        cacheDir,
		JS:              js,
		Logger:          slog.Default(),
		SyncDebounce:    20 * time.Millisecond,
		SyncJitter:      time.Millisecond,
		RetryBackoffMin: 10 * time.Millisecond,
		RetryBackoffMax: 50 * time.Millisecond,
	})

	cancel, err := cache.Watch(ctx)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer cancel()

	time.Sleep(100 * time.Millisecond)

	// Write a state file and its manifest WITHOUT bumping revision —
	// should NOT sync.
	kv, err := bus.GetBucket(ctx, js, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	content := []byte("nginx:\n  pkg.installed")
	if _, err := kv.Put(ctx, "webserver/init.zy", content); err != nil {
		t.Fatalf("put: %v", err)
	}
	manifest := statefiles.Manifest{Files: []statefiles.ManifestFile{
		{Key: "webserver/init.zy", SHA256: statefiles.HashFile(content)},
	}}
	encoded, err := bus.Encode(manifest)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	if _, err := kv.Put(ctx, statefiles.KeyManifest, encoded); err != nil {
		t.Fatalf("put manifest: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	filePath := filepath.Join(cacheDir, "webserver", "init.zy")
	if _, err := os.Stat(filePath); err == nil {
		t.Fatal("state file should NOT be cached without a revision bump")
	}

	// Now bump revision — file should appear.
	if err := bus.BumpRevision(ctx, kv); err != nil {
		t.Fatalf("bump revision: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filePath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("state file not cached after revision bump: %v", err)
	}
}

// waitForSyncedRevision polls until the cache reports the wanted last-synced
// revision or the deadline expires.
func waitForSyncedRevision(t *testing.T, cache *statefiles.Cache, want uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cache.LastSyncedRevision() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("last synced revision: got %d, want %d after %s", cache.LastSyncedRevision(), want, timeout)
}

func TestCacheWatch_DebounceCoalescesBursts(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := bus.GetBucket(ctx, js, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	if _, err := pub.PublishFiles(ctx, map[string][]byte{"init.zy": []byte("v1")}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	cache := statefiles.NewCache(statefiles.CacheConfig{
		CacheDir:        filepath.Join(t.TempDir(), "cache"),
		JS:              js,
		Logger:          slog.Default(),
		SyncDebounce:    100 * time.Millisecond,
		SyncJitter:      time.Millisecond,
		RetryBackoffMin: 10 * time.Millisecond,
		RetryBackoffMax: 50 * time.Millisecond,
	})

	cancel, err := cache.Watch(ctx)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer cancel()

	// The watcher replays the existing _revision entry, so an initial sync
	// lands revision 1.
	waitForSyncedRevision(t, cache, 1, 3*time.Second)

	fakeKV := js.GetBucket(bus.BucketStateFiles)
	fakeKV.ResetGetCounts()

	// Burst of 5 revision bumps well inside the debounce window.
	for i := 0; i < 5; i++ {
		if err := bus.BumpRevision(ctx, kv); err != nil {
			t.Fatalf("bump %d: %v", i, err)
		}
	}

	waitForSyncedRevision(t, cache, 6, 3*time.Second)
	time.Sleep(250 * time.Millisecond) // let any straggler invocation finish

	// Each Sync fetches _manifest exactly once, so the manifest Get count is
	// the number of sync attempts triggered by the burst.
	if got := fakeKV.GetCount(statefiles.KeyManifest); got < 1 || got > 2 {
		t.Errorf("sync attempts for 5-bump burst: got %d, want 1 (coalesced, tolerating 2)", got)
	}
}

func TestCacheWatch_InitialReplaySkipsAlreadySyncedRevision(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := bus.GetBucket(ctx, js, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	if _, err := pub.PublishFiles(ctx, map[string][]byte{"init.zy": []byte("v1")}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	cache := statefiles.NewCache(statefiles.CacheConfig{
		CacheDir:     filepath.Join(t.TempDir(), "cache"),
		JS:           js,
		Logger:       slog.Default(),
		SyncDebounce: 20 * time.Millisecond,
		SyncJitter:   time.Millisecond,
	})

	// Startup sync (as the peel does), then start the watcher.
	if _, err := cache.Sync(ctx); err != nil {
		t.Fatalf("startup sync: %v", err)
	}
	waitForSyncedRevision(t, cache, 1, time.Second)

	fakeKV := js.GetBucket(bus.BucketStateFiles)
	fakeKV.ResetGetCounts()

	cancel, err := cache.Watch(ctx)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer cancel()

	// The initial _revision replay must not trigger a redundant full
	// re-sync: no manifest fetch after the debounce window.
	time.Sleep(300 * time.Millisecond)
	if got := fakeKV.GetCount(statefiles.KeyManifest); got != 0 {
		t.Errorf("initial replay triggered %d redundant sync(s), want 0", got)
	}

	// A real bump still syncs.
	if err := bus.BumpRevision(ctx, kv); err != nil {
		t.Fatalf("bump: %v", err)
	}
	waitForSyncedRevision(t, cache, 2, 3*time.Second)
}

func TestCacheWatch_RetriesFailedSyncUntilRevisionSynced(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := bus.GetBucket(ctx, js, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	// Publish a manifest whose digest expects "v2" while the bucket holds
	// "v1": every sync attempt fails on the hash mismatch.
	if _, err := kv.Put(ctx, "init.zy", []byte("v1")); err != nil {
		t.Fatalf("put: %v", err)
	}
	manifest := statefiles.Manifest{Files: []statefiles.ManifestFile{
		{Key: "init.zy", SHA256: statefiles.HashFile([]byte("v2"))},
	}}
	encoded, err := bus.Encode(manifest)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	if _, err := kv.Put(ctx, statefiles.KeyManifest, encoded); err != nil {
		t.Fatalf("put manifest: %v", err)
	}
	if err := bus.BumpRevision(ctx, kv); err != nil {
		t.Fatalf("bump: %v", err)
	}

	cacheDir := filepath.Join(t.TempDir(), "cache")
	cache := statefiles.NewCache(statefiles.CacheConfig{
		CacheDir:        cacheDir,
		JS:              js,
		Logger:          slog.Default(),
		SyncDebounce:    20 * time.Millisecond,
		SyncJitter:      time.Millisecond,
		RetryBackoffMin: 10 * time.Millisecond,
		RetryBackoffMax: 30 * time.Millisecond,
	})

	cancel, err := cache.Watch(ctx)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer cancel()

	// Let a few failing attempts happen.
	time.Sleep(150 * time.Millisecond)
	if got := cache.LastSyncedRevision(); got != 0 {
		t.Fatalf("sync should not have succeeded yet, got revision %d", got)
	}

	// Fix the file WITHOUT bumping the revision: only the retry loop can
	// pick this up.
	if _, err := kv.Put(ctx, "init.zy", []byte("v2")); err != nil {
		t.Fatalf("put fix: %v", err)
	}

	waitForSyncedRevision(t, cache, 1, 3*time.Second)

	data, err := os.ReadFile(filepath.Join(cacheDir, "init.zy"))
	if err != nil {
		t.Fatalf("read synced file: %v", err)
	}
	if got := string(data); got != "v2" {
		t.Errorf("synced content: got %q, want %q", got, "v2")
	}
}
