package statefiles_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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

	count, err := pub.Publish(ctx)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if count != 3 {
		t.Errorf("count: got %d, want 3", count)
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

	count, err := pub.Publish(ctx)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if count != 3 {
		t.Errorf("count: got %d, want 3", count)
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

	count, err := pub.Publish(ctx)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if count != 0 {
		t.Errorf("count: got %d, want 0", count)
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

	if _, err := pub.Publish(ctx); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Verify _revision exists and equals "1".
	entry, err := kv.Get(ctx, bus.KeyRevision)
	if err != nil {
		t.Fatalf("get revision: %v", err)
	}
	if got := string(entry.Value()); got != "1" {
		t.Errorf("revision: got %q, want %q", got, "1")
	}

	// Publish again — revision should increment to "2".
	if _, err := pub.Publish(ctx); err != nil {
		t.Fatalf("publish 2: %v", err)
	}
	entry, err = kv.Get(ctx, bus.KeyRevision)
	if err != nil {
		t.Fatalf("get revision 2: %v", err)
	}
	if got := string(entry.Value()); got != "2" {
		t.Errorf("revision: got %q, want %q", got, "2")
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

	count, err := pub.Publish(ctx)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
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
