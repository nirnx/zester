package statefiles_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/statefiles"
)

func TestSync(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	// Pre-populate KV with state files.
	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	if _, err := pub.PublishFiles(ctx, map[string][]byte{
		"init.zy":           []byte("top-level state"),
		"webserver/init.zy": []byte("nginx config"),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	cacheDir := t.TempDir()
	cache := statefiles.NewCache(statefiles.CacheConfig{
		CacheDir: cacheDir,
		JS:       js,
	})

	count, err := cache.Sync(ctx)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if count != 2 {
		t.Errorf("count: got %d, want 2", count)
	}

	// Verify files on disk.
	tests := []struct {
		path string
		want string
	}{
		{"init.zy", "top-level state"},
		{filepath.Join("webserver", "init.zy"), "nginx config"},
	}
	for _, tt := range tests {
		data, err := os.ReadFile(filepath.Join(cacheDir, tt.path))
		if err != nil {
			t.Errorf("read %s: %v", tt.path, err)
			continue
		}
		if got := string(data); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestSyncCreatesSubdirs(t *testing.T) {
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
		"a/b/c/deep.zy": []byte("deeply nested"),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	cacheDir := t.TempDir()
	cache := statefiles.NewCache(statefiles.CacheConfig{
		CacheDir: cacheDir,
		JS:       js,
	})

	count, err := cache.Sync(ctx)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}

	// Verify subdirectory was created.
	deepPath := filepath.Join(cacheDir, "a", "b", "c", "deep.zy")
	data, err := os.ReadFile(deepPath)
	if err != nil {
		t.Fatalf("read %s: %v", deepPath, err)
	}
	if got := string(data); got != "deeply nested" {
		t.Errorf("value: got %q, want %q", got, "deeply nested")
	}
}

func TestSyncSkipsRevisionKey(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	// A publish leaves _revision and _manifest meta keys in the bucket
	// alongside the state files.
	pub := statefiles.NewPublisher(statefiles.PublisherConfig{KV: kv})
	if _, err := pub.PublishFiles(ctx, map[string][]byte{
		"init.zy": []byte("top-level state"),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	cacheDir := t.TempDir()
	cache := statefiles.NewCache(statefiles.CacheConfig{
		CacheDir: cacheDir,
		JS:       js,
	})

	count, err := cache.Sync(ctx)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1 (should skip meta keys)", count)
	}

	// Verify the meta keys were NOT written to disk.
	if _, err := os.Stat(filepath.Join(cacheDir, bus.KeyRevision)); err == nil {
		t.Error("_revision file should not exist on disk")
	}
	if _, err := os.Stat(filepath.Join(cacheDir, statefiles.KeyManifest)); err == nil {
		t.Error("_manifest file should not exist on disk")
	}

	// Verify the real state file was written.
	data, err := os.ReadFile(filepath.Join(cacheDir, "init.zy"))
	if err != nil {
		t.Fatalf("read init.zy: %v", err)
	}
	if got := string(data); got != "top-level state" {
		t.Errorf("init.zy: got %q, want %q", got, "top-level state")
	}
}

func TestSyncAtomicWriteLeavesNoTempFiles(t *testing.T) {
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
		"init.zy":           []byte("v1"),
		"webserver/init.zy": []byte("nginx v1"),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	cacheDir := t.TempDir()
	cache := statefiles.NewCache(statefiles.CacheConfig{
		CacheDir: cacheDir,
		JS:       js,
	})

	if _, err := cache.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Re-sync with updated content to exercise the overwrite path.
	if _, err := pub.PublishFiles(ctx, map[string][]byte{
		"init.zy":           []byte("v2"),
		"webserver/init.zy": []byte("nginx v1"),
	}); err != nil {
		t.Fatalf("re-publish: %v", err)
	}
	if _, err := cache.Sync(ctx); err != nil {
		t.Fatalf("re-sync: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(cacheDir, "init.zy"))
	if err != nil {
		t.Fatalf("read init.zy: %v", err)
	}
	if got := string(data); got != "v2" {
		t.Errorf("init.zy: got %q, want %q", got, "v2")
	}

	// The cache dir must contain only the final files — no temp litter.
	var files []string
	err = filepath.WalkDir(cacheDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(cacheDir, path)
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk cache dir: %v", err)
	}
	sort.Strings(files)
	want := []string{"init.zy", filepath.Join("webserver", "init.zy")}
	if len(files) != len(want) {
		t.Fatalf("cache dir files: got %v, want %v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Errorf("cache dir files: got %v, want %v", files, want)
			break
		}
	}

	// Mode of the previous os.WriteFile path must be preserved.
	info, err := os.Stat(filepath.Join(cacheDir, "init.zy"))
	if err != nil {
		t.Fatalf("stat init.zy: %v", err)
	}
	if got := info.Mode().Perm(); got != 0644 {
		t.Errorf("mode: got %o, want 0644", got)
	}
}

func TestSyncEmptyBucket(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	cacheDir := t.TempDir()
	cache := statefiles.NewCache(statefiles.CacheConfig{
		CacheDir: cacheDir,
		JS:       js,
	})

	count, err := cache.Sync(ctx)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if count != 0 {
		t.Errorf("count: got %d, want 0", count)
	}
}
