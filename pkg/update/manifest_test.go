package update

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
)

func setupManifestTest(t *testing.T) (*ManifestStore, *BinaryStore) {
	t.Helper()
	ctx := context.Background()
	js := bustest.NewFakeJS()
	bus.InitializeStorage(ctx, js)
	kv, err := bus.GetBucket(ctx, js, bus.BucketUpdateManifests)
	if err != nil {
		t.Fatal(err)
	}
	objStore := newFakeObjectStore()
	return NewManifestStore(kv), NewBinaryStore(objStore)
}

func TestManifestStore_PublishAndGet(t *testing.T) {
	ctx := context.Background()
	mStore, _ := setupManifestTest(t)

	published := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	m := &Manifest{
		Version:   "v1.2.3",
		Component: "peel",
		GOOS:      "linux",
		GOARCH:    "amd64",
		SHA256:    "abc123def456",
		Size:      1024,
		ObjectKey: "peel/linux/amd64/v1.2.3",
		Published: published,
		Publisher: "admin",
	}

	if err := mStore.Publish(ctx, m); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got, err := mStore.Get(ctx, "peel", "linux", "amd64", "v1.2.3")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Version != m.Version {
		t.Errorf("Version: got %q, want %q", got.Version, m.Version)
	}
	if got.Component != m.Component {
		t.Errorf("Component: got %q, want %q", got.Component, m.Component)
	}
	if got.GOOS != m.GOOS {
		t.Errorf("GOOS: got %q, want %q", got.GOOS, m.GOOS)
	}
	if got.GOARCH != m.GOARCH {
		t.Errorf("GOARCH: got %q, want %q", got.GOARCH, m.GOARCH)
	}
	if got.SHA256 != m.SHA256 {
		t.Errorf("SHA256: got %q, want %q", got.SHA256, m.SHA256)
	}
	if got.Size != m.Size {
		t.Errorf("Size: got %d, want %d", got.Size, m.Size)
	}
	if got.ObjectKey != m.ObjectKey {
		t.Errorf("ObjectKey: got %q, want %q", got.ObjectKey, m.ObjectKey)
	}
	if got.Publisher != m.Publisher {
		t.Errorf("Publisher: got %q, want %q", got.Publisher, m.Publisher)
	}
}

func TestManifestStore_ListByComponent(t *testing.T) {
	ctx := context.Background()
	mStore, _ := setupManifestTest(t)

	manifests := []*Manifest{
		{Version: "v1.0.0", Component: "peel", GOOS: "linux", GOARCH: "amd64", ObjectKey: "peel/linux/amd64/v1.0.0"},
		{Version: "v1.0.0", Component: "peel", GOOS: "linux", GOARCH: "arm64", ObjectKey: "peel/linux/arm64/v1.0.0"},
		{Version: "v1.0.0", Component: "master", GOOS: "linux", GOARCH: "amd64", ObjectKey: "master/linux/amd64/v1.0.0"},
	}
	for _, m := range manifests {
		if err := mStore.Publish(ctx, m); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	peels, err := mStore.ListByComponent(ctx, "peel")
	if err != nil {
		t.Fatalf("ListByComponent(peel): %v", err)
	}
	if len(peels) != 2 {
		t.Errorf("ListByComponent(peel): got %d results, want 2", len(peels))
	}

	masters, err := mStore.ListByComponent(ctx, "master")
	if err != nil {
		t.Fatalf("ListByComponent(master): %v", err)
	}
	if len(masters) != 1 {
		t.Errorf("ListByComponent(master): got %d results, want 1", len(masters))
	}
}

func TestManifestStore_Get_NotFound(t *testing.T) {
	ctx := context.Background()
	mStore, _ := setupManifestTest(t)

	_, err := mStore.Get(ctx, "peel", "linux", "amd64", "v99.0.0")
	if err == nil {
		t.Fatal("Get: expected error for non-existent manifest, got nil")
	}
}

func TestBinaryStore_UploadAndDownload(t *testing.T) {
	ctx := context.Background()
	_, binStore := setupManifestTest(t)

	data := []byte("binary content for testing")
	key := "peel/linux/amd64/v1.0.0"

	digest, err := binStore.Upload(ctx, key, data)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if digest == "" {
		t.Fatal("Upload: returned empty digest")
	}

	got, err := binStore.Download(ctx, key, digest)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("Download: got %q, want %q", got, data)
	}
}

func TestBinaryStore_Download_HashMismatch(t *testing.T) {
	ctx := context.Background()
	_, binStore := setupManifestTest(t)

	data := []byte("binary content for testing")
	key := "peel/linux/amd64/v1.0.0"

	if _, err := binStore.Upload(ctx, key, data); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	_, err := binStore.Download(ctx, key, "wronghash")
	if err == nil {
		t.Fatal("Download: expected error for hash mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("Download: error %q does not contain 'hash mismatch'", err.Error())
	}
}

func TestBinaryStore_Download_NotFound(t *testing.T) {
	ctx := context.Background()
	_, binStore := setupManifestTest(t)

	_, err := binStore.Download(ctx, "nonexistent/key", "anyhash")
	if err == nil {
		t.Fatal("Download: expected error for non-existent key, got nil")
	}
}

func TestBinaryStore_Delete(t *testing.T) {
	ctx := context.Background()
	_, binStore := setupManifestTest(t)

	data := []byte("binary content for testing")
	key := "peel/linux/amd64/v1.0.0"

	digest, err := binStore.Upload(ctx, key, data)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if err := binStore.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = binStore.Download(ctx, key, digest)
	if err == nil {
		t.Fatal("Download after Delete: expected error, got nil")
	}
}

func TestBinaryStore_List(t *testing.T) {
	ctx := context.Background()
	_, binStore := setupManifestTest(t)

	keys := []string{"peel/linux/amd64/v1.0.0", "master/linux/amd64/v1.0.0"}
	for _, key := range keys {
		if _, err := binStore.Upload(ctx, key, []byte("data for "+key)); err != nil {
			t.Fatalf("Upload %q: %v", key, err)
		}
	}

	infos, err := binStore.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 2 {
		t.Errorf("List: got %d results, want 2", len(infos))
	}
}

func TestManifest_ManifestKey(t *testing.T) {
	m := &Manifest{
		Component: "peel",
		GOOS:      "linux",
		GOARCH:    "amd64",
		Version:   "v1.0.0",
	}
	want := "peel.linux.amd64.v1.0.0"
	if got := m.ManifestKey(); got != want {
		t.Errorf("ManifestKey: got %q, want %q", got, want)
	}
}

func TestObjectKeyFor(t *testing.T) {
	want := "peel/linux/amd64/v1.0.0"
	if got := ObjectKeyFor("peel", "linux", "amd64", "v1.0.0"); got != want {
		t.Errorf("ObjectKeyFor: got %q, want %q", got, want)
	}
}

// TestManifest_MinProtocolMsgpackAdditive verifies MinProtocol round-trips
// and that manifests published without the field decode to 0 (check disabled).
func TestManifest_MinProtocolMsgpackAdditive(t *testing.T) {
	original := Manifest{
		Version:     "v1.0.0",
		Component:   "peel",
		GOOS:        "linux",
		GOARCH:      "amd64",
		MinProtocol: 3,
	}
	data, err := bus.Encode(&original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Manifest
	if err := bus.Decode(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.MinProtocol != 3 {
		t.Errorf("MinProtocol: got %d, want 3", decoded.MinProtocol)
	}

	legacy := Manifest{Version: "v0.9.0", Component: "peel", GOOS: "linux", GOARCH: "amd64"}
	data, err = bus.Encode(&legacy)
	if err != nil {
		t.Fatal(err)
	}
	var legacyDecoded Manifest
	if err := bus.Decode(data, &legacyDecoded); err != nil {
		t.Fatal(err)
	}
	if legacyDecoded.MinProtocol != 0 {
		t.Errorf("legacy MinProtocol: got %d, want 0", legacyDecoded.MinProtocol)
	}
}
