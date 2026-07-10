package update

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
)

func gcFixture(t *testing.T) (*BinaryGC, *fakeObjectStore, *ManifestStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	objStore := newFakeObjectStore()
	manifests := NewManifestStore(bustest.NewFakeKV("update-manifests", 0))
	rollouts := NewRolloutStore(bustest.NewFakeKV("update-rollouts", 0))
	gc := &BinaryGC{
		Manifests: manifests,
		Binaries:  NewBinaryStore(objStore),
		Rollouts:  rollouts,
	}
	return gc, objStore, manifests, ctx
}

func publishFixtureVersion(t *testing.T, ctx context.Context, objStore *fakeObjectStore, manifests *ManifestStore, m *Manifest) {
	t.Helper()
	m.ObjectKey = ObjectKeyFor(m.Component, m.GOOS, m.GOARCH, m.Version)
	bs := NewBinaryStore(objStore)
	if _, err := bs.Upload(ctx, m.ObjectKey, []byte("bin-"+m.Version)); err != nil {
		t.Fatal(err)
	}
	if err := manifests.Publish(ctx, m); err != nil {
		t.Fatal(err)
	}
}

// TestBinaryGCExpiryAndPromotion pins the release-lifecycle contract: an
// expired non-promoted version is reaped (object AND manifest); a promoted
// version older than any TTL survives every pass.
func TestBinaryGCExpiryAndPromotion(t *testing.T) {
	gc, objStore, manifests, ctx := gcFixture(t)
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)

	publishFixtureVersion(t, ctx, objStore, manifests, &Manifest{
		Component: "peel", GOOS: "linux", GOARCH: "amd64",
		Version: "0.1.0", Published: old, // legacy: Published+30d => expired
	})
	publishFixtureVersion(t, ctx, objStore, manifests, &Manifest{
		Component: "peel", GOOS: "linux", GOARCH: "amd64",
		Version: "0.2.0", Published: old, Promoted: true, // promoted: immortal
	})
	publishFixtureVersion(t, ctx, objStore, manifests, &Manifest{
		Component: "peel", GOOS: "linux", GOARCH: "amd64",
		Version: "0.3.0", Published: old, ExpiresAtUnix: TTLNever, // explicit never
	})

	res, err := gc.Run(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "peel/linux/amd64/0.1.0" {
		t.Fatalf("deleted = %v, want the expired legacy version only", res.Deleted)
	}
	if _, err := manifests.Get(ctx, "peel", "linux", "amd64", "0.1.0"); !errors.Is(err, bus.ErrKeyNotFound) {
		t.Fatalf("expired manifest must be gone, got err=%v", err)
	}
	if _, err := NewBinaryStore(objStore).Info(ctx, "peel/linux/amd64/0.2.0"); err != nil {
		t.Fatal("promoted binary must survive GC")
	}
	if _, err := manifests.Get(ctx, "peel", "linux", "amd64", "0.3.0"); err != nil {
		t.Fatal("TTLNever manifest must survive GC")
	}
}

// TestBinaryGCSkipsActiveRollout: an expired version referenced by a
// non-terminal rollout is kept (an in-flight prepare must download it).
func TestBinaryGCSkipsActiveRollout(t *testing.T) {
	gc, objStore, manifests, ctx := gcFixture(t)
	now := time.Now()

	publishFixtureVersion(t, ctx, objStore, manifests, &Manifest{
		Component: "peel", GOOS: "linux", GOARCH: "amd64",
		Version: "0.1.0", Published: now.Add(-40 * 24 * time.Hour),
	})
	if err := gc.Rollouts.Save(ctx, &RolloutState{
		ID: "rol-x", State: RolloutRolling,
		Config:      RolloutConfig{Component: "peel", Version: "0.1.0"},
		NodeResults: map[string]*NodeResult{},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := gc.Run(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 0 || len(res.Skipped) != 1 {
		t.Fatalf("deleted=%v skipped=%v; active-rollout version must be kept", res.Deleted, res.Skipped)
	}
	if _, err := manifests.Get(ctx, "peel", "linux", "amd64", "0.1.0"); err != nil {
		t.Fatal("manifest must survive while its rollout is active")
	}
}

// TestBinaryGCOrphanSweep: a manifest-less object older than the safety age
// is removed; a fresh one (publish-in-progress) is kept.
func TestBinaryGCOrphanSweep(t *testing.T) {
	gc, objStore, _, ctx := gcFixture(t)
	now := time.Now()

	// Old orphan: eligible.
	if _, err := objStore.Put(ctx, objMeta("peel/linux/amd64/9.9.9"), bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	objStore.setModTime("peel/linux/amd64/9.9.9", now.Add(-2*time.Hour))
	// Fresh orphan: a publish mid-flight (object up, manifest not yet).
	if _, err := objStore.Put(ctx, objMeta("peel/linux/amd64/8.8.8"), bytes.NewReader([]byte("y"))); err != nil {
		t.Fatal(err)
	}

	res, err := gc.Run(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Orphans) != 1 || res.Orphans[0] != "peel/linux/amd64/9.9.9" {
		t.Fatalf("orphans = %v, want the old one only", res.Orphans)
	}
	if _, err := NewBinaryStore(objStore).Info(ctx, "peel/linux/amd64/8.8.8"); err != nil {
		t.Fatal("fresh orphan (publish-in-progress) must be kept")
	}
}

func objMeta(name string) jetstream.ObjectMeta { return jetstream.ObjectMeta{Name: name} }

// TestBinaryGCRefusesPartialListing pins the HIGH review finding: a manifest
// listing that cannot be read COMPLETELY must abort the whole pass — a
// silently partial listing under-populates the orphan guard and would reap a
// still-manifested (possibly promoted) binary as an "orphan".
func TestBinaryGCRefusesPartialListing(t *testing.T) {
	gc, objStore, manifests, ctx := gcFixture(t)
	now := time.Now()

	publishFixtureVersion(t, ctx, objStore, manifests, &Manifest{
		Component: "peel", GOOS: "linux", GOARCH: "amd64",
		Version: "0.5.0", Published: now, Promoted: true,
	})
	objStore.setModTime("peel/linux/amd64/0.5.0", now.Add(-2*time.Hour))
	// Corrupt the manifest entry: Keys() lists it, KVGet fails to decode.
	if _, err := gc.Manifests.kv.Put(ctx, "peel.linux.amd64.0.5.0", []byte("not-msgpack-garbage\xff\xfe")); err != nil {
		t.Fatal(err)
	}

	if _, err := gc.Run(ctx, now); err == nil {
		t.Fatal("GC must fail on an unreadable manifest instead of sweeping with a partial guard")
	}
	if _, err := NewBinaryStore(objStore).Info(ctx, "peel/linux/amd64/0.5.0"); err != nil {
		t.Fatal("binary must survive a partial-listing GC pass")
	}
}
