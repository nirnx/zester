package statefiles_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/statefiles"
)

func TestPublish(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	// Create temp dir with .zy files including nested dirs.
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "webserver"), 0755)
	os.WriteFile(filepath.Join(dir, "init.zy"), []byte("top-level"), 0644)
	os.WriteFile(filepath.Join(dir, "webserver", "init.zy"), []byte("webserver-init"), 0644)
	os.WriteFile(filepath.Join(dir, "webserver", "config.zy"), []byte("webserver-config"), 0644)

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: dir,
		KV:        kv,
	})

	res, err := pub.Publish(ctx)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.Files != 3 {
		t.Errorf("count: got %d, want 3", res.Files)
	}

	// Verify KV keys and values.
	tests := []struct {
		key  string
		want string
	}{
		{"init.zy", "top-level"},
		{"webserver/init.zy", "webserver-init"},
		{"webserver/config.zy", "webserver-config"},
	}
	for _, tt := range tests {
		entry, err := kv.Get(ctx, tt.key)
		if err != nil {
			t.Errorf("get %q: %v", tt.key, err)
			continue
		}
		if got := string(entry.Value()); got != tt.want {
			t.Errorf("key %q: got %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestPublishSkipsHiddenAndVCS(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	dir := t.TempDir()
	// Visible files of various types should be published.
	os.WriteFile(filepath.Join(dir, "state.zy"), []byte("state"), 0644)
	os.WriteFile(filepath.Join(dir, "defaults.yaml"), []byte("defaults"), 0644)
	os.WriteFile(filepath.Join(dir, "helper.star"), []byte("star"), 0644)
	// Hidden files and VCS dirs should be skipped.
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignore"), 0644)
	os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("git-config"), 0644)
	os.MkdirAll(filepath.Join(dir, "__pycache__"), 0755)
	os.WriteFile(filepath.Join(dir, "__pycache__", "mod.pyc"), []byte("pyc"), 0644)

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: dir,
		KV:        kv,
	})

	res, err := pub.Publish(ctx)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.Files != 3 {
		t.Errorf("count: got %d, want 3", res.Files)
	}

	// Verify visible files were published.
	for _, key := range []string{"state.zy", "defaults.yaml", "helper.star"} {
		if _, err := kv.Get(ctx, key); err != nil {
			t.Errorf("expected %q to be published: %v", key, err)
		}
	}
}

func TestPublishEmptyDir(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	dir := t.TempDir()

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: dir,
		KV:        kv,
	})

	res, err := pub.Publish(ctx)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.Files != 0 {
		t.Errorf("count: got %d, want 0", res.Files)
	}
}

func TestPublishBumpsRevision(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "init.zy"), []byte("state"), 0644)

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: dir,
		KV:        kv,
	})

	res, err := pub.Publish(ctx)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !res.Changed {
		t.Error("first publish must report Changed")
	}

	// Verify _revision exists and equals "1".
	entry, err := kv.Get(ctx, bus.KeyRevision)
	if err != nil {
		t.Fatalf("get revision: %v", err)
	}
	if got := string(entry.Value()); got != "1" {
		t.Errorf("revision: got %q, want %q", got, "1")
	}

	// HASH-GATE: republishing the identical tree writes nothing — the
	// revision must NOT bump (no peel resyncs on a no-op republish tick).
	res, err = pub.Publish(ctx)
	if err != nil {
		t.Fatalf("publish 2: %v", err)
	}
	if res.Changed {
		t.Error("unchanged republish must report Changed=false")
	}
	entry, err = kv.Get(ctx, bus.KeyRevision)
	if err != nil {
		t.Fatalf("get revision 2: %v", err)
	}
	if got := string(entry.Value()); got != "1" {
		t.Errorf("revision after unchanged republish: got %q, want %q (gate must skip the bump)", got, "1")
	}

	// A CONTENT change publishes and bumps.
	os.WriteFile(filepath.Join(dir, "init.zy"), []byte("state v2"), 0644)
	res, err = pub.Publish(ctx)
	if err != nil {
		t.Fatalf("publish 3: %v", err)
	}
	if !res.Changed {
		t.Error("changed republish must report Changed")
	}
	entry, _ = kv.Get(ctx, bus.KeyRevision)
	if got := string(entry.Value()); got != "2" {
		t.Errorf("revision after change: got %q, want %q", got, "2")
	}

	// PublishForce bypasses the gate even with identical content — the
	// operator's tamper-heal path rewrites everything and bumps.
	res, err = pub.PublishForce(ctx)
	if err != nil {
		t.Fatalf("force publish: %v", err)
	}
	if !res.Changed {
		t.Error("forced publish must report Changed")
	}
	entry, _ = kv.Get(ctx, bus.KeyRevision)
	if got := string(entry.Value()); got != "3" {
		t.Errorf("revision after force: got %q, want %q", got, "3")
	}
}

func TestPublishFilesWritesSortedHashedManifest(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	files := map[string][]byte{
		"webserver/init.zy": []byte("nginx"),
		"init.zy":           []byte("top"),
	}
	if _, err := pub.PublishFiles(ctx, files); err != nil {
		t.Fatalf("publish: %v", err)
	}

	entry, err := kv.Get(ctx, statefiles.KeyManifest)
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	var m statefiles.Manifest
	if err := bus.Decode(entry.Value(), &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(m.Files) != 2 {
		t.Fatalf("manifest entries: got %d, want 2", len(m.Files))
	}
	// Sorted by key.
	if m.Files[0].Key != "init.zy" || m.Files[1].Key != "webserver/init.zy" {
		t.Errorf("manifest not sorted: %+v", m.Files)
	}
	for _, f := range m.Files {
		if want := statefiles.HashFile(files[f.Key]); f.SHA256 != want {
			t.Errorf("manifest hash for %s: got %s, want %s", f.Key, f.SHA256, want)
		}
	}
}

func TestPublishFilesWritesManifestBeforeRevisionBump(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	if _, err := pub.PublishFiles(ctx, map[string][]byte{"init.zy": []byte("v1")}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// The KV entry revision (bucket sequence) of _manifest must precede the
	// one of _revision: a watcher reacting to the revision bump always finds
	// a manifest covering the batch.
	manifestEntry, err := kv.Get(ctx, statefiles.KeyManifest)
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	revisionEntry, err := kv.Get(ctx, bus.KeyRevision)
	if err != nil {
		t.Fatalf("get revision: %v", err)
	}
	fileEntry, err := kv.Get(ctx, "init.zy")
	if err != nil {
		t.Fatalf("get file: %v", err)
	}
	if !(fileEntry.Revision() < manifestEntry.Revision()) {
		t.Errorf("file (seq %d) must be written before manifest (seq %d)", fileEntry.Revision(), manifestEntry.Revision())
	}
	if !(manifestEntry.Revision() < revisionEntry.Revision()) {
		t.Errorf("manifest (seq %d) must be written before revision bump (seq %d)", manifestEntry.Revision(), revisionEntry.Revision())
	}
}

func TestPublishFilesDeletesStaleKeys(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	if _, err := pub.PublishFiles(ctx, map[string][]byte{
		"keep.zy":    []byte("keep"),
		"removed.zy": []byte("old"),
	}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}

	// Second publish without removed.zy: its KV key must be deleted, the
	// meta keys must survive.
	if _, err := pub.PublishFiles(ctx, map[string][]byte{
		"keep.zy": []byte("keep v2"),
	}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	if _, err := kv.Get(ctx, "removed.zy"); err == nil {
		t.Error("removed.zy should be deleted from the bucket")
	}
	if _, err := kv.Get(ctx, "keep.zy"); err != nil {
		t.Errorf("keep.zy should survive: %v", err)
	}
	if _, err := kv.Get(ctx, statefiles.KeyManifest); err != nil {
		t.Errorf("_manifest must never be deleted: %v", err)
	}
	if _, err := kv.Get(ctx, bus.KeyRevision); err != nil {
		t.Errorf("_revision must never be deleted: %v", err)
	}
}

func TestPublishFilesRefusesEmptySetOverExistingFiles(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	if _, err := pub.PublishFiles(ctx, map[string][]byte{"init.zy": []byte("v1")}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Empty publish over a populated bucket is refused by default (it would
	// wipe every peel's cache).
	if _, err := pub.PublishFiles(ctx, map[string][]byte{}); err == nil {
		t.Fatal("empty publish over populated bucket should be refused")
	}
	if _, err := kv.Get(ctx, "init.zy"); err != nil {
		t.Errorf("init.zy must survive the refused publish: %v", err)
	}

	// With AllowEmpty the wipe is explicit and proceeds.
	forcePub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv, AllowEmpty: true})
	if _, err := forcePub.PublishFiles(ctx, map[string][]byte{}); err != nil {
		t.Fatalf("AllowEmpty publish: %v", err)
	}
	if _, err := kv.Get(ctx, "init.zy"); err == nil {
		t.Error("init.zy should be deleted by the forced empty publish")
	}
}

func TestPublishPreservesPath(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a", "b", "c"), 0755)
	os.WriteFile(filepath.Join(dir, "a", "b", "c", "deep.zy"), []byte("deep"), 0644)

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: dir,
		KV:        kv,
	})

	res, err := pub.Publish(ctx)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.Files != 1 {
		t.Errorf("count: got %d, want 1", res.Files)
	}

	// Verify forward-slash key.
	entry, err := kv.Get(ctx, "a/b/c/deep.zy")
	if err != nil {
		t.Fatalf("get key: %v", err)
	}
	if got := string(entry.Value()); got != "deep" {
		t.Errorf("value: got %q, want %q", got, "deep")
	}
}

// TestGateIntegrity_TornPublishHeals: a publish interrupted between the
// _manifest Put and the _revision bump must NOT be pinned by the hash-gate —
// the next gated publish detects the missing bump and repairs.
func TestGateIntegrity_TornPublishHeals(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatal(err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "init.zy"), []byte("v1"), 0644)
	pub := statefiles.NewPublisher(statefiles.PublisherConfig{StatesDir: dir, KV: kv})

	if _, err := pub.Publish(ctx); err != nil {
		t.Fatal(err)
	}

	// Simulate a TORN publish: the file and manifest for v2 land, but the
	// process dies before BumpRevision.
	os.WriteFile(filepath.Join(dir, "init.zy"), []byte("v2"), 0644)
	if _, err := kv.Put(ctx, "init.zy", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	m := statefiles.BuildManifest(map[string][]byte{"init.zy": []byte("v2")})
	encoded, _ := bus.Encode(m)
	if _, err := kv.Put(ctx, statefiles.KeyManifest, encoded); err != nil {
		t.Fatal(err)
	}
	revBefore := bus.GetRevision(ctx, kv)

	// The gated publish sees a byte-identical manifest BUT detects the
	// missing bump (revision entry older than the manifest entry) and
	// completes the publish.
	res, err := pub.Publish(ctx)
	if err != nil {
		t.Fatalf("healing publish: %v", err)
	}
	if !res.Changed {
		t.Fatal("torn publish was pinned by the hash-gate (Changed=false)")
	}
	if rev := bus.GetRevision(ctx, kv); rev <= revBefore {
		t.Fatalf("revision not bumped by the healing publish: %d <= %d", rev, revBefore)
	}

	// And now the gate holds again.
	res, err = pub.Publish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Error("healed bucket must gate subsequent identical publishes")
	}
}

// TestGateIntegrity_MissingListedKeyHeals: a manifest-listed file key deleted
// out from under the manifest (dual-leader prune casualty) is detected and
// repaired by the next gated publish.
func TestGateIntegrity_MissingListedKeyHeals(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatal(err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "init.zy"), []byte("v1"), 0644)
	pub := statefiles.NewPublisher(statefiles.PublisherConfig{StatesDir: dir, KV: kv})
	if _, err := pub.Publish(ctx); err != nil {
		t.Fatal(err)
	}

	// A losing dual-leader's stale-key prune deletes a listed key.
	if err := kv.Delete(ctx, "init.zy"); err != nil {
		t.Fatal(err)
	}

	res, err := pub.Publish(ctx)
	if err != nil {
		t.Fatalf("healing publish: %v", err)
	}
	if !res.Changed {
		t.Fatal("pruned listed key was not repaired (gate skipped)")
	}
	if _, err := kv.Get(ctx, "init.zy"); err != nil {
		t.Fatalf("listed key not restored: %v", err)
	}
}

// TestSealedRoundTrip pins the sealed-replication contract: the publisher
// seals values at Put (EncodeValue) while manifests hash the PLAINTEXT, the
// hash-gate therefore stays deterministic despite randomized NaCl boxes, the
// bucket never stores plaintext, and a cache with the matching DecodeValue
// verifies plaintext hashes and lands plaintext on disk. Tampered ciphertext
// fails the sync.
func TestSealedRoundTrip(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatal(err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketMasterSettings)
	if err != nil {
		t.Fatal(err)
	}

	// The shared account key: every master derives the same encryptor.
	accountKB, err := auth.GenerateKeyBundle(auth.RoleAccount)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := auth.NewEncryptor(accountKB)
	if err != nil {
		t.Fatal(err)
	}

	srcDir := t.TempDir()
	plaintext := "app:\n  db_password: !encrypted \"hunter2\"\n"
	os.WriteFile(filepath.Join(srcDir, "app.zy"), []byte(plaintext), 0644)

	// KEYED manifest hash (HMAC), as the master-settings publisher uses:
	// the stored manifest must NOT be the plaintext SHA-256, or a $KV.>
	// reader could brute-force low-entropy secrets offline.
	hmacKey := []byte("test-account-seed")
	keyedHash := func(data []byte) string {
		mac := hmac.New(sha256.New, hmacKey)
		mac.Write(data)
		return hex.EncodeToString(mac.Sum(nil))
	}
	pub := statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: srcDir,
		KV:        kv,
		EncodeValue: func(_ string, data []byte) ([]byte, error) {
			return enc.Seal(data, enc.PublicKey()) // seal to self = to every master
		},
		HashValue: keyedHash,
	})

	res, err := pub.Publish(ctx)
	if err != nil {
		t.Fatalf("sealed publish: %v", err)
	}
	if !res.Changed || res.Files != 1 {
		t.Fatalf("sealed publish = %+v", res)
	}

	// The bucket must NOT contain the plaintext.
	entry, err := kv.Get(ctx, "app.zy")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(entry.Value()), "hunter2") {
		t.Fatal("plaintext secret leaked into the sealed bucket")
	}

	// ORACLE CLOSED: the stored _manifest must carry the KEYED hash, never
	// the plaintext SHA-256 a $KV.> reader could recompute offline.
	mEntry, err := kv.Get(ctx, statefiles.KeyManifest)
	if err != nil {
		t.Fatal(err)
	}
	var man statefiles.Manifest
	if err := bus.Decode(mEntry.Value(), &man); err != nil {
		t.Fatal(err)
	}
	if len(man.Files) != 1 {
		t.Fatalf("manifest entries: %d", len(man.Files))
	}
	if man.Files[0].SHA256 == statefiles.HashFile([]byte(plaintext)) {
		t.Fatal("manifest stores the UNKEYED plaintext SHA-256 — offline brute-force oracle open")
	}
	if man.Files[0].SHA256 != keyedHash([]byte(plaintext)) {
		t.Fatal("manifest hash is not the keyed HMAC")
	}

	// GATE DETERMINISM: an identical republish must gate to a no-op even
	// though a fresh seal would produce different ciphertext.
	res, err = pub.Publish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatal("identical sealed republish did not hash-gate (manifest must hash plaintext)")
	}

	// The mirror side: decode + verify + land plaintext.
	dstDir := filepath.Join(t.TempDir(), "settings")
	cache := statefiles.NewCache(statefiles.CacheConfig{
		CacheDir: dstDir,
		JS:       js,
		Bucket:   bus.BucketMasterSettings,
		DecodeValue: func(_ string, stored []byte) ([]byte, error) {
			return enc.Open(stored, enc.PublicKey())
		},
		HashValue: keyedHash,
	})
	if _, err := cache.Sync(ctx); err != nil {
		t.Fatalf("sealed sync: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dstDir, "app.zy"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != plaintext {
		t.Fatalf("mirrored plaintext mismatch:\n%s", got)
	}

	// Tampered ciphertext fails the sync (decode error), leaving disk intact.
	if _, err := kv.Put(ctx, "app.zy", []byte("garbage-not-a-box")); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Sync(ctx); err == nil {
		t.Fatal("tampered ciphertext must fail the sync")
	}
	if got, _ := os.ReadFile(filepath.Join(dstDir, "app.zy")); string(got) != plaintext {
		t.Fatal("failed sync must leave the previous plaintext tree intact")
	}
}
