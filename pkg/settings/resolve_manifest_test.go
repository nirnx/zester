package settings_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/settings"
)

const manifestTestTop = "base:\n  '*':\n    - app\n"

// putManifestFor writes a _manifest entry listing exactly the given files
// with their correct content hashes (key-sorted).
func putManifestFor(t *testing.T, ctx context.Context, kv bus.KV, files map[string][]byte) {
	t.Helper()
	entries := make([]settings.ManifestEntry, 0, len(files))
	for k, v := range files {
		sum := sha256.Sum256(v)
		entries = append(entries, settings.ManifestEntry{Key: k, SHA256: hex.EncodeToString(sum[:])})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	if _, err := bus.KVPut(ctx, kv, settings.ManifestKey, entries); err != nil {
		t.Fatalf("put manifest: %v", err)
	}
}

// manifestTestSetup creates the settings-files bucket and a resolver.
func manifestTestSetup(t *testing.T) (*settings.Resolver, bus.KV, context.Context) {
	t.Helper()
	js := bustest.NewFakeJS()
	ctx := context.Background()

	kv, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSettingsFiles})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	resolver, err := settings.NewResolver(settings.ResolverConfig{
		PeelID: "node-01",
		JS:     js,
		Engine: newEngine(t),
	})
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}
	return resolver, kv, ctx
}

// TestResolve_ManifestHappyPath verifies that a consistent manifest-verified
// snapshot resolves normally.
func TestResolve_ManifestHappyPath(t *testing.T) {
	resolver, kv, ctx := manifestTestSetup(t)

	files := map[string][]byte{
		"top.zy": []byte(manifestTestTop),
		"app.zy": []byte("app_name: myapp\n"),
	}
	for k, v := range files {
		if _, err := kv.Put(ctx, k, v); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	putManifestFor(t, ctx, kv, files)

	result, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result["app_name"] != "myapp" {
		t.Errorf("app_name = %v, want myapp", result["app_name"])
	}
}

// TestResolve_ManifestListedFileMissing verifies torn-read protection: a
// file listed in the manifest but absent from KV fails the resolve, nothing
// is cached, and a later resolve succeeds once the file arrives.
func TestResolve_ManifestListedFileMissing(t *testing.T) {
	resolver, kv, ctx := manifestTestSetup(t)

	appContent := []byte("app_name: myapp\n")
	files := map[string][]byte{
		"top.zy": []byte(manifestTestTop),
		"app.zy": appContent,
	}
	// Publish top.zy and the manifest, but NOT app.zy (simulates reading
	// mid-publish or after a partial write).
	if _, err := kv.Put(ctx, "top.zy", files["top.zy"]); err != nil {
		t.Fatalf("put top.zy: %v", err)
	}
	putManifestFor(t, ctx, kv, files)

	_, err := resolver.Resolve(ctx, map[string]any{})
	if err == nil {
		t.Fatal("resolve should fail when a manifest-listed file is missing")
	}
	if !strings.Contains(err.Error(), "listed in manifest but missing") {
		t.Errorf("error = %v, want torn-read message", err)
	}

	// The failed resolve must not have been cached: once the file arrives,
	// the same revision resolves successfully.
	if _, err := kv.Put(ctx, "app.zy", appContent); err != nil {
		t.Fatalf("put app.zy: %v", err)
	}
	result, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve after file arrives: %v", err)
	}
	if result["app_name"] != "myapp" {
		t.Errorf("app_name = %v, want myapp", result["app_name"])
	}
}

// TestResolve_ManifestHashMismatch verifies that content that does not match
// the manifest hash (mixed batches) fails the resolve.
func TestResolve_ManifestHashMismatch(t *testing.T) {
	resolver, kv, ctx := manifestTestSetup(t)

	files := map[string][]byte{
		"top.zy": []byte(manifestTestTop),
		"app.zy": []byte("app_name: myapp\n"),
	}
	for k, v := range files {
		if _, err := kv.Put(ctx, k, v); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	putManifestFor(t, ctx, kv, files)

	// Overwrite app.zy with content from "another batch" without updating
	// the manifest.
	if _, err := kv.Put(ctx, "app.zy", []byte("app_name: tampered\n")); err != nil {
		t.Fatalf("tamper app.zy: %v", err)
	}

	_, err := resolver.Resolve(ctx, map[string]any{})
	if err == nil {
		t.Fatal("resolve should fail on manifest hash mismatch")
	}
	if !strings.Contains(err.Error(), "does not match manifest") {
		t.Errorf("error = %v, want hash-mismatch message", err)
	}
}

// TestResolve_ManifestUnlistedFileLoaded verifies that a file present in KV
// but absent from the manifest (stale key from an older batch) fails the
// resolve rather than being silently applied.
func TestResolve_ManifestUnlistedFileLoaded(t *testing.T) {
	resolver, kv, ctx := manifestTestSetup(t)

	topContent := []byte(manifestTestTop)
	if _, err := kv.Put(ctx, "top.zy", topContent); err != nil {
		t.Fatalf("put top.zy: %v", err)
	}
	// Stale leftover from a previous batch.
	if _, err := kv.Put(ctx, "app.zy", []byte("app_name: stale\n")); err != nil {
		t.Fatalf("put app.zy: %v", err)
	}
	// Manifest lists only top.zy.
	putManifestFor(t, ctx, kv, map[string][]byte{"top.zy": topContent})

	_, err := resolver.Resolve(ctx, map[string]any{})
	if err == nil {
		t.Fatal("resolve should fail when loading a file not listed in the manifest")
	}
	if !strings.Contains(err.Error(), "not listed in manifest") {
		t.Errorf("error = %v, want unlisted-file message", err)
	}
}

// TestResolve_ManifestTopFileMismatch verifies that top.zy itself is
// manifest-verified.
func TestResolve_ManifestTopFileMismatch(t *testing.T) {
	resolver, kv, ctx := manifestTestSetup(t)

	if _, err := kv.Put(ctx, "top.zy", []byte(manifestTestTop)); err != nil {
		t.Fatalf("put top.zy: %v", err)
	}
	// Manifest lists top.zy with the hash of DIFFERENT content.
	putManifestFor(t, ctx, kv, map[string][]byte{"top.zy": []byte("base:\n  '*':\n    - other\n")})

	_, err := resolver.Resolve(ctx, map[string]any{})
	if err == nil {
		t.Fatal("resolve should fail when top.zy does not match the manifest")
	}
	if !strings.Contains(err.Error(), "does not match manifest") {
		t.Errorf("error = %v, want hash-mismatch message", err)
	}
}

// TestResolve_NoManifestWithFilesFails verifies that settings content
// without a _manifest key fails the resolve (torn or tampered bucket —
// every publish writes the manifest before the revision bump), and that the
// same revision resolves cleanly once the manifest lands.
func TestResolve_NoManifestWithFilesFails(t *testing.T) {
	resolver, kv, ctx := manifestTestSetup(t)

	files := map[string][]byte{
		"top.zy": []byte(manifestTestTop),
		"app.zy": []byte("app_name: myapp\n"),
	}
	for k, v := range files {
		if _, err := kv.Put(ctx, k, v); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}

	_, err := resolver.Resolve(ctx, map[string]any{})
	if err == nil {
		t.Fatal("resolve should fail when settings files exist without a manifest")
	}
	if !strings.Contains(err.Error(), settings.ManifestKey) {
		t.Errorf("error = %v, want missing-manifest message", err)
	}

	// The failure must not be cached: the manifest landing (mid-publish
	// window closing) makes the next resolve succeed.
	putManifestFor(t, ctx, kv, files)
	result, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve after manifest lands: %v", err)
	}
	if result["app_name"] != "myapp" {
		t.Errorf("app_name = %v, want myapp", result["app_name"])
	}
}

// TestResolve_NoManifestNoTopFileIsEmpty verifies the first-boot path: a
// bucket with neither settings content nor a manifest (before the master's
// very first publish) resolves to empty settings, not an error.
func TestResolve_NoManifestNoTopFileIsEmpty(t *testing.T) {
	resolver, _, ctx := manifestTestSetup(t)

	result, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve on pristine bucket should succeed: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty settings, got %v", result)
	}
}

// TestResolve_PublisherManifestRoundTrip verifies the full pipeline: files
// published by Publisher.PublishRawFiles (which sanitizes content, writes
// the manifest, and prunes stale keys) resolve cleanly on the peel side,
// including after a second publish that drops a file.
func TestResolve_PublisherManifestRoundTrip(t *testing.T) {
	env := newTestPublisher(t)
	ctx := context.Background()

	files := map[string][]byte{
		"top.zy": []byte(manifestTestTop),
		"app.zy": []byte("app_name: roundtrip\n"),
		"old.zy": []byte("gone: soon\n"),
	}
	if _, err := env.pub.PublishRawFiles(ctx, files); err != nil {
		t.Fatalf("first publish: %v", err)
	}

	resolver, err := settings.NewResolver(settings.ResolverConfig{
		PeelID: "node-01",
		JS:     env.js,
		Engine: newEngine(t),
	})
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	result, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve after first publish: %v", err)
	}
	if result["app_name"] != "roundtrip" {
		t.Errorf("app_name = %v, want roundtrip", result["app_name"])
	}

	// Second publish drops old.zy — pruning must not break resolution.
	delete(files, "old.zy")
	if _, err := env.pub.PublishRawFiles(ctx, files); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if _, err := env.filesKV.Get(ctx, "old.zy"); !errors.Is(err, bus.ErrKeyNotFound) {
		t.Errorf("old.zy should be pruned, err=%v", err)
	}

	result, err = resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve after prune: %v", err)
	}
	if result["app_name"] != "roundtrip" {
		t.Errorf("app_name after prune = %v, want roundtrip", result["app_name"])
	}
}
