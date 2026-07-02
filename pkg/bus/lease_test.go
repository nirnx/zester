package bus_test

import (
	"context"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
)

// leaseTestSetup creates a lease bucket with a short TTL. FakeKV checks TTL
// on access (no background purge), so expiry tests just sleep past the TTL.
func leaseTestSetup(t *testing.T, ttl time.Duration) *testJS {
	t.Helper()
	js := newTestJS()
	_, err := bus.CreateBucket(context.Background(), js, bus.BucketConfig{
		Bucket: "test-leases",
		TTL:    ttl,
	})
	if err != nil {
		t.Fatalf("create lease bucket: %v", err)
	}
	return js
}

func newTestLease(t *testing.T, js *testJS, holder string, ttl time.Duration, acquired, lost chan struct{}) *bus.LeaderLease {
	t.Helper()
	lease, err := bus.NewLeaderLease(bus.LeaderLeaseConfig{
		JS:            js,
		Bucket:        "test-leases",
		Key:           "settings-publisher",
		HolderID:      holder,
		TTL:           ttl,
		RenewInterval: ttl / 6,
		OnAcquired:    func() { acquired <- struct{}{} },
		OnLost:        func() { lost <- struct{}{} },
	})
	if err != nil {
		t.Fatalf("new lease for %s: %v", holder, err)
	}
	return lease
}

func waitSignal(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestNewLeaderLease_Validation(t *testing.T) {
	js := newTestJS()

	if _, err := bus.NewLeaderLease(bus.LeaderLeaseConfig{Key: "k", HolderID: "h"}); err == nil {
		t.Error("expected error for missing JS")
	}
	if _, err := bus.NewLeaderLease(bus.LeaderLeaseConfig{JS: js, HolderID: "h"}); err == nil {
		t.Error("expected error for missing Key")
	}
	if _, err := bus.NewLeaderLease(bus.LeaderLeaseConfig{JS: js, Key: "k"}); err == nil {
		t.Error("expected error for missing HolderID")
	}
	if _, err := bus.NewLeaderLease(bus.LeaderLeaseConfig{JS: js, Key: "k", HolderID: "h"}); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestLeaderLease_RunErrorsWhenBucketMissing(t *testing.T) {
	js := newTestJS() // no bucket created
	lease, err := bus.NewLeaderLease(bus.LeaderLeaseConfig{JS: js, Key: "k", HolderID: "h"})
	if err != nil {
		t.Fatalf("new lease: %v", err)
	}
	if err := lease.Run(context.Background()); err == nil {
		t.Fatal("expected error when lease bucket does not exist")
	}
}

func TestLeaderLease_AcquireRenewFailover(t *testing.T) {
	const ttl = 300 * time.Millisecond
	js := leaseTestSetup(t, ttl)
	ctx := context.Background()

	acquiredA, lostA := make(chan struct{}, 8), make(chan struct{}, 8)
	acquiredB, lostB := make(chan struct{}, 8), make(chan struct{}, 8)
	leaseA := newTestLease(t, js, "master-a", ttl, acquiredA, lostA)
	leaseB := newTestLease(t, js, "master-b", ttl, acquiredB, lostB)

	ctxA, cancelA := context.WithCancel(ctx)
	go func() { _ = leaseA.Run(ctxA) }()
	waitSignal(t, acquiredA, "A to acquire")
	if !leaseA.IsLeader() {
		t.Fatal("A should report leadership after OnAcquired")
	}

	ctxB, cancelB := context.WithCancel(ctx)
	defer cancelB()
	go func() { _ = leaseB.Run(ctxB) }()

	// A renews every ttl/6, so B must not acquire even after the TTL passes.
	select {
	case <-acquiredB:
		t.Fatal("B acquired the lease while A was renewing it")
	case <-time.After(2 * ttl):
	}
	if leaseB.IsLeader() {
		t.Fatal("B should not report leadership")
	}

	// A shuts down: it releases the lease, B takes over without waiting a
	// full TTL, and A reports the loss.
	cancelA()
	waitSignal(t, lostA, "A to report loss on shutdown")
	waitSignal(t, acquiredB, "B to take over after release")
	if leaseA.IsLeader() {
		t.Error("A should not report leadership after shutdown")
	}
	if !leaseB.IsLeader() {
		t.Error("B should report leadership after takeover")
	}
}

func TestLeaderLease_LostOnCASConflictThenReacquires(t *testing.T) {
	const ttl = 300 * time.Millisecond
	js := leaseTestSetup(t, ttl)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	acquired, lost := make(chan struct{}, 8), make(chan struct{}, 8)
	lease := newTestLease(t, js, "master-a", ttl, acquired, lost)
	go func() { _ = lease.Run(ctx) }()
	waitSignal(t, acquired, "initial acquisition")

	// An intruder overwrites the key: the next renewal's CAS must fail and
	// leadership is lost.
	kv, err := bus.GetBucket(ctx, js, "test-leases")
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if _, err := kv.Put(ctx, "settings-publisher", []byte("intruder")); err != nil {
		t.Fatalf("usurp lease: %v", err)
	}
	waitSignal(t, lost, "loss after CAS conflict")

	// The intruder never renews, so its entry expires after the bucket TTL
	// and the lease is re-acquired.
	waitSignal(t, acquired, "re-acquisition after intruder expiry")
	if !lease.IsLeader() {
		t.Error("lease should report leadership after re-acquisition")
	}
}
