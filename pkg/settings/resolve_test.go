package settings_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/settings"
	"github.com/nirnx/zester/pkg/template"
)

func newEngine(t *testing.T) *template.Engine {
	t.Helper()
	eng, err := template.NewEngine(template.EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}
	return eng
}

// TestResolve_SelectiveLoading verifies that Resolve() only fetches top.zy
// and the matched settings files, not every file in the bucket.
func TestResolve_SelectiveLoading(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	kv, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSettingsFiles})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// Populate KV: top.zy targets '*' → app, plus an unused file.
	files := map[string][]byte{
		"top.zy":    []byte("base:\n  '*':\n    - app\n"),
		"app.zy":    []byte("app_name: myapp\n"),
		"unused.zy": []byte("should_not: be_loaded\n"),
	}
	for k, v := range files {
		if _, err := kv.Put(ctx, k, v); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	putManifestFor(t, ctx, kv, files)

	// Get the underlying FakeKV to check get counts.
	fakeKV := js.GetBucket(bus.BucketSettingsFiles)
	if fakeKV == nil {
		t.Fatal("could not get FakeKV for settings-files bucket")
	}
	fakeKV.ResetGetCounts()

	resolver, err := settings.NewResolver(settings.ResolverConfig{
		PeelID: "node-01",
		JS:     js,
		Engine: newEngine(t),
	})
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	result, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if result["app_name"] != "myapp" {
		t.Errorf("app_name = %v, want myapp", result["app_name"])
	}

	// Verify selective loading: top.zy and app.zy fetched, unused.zy never touched.
	if got := fakeKV.GetCount("top.zy"); got != 1 {
		t.Errorf("GetCount(top.zy) = %d, want 1", got)
	}
	if got := fakeKV.GetCount("app.zy"); got != 1 {
		t.Errorf("GetCount(app.zy) = %d, want 1", got)
	}
	if got := fakeKV.GetCount("unused.zy"); got != 0 {
		t.Errorf("GetCount(unused.zy) = %d, want 0", got)
	}
}

// kvWrapJS returns a wrapped bus.KV for one bucket, delegating everything
// else to the underlying JetStreamAPI. Lets tests intercept the resolver's
// KV calls without touching the fake.
type kvWrapJS struct {
	bus.JetStreamAPI
	bucket string
	wrap   func(bus.KV) bus.KV
}

func (w *kvWrapJS) KeyValue(ctx context.Context, bucket string) (bus.KV, error) {
	kv, err := w.JetStreamAPI.KeyValue(ctx, bucket)
	if err != nil || bucket != w.bucket {
		return kv, err
	}
	return w.wrap(kv), nil
}

// revisionGetHookKV invokes onCall with a 1-based counter before every Get of
// the _revision key, so a test can inject side effects (e.g., a publish
// completing) at an exact point inside Resolve.
type revisionGetHookKV struct {
	bus.KV
	calls  *int
	onCall func(n int)
}

func (h *revisionGetHookKV) Get(ctx context.Context, key string) (bus.KVEntry, error) {
	if key == bus.KeyRevision {
		*h.calls++
		if h.onCall != nil {
			h.onCall(*h.calls)
		}
	}
	return h.KV.Get(ctx, key)
}

// TestResolve_PublishMidResolveNotCachedUnderNewRevision reproduces a publish
// completing during Resolve's load/render window: the resolve reads a clean
// OLD batch, but by the time it writes its cache the bucket _revision already
// belongs to the NEW batch. The result must NOT be cached under the new
// revision — otherwise the next (watch-triggered) resolve would be served the
// old batch from cache and the peel would be pinned one publish behind for
// the whole generation.
func TestResolve_PublishMidResolveNotCachedUnderNewRevision(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	kv, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSettingsFiles})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	oldBatch := map[string][]byte{
		"top.zy": []byte("base:\n  '*':\n    - app\n"),
		"app.zy": []byte("value: old\n"),
	}
	for k, v := range oldBatch {
		if _, err := kv.Put(ctx, k, v); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	putManifestFor(t, ctx, kv, oldBatch)
	if err := bus.BumpRevision(ctx, kv); err != nil {
		t.Fatalf("bump revision: %v", err)
	}

	// Resolve reads _revision exactly twice per cache-miss invocation:
	// once at the entry gate, once at cache-write time. Landing the
	// "publish" on the 2nd read simulates a publisher committing between
	// the file loads and the cache write.
	calls := 0
	hooked := &kvWrapJS{
		JetStreamAPI: js,
		bucket:       bus.BucketSettingsFiles,
		wrap: func(inner bus.KV) bus.KV {
			return &revisionGetHookKV{KV: inner, calls: &calls, onCall: func(n int) {
				if n != 2 {
					return
				}
				newBatch := map[string][]byte{
					"top.zy": oldBatch["top.zy"],
					"app.zy": []byte("value: new\n"),
				}
				if _, err := kv.Put(ctx, "app.zy", newBatch["app.zy"]); err != nil {
					t.Errorf("mid-resolve put: %v", err)
				}
				putManifestFor(t, ctx, kv, newBatch)
				if err := bus.BumpRevision(ctx, kv); err != nil {
					t.Errorf("mid-resolve bump: %v", err)
				}
			}}
		},
	}

	resolver, err := settings.NewResolver(settings.ResolverConfig{
		PeelID: "node-01",
		JS:     hooked,
		Engine: newEngine(t),
	})
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	// First resolve reads the old batch; the publish lands mid-resolve.
	res1, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if res1["value"] != "old" {
		t.Fatalf("first resolve value = %v, want old (consistent pre-publish batch)", res1["value"])
	}

	// Second resolve must see the new batch, not a poisoned cache entry
	// keyed to the new revision.
	res2, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if res2["value"] != "new" {
		t.Fatalf("second resolve value = %v, want new (stale batch pinned in cache under new revision)", res2["value"])
	}

	// Third resolve must be a cache hit at the settled revision: no
	// further file reads.
	fakeKV := js.GetBucket(bus.BucketSettingsFiles)
	if fakeKV == nil {
		t.Fatal("could not get FakeKV for settings-files bucket")
	}
	getsBefore := fakeKV.GetCount("app.zy")
	res3, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("third resolve: %v", err)
	}
	if res3["value"] != "new" {
		t.Fatalf("third resolve value = %v, want new", res3["value"])
	}
	if got := fakeKV.GetCount("app.zy"); got != getsBefore {
		t.Errorf("third resolve re-read app.zy (%d -> %d gets), want cache hit at settled revision", getsBefore, got)
	}
}

// TestResolve_TopFileNotFound verifies that Resolve() returns empty settings
// (not an error) when top.zy does not exist in the KV bucket.
func TestResolve_TopFileNotFound(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	// Create the bucket but don't put top.zy.
	if _, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSettingsFiles}); err != nil {
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

	result, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve should succeed with no top.zy, got: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty settings, got %v", result)
	}
}

// TestResolve_MatchingFileNotFound verifies that Resolve() skips missing
// referenced files gracefully (logs warning, doesn't error).
func TestResolve_MatchingFileNotFound(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	kv, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSettingsFiles})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// top.zy references "missing", which exists in neither KV nor the
	// manifest — warn-and-skip, not an error.
	topContent := []byte("base:\n  '*':\n    - missing\n")
	if _, err := kv.Put(ctx, "top.zy", topContent); err != nil {
		t.Fatalf("put top.zy: %v", err)
	}
	putManifestFor(t, ctx, kv, map[string][]byte{"top.zy": topContent})

	resolver, err := settings.NewResolver(settings.ResolverConfig{
		PeelID: "node-01",
		JS:     js,
		Engine: newEngine(t),
	})
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	result, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve should succeed with missing ref, got: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty settings, got %v", result)
	}
}

// setupSecretsPipeline creates the settings-files and secrets buckets, a
// master-side publisher, and a peel-side resolver (peel ID "peel-01") wired
// with real encryptors. Returns the publisher, resolver, and the peel's curve
// public key for PublishSecrets calls.
func setupSecretsPipeline(t *testing.T, js *bustest.FakeJS) (*settings.Publisher, *settings.Resolver, string) {
	t.Helper()
	ctx := context.Background()

	filesKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSettingsFiles})
	if err != nil {
		t.Fatalf("create files bucket: %v", err)
	}
	secretsKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSecrets})
	if err != nil {
		t.Fatalf("create secrets bucket: %v", err)
	}

	masterKB, err := auth.GenerateKeyBundle(auth.RoleOperator)
	if err != nil {
		t.Fatalf("generate master key bundle: %v", err)
	}
	masterEnc, err := auth.NewEncryptor(masterKB)
	if err != nil {
		t.Fatalf("create master encryptor: %v", err)
	}
	peelKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("generate peel key bundle: %v", err)
	}
	peelCurvePub, err := peelKB.CurvePublicKey()
	if err != nil {
		t.Fatalf("peel curve public key: %v", err)
	}
	peelEnc, err := auth.NewEncryptor(peelKB)
	if err != nil {
		t.Fatalf("create peel encryptor: %v", err)
	}

	pub, err := settings.NewPublisher(settings.PublisherConfig{
		SettingsDir:     t.TempDir(),
		FilesKV:         filesKV,
		SecretsKV:       secretsKV,
		MasterEncryptor: masterEnc,
	})
	if err != nil {
		t.Fatalf("create publisher: %v", err)
	}

	resolver, err := settings.NewResolver(settings.ResolverConfig{
		PeelID:    "peel-01",
		JS:        js,
		Engine:    newEngine(t),
		Encryptor: peelEnc,
		SenderPub: masterEnc.PublicKey(),
	})
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	return pub, resolver, peelCurvePub
}

// TestResolve_UnresolvedPlaceholderFails verifies that when a sanitized file
// contains a secret placeholder but no secrets have been published to KV yet
// (the startup race from architecture-review finding 16), Resolve returns an
// error naming the affected key and does NOT cache the placeholder-laden
// result as a success.
func TestResolve_UnresolvedPlaceholderFails(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	pub, resolver, _ := setupSecretsPipeline(t, js)

	rawFiles := map[string][]byte{
		"top.zy": []byte("base:\n  '*':\n    - app\n"),
		"app.zy": []byte("app_name: myapp\ndb_password: !encrypted \"prod-pass\"\n"),
	}
	if _, err := pub.PublishRawFiles(ctx, rawFiles); err != nil {
		t.Fatalf("publish raw files: %v", err)
	}
	// Deliberately do NOT publish secrets: the peel resolves before the
	// master's facts watcher has published its per-peel secrets.

	result, err := resolver.Resolve(ctx, map[string]any{})
	if err == nil {
		t.Fatalf("resolve should fail with unresolved placeholder, got result: %v", result)
	}
	if !strings.Contains(err.Error(), "unresolved secret placeholder") {
		t.Errorf("error should mention unresolved secret placeholders, got: %v", err)
	}
	if !strings.Contains(err.Error(), "db_password") {
		t.Errorf("error should name the affected key db_password, got: %v", err)
	}

	// The failed result must not be cached: a second resolve at the same
	// revision must fail again, not be served a poisoned cached success.
	if _, err := resolver.Resolve(ctx, map[string]any{}); err == nil {
		t.Fatal("second resolve should also fail; a failed resolve must never be cached")
	}
}

// TestResolve_SecretsArriveAfterFailedResolve verifies the recovery path:
// after a failed resolve (placeholders, no secrets), publishing the secrets
// to KV — with NO settings-files revision bump — lets the next Resolve
// succeed, because the failure was never cached. It then verifies that
// InvalidateCache() (the WatchSecrets wiring hook) forces a re-read after a
// secrets-only rotation that the revision gate would otherwise swallow.
func TestResolve_SecretsArriveAfterFailedResolve(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	pub, resolver, peelCurvePub := setupSecretsPipeline(t, js)

	rawFiles := map[string][]byte{
		"top.zy": []byte("base:\n  '*':\n    - app\n"),
		"app.zy": []byte("db_password: !encrypted \"prod-pass\"\n"),
	}
	extracted, err := pub.PublishRawFiles(ctx, rawFiles)
	if err != nil {
		t.Fatalf("publish raw files: %v", err)
	}

	// First resolve races ahead of the secrets publish and must fail.
	if _, err := resolver.Resolve(ctx, map[string]any{}); err == nil {
		t.Fatal("resolve before secrets published should fail")
	}

	// Secrets land (secrets bucket only; settings-files _revision unchanged).
	if err := pub.PublishSecrets(ctx, "peel-01", extracted.Flat(), peelCurvePub); err != nil {
		t.Fatalf("publish secrets: %v", err)
	}

	// Next resolve must succeed with the decrypted value — no InvalidateCache
	// needed, because the failed resolve was never cached.
	result, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve after secrets published: %v", err)
	}
	if result["db_password"] != "prod-pass" {
		t.Errorf("db_password = %v, want prod-pass", result["db_password"])
	}

	// Rotate the secret value (secrets bucket only, revision unchanged):
	// the revision-gated cache serves the old value...
	if err := pub.PublishSecrets(ctx, "peel-01", map[string]string{"db_password": "rotated-pass"}, peelCurvePub); err != nil {
		t.Fatalf("publish rotated secrets: %v", err)
	}
	result, err = resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve after rotation: %v", err)
	}
	if result["db_password"] != "prod-pass" {
		t.Errorf("pre-invalidate db_password = %v, want cached prod-pass", result["db_password"])
	}

	// ...until InvalidateCache — what the WatchSecrets callback calls —
	// forces the next resolve to re-read and pick up the rotated value.
	resolver.InvalidateCache()
	result, err = resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve after InvalidateCache: %v", err)
	}
	if result["db_password"] != "rotated-pass" {
		t.Errorf("post-invalidate db_password = %v, want rotated-pass", result["db_password"])
	}
}

// TestResolve_NoSecretsBehaviorUnchanged verifies that settings without any
// secrets resolve exactly as before: success, and the result is cached (the
// second resolve is served from the revision-gated cache without KV reads).
func TestResolve_NoSecretsBehaviorUnchanged(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	kv, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSettingsFiles})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	files := map[string][]byte{
		"top.zy": []byte("base:\n  '*':\n    - app\n"),
		"app.zy": []byte("app_name: myapp\n"),
	}
	for k, v := range files {
		if _, err := kv.Put(ctx, k, v); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	putManifestFor(t, ctx, kv, files)
	if err := bus.BumpRevision(ctx, kv); err != nil {
		t.Fatalf("bump revision: %v", err)
	}

	fakeKV := js.GetBucket(bus.BucketSettingsFiles)
	if fakeKV == nil {
		t.Fatal("could not get FakeKV for settings-files bucket")
	}
	fakeKV.ResetGetCounts()

	resolver, err := settings.NewResolver(settings.ResolverConfig{
		PeelID: "node-01",
		JS:     js,
		Engine: newEngine(t),
	})
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	result, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result["app_name"] != "myapp" {
		t.Errorf("app_name = %v, want myapp", result["app_name"])
	}

	// Second resolve at the same revision must be a cache hit.
	result2, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if result2["app_name"] != "myapp" {
		t.Errorf("cached app_name = %v, want myapp", result2["app_name"])
	}
	if got := fakeKV.GetCount("top.zy"); got != 1 {
		t.Errorf("GetCount(top.zy) = %d, want 1 (second resolve should hit cache)", got)
	}
}

// TestResolve_NestedPlaceholderDetected verifies the deep scan finds
// placeholders nested inside collections (a map inside a list) and embedded
// inside larger rendered strings, and names each affected key.
func TestResolve_NestedPlaceholderDetected(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	kv, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSettingsFiles})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	nested := settings.SecretPlaceholderPrefix + "db.primary.password" + settings.SecretPlaceholderSuffix
	embedded := settings.SecretPlaceholderPrefix + "api_token" + settings.SecretPlaceholderSuffix
	appZY := fmt.Sprintf(`
databases:
  - name: primary
    password: "%s"
conn_string: "https://api.example.com/?token=%s"
`, nested, embedded)

	files := map[string][]byte{
		"top.zy": []byte("base:\n  '*':\n    - app\n"),
		"app.zy": []byte(appZY),
	}
	for k, v := range files {
		if _, err := kv.Put(ctx, k, v); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	putManifestFor(t, ctx, kv, files)

	resolver, err := settings.NewResolver(settings.ResolverConfig{
		PeelID: "node-01",
		JS:     js,
		Engine: newEngine(t),
	})
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	_, err = resolver.Resolve(ctx, map[string]any{})
	if err == nil {
		t.Fatal("resolve should fail: nested and embedded placeholders survived")
	}
	if !strings.Contains(err.Error(), "db.primary.password") {
		t.Errorf("error should name nested key db.primary.password, got: %v", err)
	}
	if !strings.Contains(err.Error(), "api_token") {
		t.Errorf("error should name embedded key api_token, got: %v", err)
	}
}
