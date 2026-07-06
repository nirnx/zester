package statefiles_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/statefiles"
)

// manifestTestSetup initializes storage and returns the state-files KV plus a
// cache whose dir is a named subdirectory of a fresh temp dir (so swap litter
// in the parent is observable).
func manifestTestSetup(t *testing.T) (context.Context, bus.KV, *statefiles.Cache, string) {
	t.Helper()
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := bus.GetBucket(ctx, js, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	cache := statefiles.NewCache(statefiles.CacheConfig{
		CacheDir: cacheDir,
		JS:       js,
	})
	return ctx, kv, cache, cacheDir
}

// treeOf returns the sorted relative file paths under dir ("" if dir missing).
func treeOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return files
}

func TestSyncManifestModeFetchesExactSet(t *testing.T) {
	ctx, kv, cache, cacheDir := manifestTestSetup(t)

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	if _, err := pub.PublishFiles(ctx, map[string][]byte{
		"init.zy":           []byte("top"),
		"webserver/init.zy": []byte("nginx"),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// A stray key that is NOT in the manifest must not be fetched.
	if _, err := kv.Put(ctx, "stray.zy", []byte("stray")); err != nil {
		t.Fatalf("put stray: %v", err)
	}

	count, err := cache.Sync(ctx)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if count != 2 {
		t.Errorf("count: got %d, want 2", count)
	}

	got := treeOf(t, cacheDir)
	want := map[string]string{"init.zy": "top", "webserver/init.zy": "nginx"}
	if len(got) != len(want) {
		t.Fatalf("tree: got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("tree[%s]: got %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["stray.zy"]; ok {
		t.Error("stray.zy (not in manifest) should not be synced")
	}
	if _, ok := got[statefiles.KeyManifest]; ok {
		t.Error("_manifest must never be written to disk")
	}
	if _, ok := got[bus.KeyRevision]; ok {
		t.Error("_revision must never be written to disk")
	}
	if got := cache.LastSyncedRevision(); got != 1 {
		t.Errorf("last synced revision: got %d, want 1", got)
	}
}

func TestSyncManifestPrunesRemovedFiles(t *testing.T) {
	ctx, kv, cache, cacheDir := manifestTestSetup(t)

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	if _, err := pub.PublishFiles(ctx, map[string][]byte{
		"keep.zy":    []byte("keep"),
		"removed.zy": []byte("old formula"),
	}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := cache.Sync(ctx); err != nil {
		t.Fatalf("sync v1: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "removed.zy")); err != nil {
		t.Fatalf("removed.zy should exist after first sync: %v", err)
	}

	// Republish without removed.zy: the next sync must prune it locally.
	if _, err := pub.PublishFiles(ctx, map[string][]byte{
		"keep.zy": []byte("keep v2"),
	}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	if _, err := cache.Sync(ctx); err != nil {
		t.Fatalf("sync v2: %v", err)
	}

	got := treeOf(t, cacheDir)
	if _, ok := got["removed.zy"]; ok {
		t.Error("removed.zy should be pruned from the local cache")
	}
	if got["keep.zy"] != "keep v2" {
		t.Errorf("keep.zy: got %q, want %q", got["keep.zy"], "keep v2")
	}
}

func TestSyncManifestHashMismatchFailsAndKeepsOldTree(t *testing.T) {
	ctx, kv, cache, cacheDir := manifestTestSetup(t)

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	if _, err := pub.PublishFiles(ctx, map[string][]byte{"init.zy": []byte("v1")}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := cache.Sync(ctx); err != nil {
		t.Fatalf("sync v1: %v", err)
	}

	// Corrupt the file behind the manifest's back and bump.
	if _, err := kv.Put(ctx, "init.zy", []byte("corrupted")); err != nil {
		t.Fatalf("put corrupt: %v", err)
	}
	if err := bus.BumpRevision(ctx, kv); err != nil {
		t.Fatalf("bump: %v", err)
	}

	if _, err := cache.Sync(ctx); err == nil {
		t.Fatal("sync should fail on hash mismatch")
	} else if !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(cacheDir, "init.zy"))
	if err != nil {
		t.Fatalf("read after failed sync: %v", err)
	}
	if got := string(data); got != "v1" {
		t.Errorf("failed sync must keep old tree: got %q, want %q", got, "v1")
	}
	if got := cache.LastSyncedRevision(); got != 1 {
		t.Errorf("last synced revision must not advance on failure: got %d, want 1", got)
	}
}

func TestSyncAtomicMidDownloadFailureKeepsOldCompleteTree(t *testing.T) {
	ctx, kv, cache, cacheDir := manifestTestSetup(t)
	parent := filepath.Dir(cacheDir)

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	if _, err := pub.PublishFiles(ctx, map[string][]byte{
		"a.zy": []byte("a v1"),
		"b.zy": []byte("b v1"),
	}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := cache.Sync(ctx); err != nil {
		t.Fatalf("sync v1: %v", err)
	}

	// Craft a manifest whose second (sorted) entry references a key missing
	// from the bucket: a.zy stages fine, zz.zy fails mid-download.
	if _, err := kv.Put(ctx, "a.zy", []byte("a v2")); err != nil {
		t.Fatalf("put a v2: %v", err)
	}
	manifest := statefiles.Manifest{Files: []statefiles.ManifestFile{
		{Key: "a.zy", SHA256: statefiles.HashFile([]byte("a v2"))},
		{Key: "zz.zy", SHA256: statefiles.HashFile([]byte("zz"))},
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

	if _, err := cache.Sync(ctx); err == nil {
		t.Fatal("sync should fail on missing manifest file")
	}

	// The live tree must be the OLD complete set — not a v1/v2 mixture.
	got := treeOf(t, cacheDir)
	want := map[string]string{"a.zy": "a v1", "b.zy": "b v1"}
	if len(got) != len(want) || got["a.zy"] != want["a.zy"] || got["b.zy"] != want["b.zy"] {
		t.Fatalf("tree after failed sync: got %v, want %v", got, want)
	}

	// No staging or .old litter next to the cache dir.
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read parent: %v", err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(cacheDir) {
			t.Errorf("unexpected litter in cache parent: %s", e.Name())
		}
	}

	// Complete the file set: the same sync now succeeds and the swapped-in
	// tree is exactly the NEW complete set (b.zy pruned).
	if _, err := kv.Put(ctx, "zz.zy", []byte("zz")); err != nil {
		t.Fatalf("put zz: %v", err)
	}
	count, err := cache.Sync(ctx)
	if err != nil {
		t.Fatalf("sync v2: %v", err)
	}
	if count != 2 {
		t.Errorf("count: got %d, want 2", count)
	}
	got = treeOf(t, cacheDir)
	want = map[string]string{"a.zy": "a v2", "zz.zy": "zz"}
	if len(got) != len(want) || got["a.zy"] != want["a.zy"] || got["zz.zy"] != want["zz.zy"] {
		t.Fatalf("tree after recovered sync: got %v, want %v", got, want)
	}
}

func TestSyncFirstSyncCreatesCacheDir(t *testing.T) {
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
	if _, err := pub.PublishFiles(ctx, map[string][]byte{"init.zy": []byte("first")}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Deeply nested, nonexistent cache dir: first sync must create it.
	cacheDir := filepath.Join(t.TempDir(), "nested", "deeper", "cache")
	cache := statefiles.NewCache(statefiles.CacheConfig{CacheDir: cacheDir, JS: js})

	count, err := cache.Sync(ctx)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}
	data, err := os.ReadFile(filepath.Join(cacheDir, "init.zy"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(data); got != "first" {
		t.Errorf("content: got %q, want %q", got, "first")
	}
}

// TestSyncFilesWithoutManifestFails verifies that a bucket holding file keys
// but no _manifest key (torn mid-publish window, or tampering) fails the
// sync without touching the local cache, and that the same sync succeeds
// once the manifest lands — the retry loop's self-heal path.
func TestSyncFilesWithoutManifestFails(t *testing.T) {
	ctx, kv, cache, cacheDir := manifestTestSetup(t)

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	if _, err := pub.PublishFiles(ctx, map[string][]byte{"a.zy": []byte("a v1")}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := cache.Sync(ctx); err != nil {
		t.Fatalf("sync v1: %v", err)
	}

	// Simulate the torn window of a publish: new file keys are written but
	// the manifest is not there yet.
	if _, err := kv.Put(ctx, "b.zy", []byte("b v2")); err != nil {
		t.Fatalf("put b.zy: %v", err)
	}
	if err := kv.Delete(ctx, statefiles.KeyManifest); err != nil {
		t.Fatalf("delete manifest: %v", err)
	}
	if err := bus.BumpRevision(ctx, kv); err != nil {
		t.Fatalf("bump: %v", err)
	}

	if _, err := cache.Sync(ctx); err == nil {
		t.Fatal("sync should fail when file keys exist without a manifest")
	} else if !strings.Contains(err.Error(), statefiles.KeyManifest) {
		t.Fatalf("unexpected error: %v", err)
	}

	// The failed sync must leave the previous tree intact.
	got := treeOf(t, cacheDir)
	if len(got) != 1 || got["a.zy"] != "a v1" {
		t.Fatalf("failed sync must keep old tree: got %v", got)
	}
	if got := cache.LastSyncedRevision(); got != 1 {
		t.Errorf("last synced revision must not advance on failure: got %d, want 1", got)
	}

	// The manifest landing (publish completing) self-heals the next sync.
	if _, err := pub.PublishFiles(ctx, map[string][]byte{"b.zy": []byte("b v2")}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	count, err := cache.Sync(ctx)
	if err != nil {
		t.Fatalf("sync after manifest lands: %v", err)
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}
	got = treeOf(t, cacheDir)
	if len(got) != 1 || got["b.zy"] != "b v2" {
		t.Fatalf("tree after recovered sync: got %v", got)
	}
}

// TestSyncEmptyBucketNoManifestIsNoOp verifies the first-boot shape: a
// bucket with no file keys and no manifest (before the master's first
// publish) syncs as a clean no-op and never touches an existing local cache.
func TestSyncEmptyBucketNoManifestIsNoOp(t *testing.T) {
	ctx, kv, cache, cacheDir := manifestTestSetup(t)

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	if _, err := pub.PublishFiles(ctx, map[string][]byte{"a.zy": []byte("a")}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := cache.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// A purged bucket (no file keys, no manifest) carries no deletion
	// intent — the local cache must be left untouched.
	if err := kv.Delete(ctx, "a.zy"); err != nil {
		t.Fatalf("delete a.zy: %v", err)
	}
	if err := kv.Delete(ctx, statefiles.KeyManifest); err != nil {
		t.Fatalf("delete manifest: %v", err)
	}
	if err := bus.BumpRevision(ctx, kv); err != nil {
		t.Fatalf("bump: %v", err)
	}

	count, err := cache.Sync(ctx)
	if err != nil {
		t.Fatalf("no-op sync: %v", err)
	}
	if count != 0 {
		t.Errorf("count: got %d, want 0", count)
	}
	got := treeOf(t, cacheDir)
	if got["a.zy"] != "a" {
		t.Fatalf("files-less manifest-less sync must keep local cache: got %v", got)
	}
	if got := cache.LastSyncedRevision(); got != 2 {
		t.Errorf("no-op sync should record the current revision: got %d, want 2", got)
	}
}
