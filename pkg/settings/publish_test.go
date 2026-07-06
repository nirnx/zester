package settings_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/settings"
)

// pubTestEnv bundles the pieces of a publisher test fixture.
type pubTestEnv struct {
	pub       *settings.Publisher
	js        *bustest.FakeJS
	filesKV   bus.KV
	secretsKV bus.KV
	masterEnc *auth.Encryptor
}

// newTestPublisher creates a Publisher backed by fake KV buckets.
func newTestPublisher(t *testing.T) *pubTestEnv {
	t.Helper()
	js := bustest.NewFakeJS()
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

	pub, err := settings.NewPublisher(settings.PublisherConfig{
		SettingsDir:     t.TempDir(),
		FilesKV:         filesKV,
		SecretsKV:       secretsKV,
		MasterEncryptor: masterEnc,
	})
	if err != nil {
		t.Fatalf("create publisher: %v", err)
	}
	return &pubTestEnv{pub: pub, js: js, filesKV: filesKV, secretsKV: secretsKV, masterEnc: masterEnc}
}

// newPeelEncryptor generates a fresh curve key pair standing in for a peel.
func newPeelEncryptor(t *testing.T) *auth.Encryptor {
	t.Helper()
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("generate peel key bundle: %v", err)
	}
	enc, err := auth.NewEncryptor(kb)
	if err != nil {
		t.Fatalf("create peel encryptor: %v", err)
	}
	return enc
}

// TestPublishRawFiles_ManifestSortedWithHashes verifies that PublishRawFiles
// writes a _manifest key listing every published file, sorted by key, with
// the SHA-256 of the content exactly as stored in KV.
func TestPublishRawFiles_ManifestSortedWithHashes(t *testing.T) {
	env := newTestPublisher(t)
	pub, filesKV := env.pub, env.filesKV
	ctx := context.Background()

	files := map[string][]byte{
		"zeta.zy":       []byte("z: 1\n"),
		"alpha.zy":      []byte("a: 1\n"),
		"common/mid.zy": []byte("m: 1\n"),
		"top.zy":        []byte("base:\n  '*':\n    - alpha\n"),
	}
	if _, err := pub.PublishRawFiles(ctx, files); err != nil {
		t.Fatalf("publish: %v", err)
	}

	var manifest []settings.ManifestEntry
	if err := bus.KVGet(ctx, filesKV, settings.ManifestKey, &manifest); err != nil {
		t.Fatalf("get manifest: %v", err)
	}

	if len(manifest) != len(files) {
		t.Fatalf("manifest entries = %d, want %d", len(manifest), len(files))
	}
	wantOrder := []string{"alpha.zy", "common/mid.zy", "top.zy", "zeta.zy"}
	for i, want := range wantOrder {
		if manifest[i].Key != want {
			t.Errorf("manifest[%d].Key = %q, want %q", i, manifest[i].Key, want)
		}
	}

	// Each hash must match the content as stored in KV (sanitized form).
	for _, e := range manifest {
		entry, err := filesKV.Get(ctx, e.Key)
		if err != nil {
			t.Fatalf("get %q: %v", e.Key, err)
		}
		sum := sha256.Sum256(entry.Value())
		if got := hex.EncodeToString(sum[:]); got != e.SHA256 {
			t.Errorf("manifest hash for %q = %s, want %s", e.Key, e.SHA256, got)
		}
	}
}

// TestPublishRawFiles_DeletesStaleKeys verifies that keys from a previous
// publish that are absent from the new batch are deleted, while the
// protected keys (_revision, _manifest, master curve pub) always survive.
func TestPublishRawFiles_DeletesStaleKeys(t *testing.T) {
	env := newTestPublisher(t)
	pub, filesKV := env.pub, env.filesKV
	ctx := context.Background()

	// Master curve pub key lives in the files bucket in some layouts; it
	// must never be pruned regardless.
	if _, err := filesKV.Put(ctx, settings.MasterCurvePubKey, []byte("curve-pub")); err != nil {
		t.Fatalf("put curve pub: %v", err)
	}

	// First publish: two files.
	first := map[string][]byte{
		"keep.zy":    []byte("keep: 1\n"),
		"removed.zy": []byte("removed: 1\n"),
	}
	if _, err := pub.PublishRawFiles(ctx, first); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	revAfterFirst := bus.GetRevision(ctx, filesKV)
	if revAfterFirst == 0 {
		t.Fatal("revision not bumped by first publish")
	}

	// Second publish drops removed.zy.
	second := map[string][]byte{
		"keep.zy": []byte("keep: 2\n"),
	}
	if _, err := pub.PublishRawFiles(ctx, second); err != nil {
		t.Fatalf("second publish: %v", err)
	}

	// Stale key deleted.
	if _, err := filesKV.Get(ctx, "removed.zy"); !errors.Is(err, bus.ErrKeyNotFound) {
		t.Errorf("removed.zy should be deleted, got err=%v", err)
	}

	// Current file, manifest, revision, and curve pub key survive.
	for _, key := range []string{"keep.zy", settings.ManifestKey, bus.KeyRevision, settings.MasterCurvePubKey} {
		if _, err := filesKV.Get(ctx, key); err != nil {
			t.Errorf("protected/current key %q should survive pruning: %v", key, err)
		}
	}

	if rev := bus.GetRevision(ctx, filesKV); rev <= revAfterFirst {
		t.Errorf("revision after second publish = %d, want > %d", rev, revAfterFirst)
	}

	// The new manifest lists only the current batch.
	var manifest []settings.ManifestEntry
	if err := bus.KVGet(ctx, filesKV, settings.ManifestKey, &manifest); err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	if len(manifest) != 1 || manifest[0].Key != "keep.zy" {
		t.Errorf("manifest = %+v, want single keep.zy entry", manifest)
	}
}

// gcFailKV wraps a bus.KV and injects failures into the garbage-collection
// paths of pruneStaleFiles: Delete of selected keys and/or ListKeys.
type gcFailKV struct {
	bus.KV
	failDelete map[string]bool
	failList   bool
}

func (f *gcFailKV) Delete(ctx context.Context, key string, opts ...bus.KVDeleteOpt) error {
	if f.failDelete[key] {
		return fmt.Errorf("injected delete failure for %s", key)
	}
	return f.KV.Delete(ctx, key, opts...)
}

func (f *gcFailKV) ListKeys(ctx context.Context) (bus.KeyLister, error) {
	if f.failList {
		return nil, fmt.Errorf("injected listkeys failure")
	}
	return f.KV.ListKeys(ctx)
}

// newGCFailPublisher builds a Publisher whose files bucket is wrapped in a
// gcFailKV, returning both.
func newGCFailPublisher(t *testing.T) (*settings.Publisher, *gcFailKV, bus.KV) {
	t.Helper()
	env := newTestPublisher(t)
	wrapped := &gcFailKV{KV: env.filesKV, failDelete: map[string]bool{}}
	pub, err := settings.NewPublisher(settings.PublisherConfig{
		SettingsDir:     t.TempDir(),
		FilesKV:         wrapped,
		SecretsKV:       env.secretsKV,
		MasterEncryptor: env.masterEnc,
	})
	if err != nil {
		t.Fatalf("create publisher: %v", err)
	}
	return pub, wrapped, env.filesKV
}

// TestPublishRawFiles_PruneDeleteFailureDoesNotBlockRevisionBump verifies
// that a failed stale-key Delete no longer aborts the publish: the batch's
// _revision must still be bumped (otherwise every warm peel stays pinned on
// the previous generation until the next publisher-lease acquisition), the
// stale key is left behind as inert garbage, and the next successful publish
// re-prunes it.
func TestPublishRawFiles_PruneDeleteFailureDoesNotBlockRevisionBump(t *testing.T) {
	pub, wrapped, filesKV := newGCFailPublisher(t)
	ctx := context.Background()

	first := map[string][]byte{
		"keep.zy":    []byte("keep: 1\n"),
		"removed.zy": []byte("removed: 1\n"),
	}
	if _, err := pub.PublishRawFiles(ctx, first); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	rev1 := bus.GetRevision(ctx, filesKV)
	if rev1 == 0 {
		t.Fatal("revision not bumped by first publish")
	}

	// Second publish drops removed.zy, but its Delete fails.
	wrapped.failDelete["removed.zy"] = true
	second := map[string][]byte{"keep.zy": []byte("keep: 2\n")}
	if _, err := pub.PublishRawFiles(ctx, second); err != nil {
		t.Fatalf("second publish must succeed despite prune failure, got: %v", err)
	}
	rev2 := bus.GetRevision(ctx, filesKV)
	if rev2 <= rev1 {
		t.Errorf("revision after prune-failed publish = %d, want > %d (bump must not be blocked)", rev2, rev1)
	}

	// The un-deletable key survives as inert garbage; the new manifest
	// does not list it, so manifest-verified resolvers never load it.
	if _, err := filesKV.Get(ctx, "removed.zy"); err != nil {
		t.Fatalf("removed.zy should survive the failed prune: %v", err)
	}
	var manifest []settings.ManifestEntry
	if err := bus.KVGet(ctx, filesKV, settings.ManifestKey, &manifest); err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	for _, e := range manifest {
		if e.Key == "removed.zy" {
			t.Error("manifest must not list the dropped file")
		}
	}

	// Third publish with a healthy KV re-prunes the leftover.
	wrapped.failDelete = map[string]bool{}
	if _, err := pub.PublishRawFiles(ctx, second); err != nil {
		t.Fatalf("third publish: %v", err)
	}
	if _, err := filesKV.Get(ctx, "removed.zy"); !errors.Is(err, bus.ErrKeyNotFound) {
		t.Errorf("removed.zy should be re-pruned by the next successful publish, got err=%v", err)
	}
	if rev3 := bus.GetRevision(ctx, filesKV); rev3 <= rev2 {
		t.Errorf("revision after third publish = %d, want > %d", rev3, rev2)
	}
}

// TestPublishRawFiles_PruneListFailureDoesNotBlockRevisionBump verifies that
// even a transient ListKeys failure during prune leaves the publish (and the
// revision bump) intact.
func TestPublishRawFiles_PruneListFailureDoesNotBlockRevisionBump(t *testing.T) {
	pub, wrapped, filesKV := newGCFailPublisher(t)
	ctx := context.Background()

	if _, err := pub.PublishRawFiles(ctx, map[string][]byte{"a.zy": []byte("a: 1\n")}); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	rev1 := bus.GetRevision(ctx, filesKV)

	wrapped.failList = true
	if _, err := pub.PublishRawFiles(ctx, map[string][]byte{"b.zy": []byte("b: 1\n")}); err != nil {
		t.Fatalf("publish must succeed despite listkeys failure, got: %v", err)
	}
	if rev2 := bus.GetRevision(ctx, filesKV); rev2 <= rev1 {
		t.Errorf("revision after list-failed publish = %d, want > %d", rev2, rev1)
	}
}

// TestPublishSecrets_HashGate verifies the skip-cache: identical inputs do
// not re-encrypt or rewrite KV; changed secrets or a rotated recipient curve
// key force a republish; InvalidateSecretsCache forces one too.
func TestPublishSecrets_HashGate(t *testing.T) {
	env := newTestPublisher(t)
	pub, secretsKV := env.pub, env.secretsKV
	ctx := context.Background()

	peelEnc := newPeelEncryptor(t)
	peelID := "peel-01"
	secrets := map[string]string{"db_password": "hunter2", "api_key": "k-1"}

	kvRev := func() uint64 {
		t.Helper()
		entry, err := secretsKV.Get(ctx, peelID)
		if err != nil {
			t.Fatalf("get secrets entry: %v", err)
		}
		return entry.Revision()
	}

	// First publish writes.
	if err := pub.PublishSecrets(ctx, peelID, secrets, peelEnc.PublicKey()); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	rev1 := kvRev()

	// Same secrets + same key → skipped, no new KV revision.
	if err := pub.PublishSecrets(ctx, peelID, secrets, peelEnc.PublicKey()); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if rev2 := kvRev(); rev2 != rev1 {
		t.Errorf("unchanged secrets caused a KV write: rev %d -> %d", rev1, rev2)
	}

	// Changed secret value → republished.
	secrets["db_password"] = "hunter3"
	if err := pub.PublishSecrets(ctx, peelID, secrets, peelEnc.PublicKey()); err != nil {
		t.Fatalf("publish changed secret: %v", err)
	}
	rev3 := kvRev()
	if rev3 == rev1 {
		t.Error("changed secret did not cause a KV write")
	}

	// Rotated recipient curve key → republished even with same secrets.
	rotatedEnc := newPeelEncryptor(t)
	if err := pub.PublishSecrets(ctx, peelID, secrets, rotatedEnc.PublicKey()); err != nil {
		t.Fatalf("publish rotated key: %v", err)
	}
	rev4 := kvRev()
	if rev4 == rev3 {
		t.Error("rotated recipient curve key did not cause a KV write")
	}

	// Unchanged again → skipped.
	if err := pub.PublishSecrets(ctx, peelID, secrets, rotatedEnc.PublicKey()); err != nil {
		t.Fatalf("publish unchanged after rotation: %v", err)
	}
	if rev5 := kvRev(); rev5 != rev4 {
		t.Errorf("unchanged secrets after rotation caused a KV write: rev %d -> %d", rev4, rev5)
	}

	// Explicit invalidation → forced republish.
	pub.InvalidateSecretsCache(peelID)
	if err := pub.PublishSecrets(ctx, peelID, secrets, rotatedEnc.PublicKey()); err != nil {
		t.Fatalf("publish after invalidation: %v", err)
	}
	if rev6 := kvRev(); rev6 == rev4 {
		t.Error("InvalidateSecretsCache did not force a republish")
	}
}

// TestPublishSecrets_RotatedMasterKeyRepublishes verifies that a Publisher
// built with a rotated master (sender) curve key republishes even when the
// recipient key and secret map are identical: the sender key is part of the
// fingerprint, so a fresh Publisher after rotation never skips.
func TestPublishSecrets_RotatedMasterKeyRepublishes(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	filesKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSettingsFiles})
	if err != nil {
		t.Fatalf("create files bucket: %v", err)
	}
	secretsKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSecrets})
	if err != nil {
		t.Fatalf("create secrets bucket: %v", err)
	}

	peelEnc := newPeelEncryptor(t)
	peelID := "peel-01"
	secrets := map[string]string{"db_password": "hunter2"}

	newPub := func() *settings.Publisher {
		t.Helper()
		kb, err := auth.GenerateKeyBundle(auth.RoleOperator)
		if err != nil {
			t.Fatalf("generate key bundle: %v", err)
		}
		enc, err := auth.NewEncryptor(kb)
		if err != nil {
			t.Fatalf("create encryptor: %v", err)
		}
		p, err := settings.NewPublisher(settings.PublisherConfig{
			SettingsDir:     t.TempDir(),
			FilesKV:         filesKV,
			SecretsKV:       secretsKV,
			MasterEncryptor: enc,
		})
		if err != nil {
			t.Fatalf("create publisher: %v", err)
		}
		return p
	}

	if err := newPub().PublishSecrets(ctx, peelID, secrets, peelEnc.PublicKey()); err != nil {
		t.Fatalf("publish with first master key: %v", err)
	}
	entry1, err := secretsKV.Get(ctx, peelID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// Rotated master key (fresh Publisher, fresh Encryptor) → must write.
	if err := newPub().PublishSecrets(ctx, peelID, secrets, peelEnc.PublicKey()); err != nil {
		t.Fatalf("publish with rotated master key: %v", err)
	}
	entry2, err := secretsKV.Get(ctx, peelID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if entry2.Revision() == entry1.Revision() {
		t.Error("rotated master curve key did not cause a republish")
	}
}

// flakyKV wraps a bus.KV and fails the next Put on demand.
type flakyKV struct {
	bus.KV
	failNext bool
}

func (f *flakyKV) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	if f.failNext {
		f.failNext = false
		return 0, fmt.Errorf("injected put failure")
	}
	return f.KV.Put(ctx, key, value)
}

// TestPublishSecrets_FailedPutNotCached verifies that a failed KV put does
// not populate the skip-cache: the next call with identical inputs must
// retry the publish instead of being skipped.
func TestPublishSecrets_FailedPutNotCached(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	filesKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSettingsFiles})
	if err != nil {
		t.Fatalf("create files bucket: %v", err)
	}
	secretsKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSecrets})
	if err != nil {
		t.Fatalf("create secrets bucket: %v", err)
	}
	flaky := &flakyKV{KV: secretsKV, failNext: true}

	masterKB, err := auth.GenerateKeyBundle(auth.RoleOperator)
	if err != nil {
		t.Fatalf("generate master key bundle: %v", err)
	}
	masterEnc, err := auth.NewEncryptor(masterKB)
	if err != nil {
		t.Fatalf("create master encryptor: %v", err)
	}

	pub, err := settings.NewPublisher(settings.PublisherConfig{
		SettingsDir:     t.TempDir(),
		FilesKV:         filesKV,
		SecretsKV:       flaky,
		MasterEncryptor: masterEnc,
	})
	if err != nil {
		t.Fatalf("create publisher: %v", err)
	}

	peelEnc := newPeelEncryptor(t)
	peelID := "peel-01"
	secrets := map[string]string{"db_password": "hunter2"}

	// First call fails at the KV put.
	if err := pub.PublishSecrets(ctx, peelID, secrets, peelEnc.PublicKey()); err == nil {
		t.Fatal("expected injected put failure, got nil")
	}
	if _, err := secretsKV.Get(ctx, peelID); !errors.Is(err, bus.ErrKeyNotFound) {
		t.Fatalf("secrets entry should not exist after failed put, err=%v", err)
	}

	// Identical retry must NOT be skipped — the failure was not cached.
	if err := pub.PublishSecrets(ctx, peelID, secrets, peelEnc.PublicKey()); err != nil {
		t.Fatalf("retry publish: %v", err)
	}
	if _, err := secretsKV.Get(ctx, peelID); err != nil {
		t.Fatalf("secrets entry missing after retry: %v", err)
	}
}

// TestPublishSecrets_EncryptFailureNotCached verifies that an encryption
// error (bad recipient key) does not poison the skip-cache.
func TestPublishSecrets_EncryptFailureNotCached(t *testing.T) {
	env := newTestPublisher(t)
	pub, secretsKV := env.pub, env.secretsKV
	ctx := context.Background()

	peelID := "peel-01"
	secrets := map[string]string{"db_password": "hunter2"}

	if err := pub.PublishSecrets(ctx, peelID, secrets, "not-a-curve-key"); err == nil {
		t.Fatal("expected encrypt failure with invalid recipient key")
	}

	// A valid key afterwards must publish.
	peelEnc := newPeelEncryptor(t)
	if err := pub.PublishSecrets(ctx, peelID, secrets, peelEnc.PublicKey()); err != nil {
		t.Fatalf("publish with valid key: %v", err)
	}
	if _, err := secretsKV.Get(ctx, peelID); err != nil {
		t.Fatalf("secrets entry missing: %v", err)
	}
}
