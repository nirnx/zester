package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

// failingListKV wraps a bus.KV so ListKeys fails with a fixed
// error, simulating a KV/infrastructure failure (as opposed to an empty
// bucket, which the fake reports as bus.ErrNoKeysFound).
type failingListKV struct {
	bus.KV
	err error
}

func (f *failingListKV) ListKeys(context.Context) (bus.KeyLister, error) {
	return nil, f.err
}

// failingListJS wraps a JetStreamAPI so that KeyValue lookups for one
// bucket return a KV whose ListKeys fails.
type failingListJS struct {
	bus.JetStreamAPI
	bucket string
	err    error
}

func (f *failingListJS) KeyValue(ctx context.Context, bucket string) (bus.KV, error) {
	kv, err := f.JetStreamAPI.KeyValue(ctx, bucket)
	if err != nil {
		return nil, err
	}
	if bucket == f.bucket {
		return &failingListKV{KV: kv, err: f.err}, nil
	}
	return kv, nil
}

// shortHeartbeatTTL overrides the master-heartbeat bucket TTL so expiry
// tests run fast. FakeKV checks TTL on access (no background purge), so
// sleeping ttl+100ms is enough to observe expiry.
func shortHeartbeatTTL(t *testing.T, js bus.JetStreamAPI, ttl time.Duration) {
	t.Helper()
	if _, err := bus.CreateBucket(context.Background(), js, bus.BucketConfig{
		Bucket: bus.BucketMasterHeartbeat,
		TTL:    ttl,
	}); err != nil {
		t.Fatalf("override heartbeat TTL: %v", err)
	}
}

// TestHeartbeaterWritesAndExpires runs the production Heartbeater loop and
// verifies its heartbeat shows up in ListLiveMasters with the active job
// list, then disappears after the bucket TTL once the loop stops (master
// death).
func TestHeartbeaterWritesAndExpires(t *testing.T) {
	_, js := testSetup(t)
	ttl := 300 * time.Millisecond
	shortHeartbeatTTL(t, js, ttl)

	masterID := "master-hb-real"
	h := NewHeartbeater(masterID, js, nil, func() []string {
		return []string{"job-1", "job-2"}
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.Run(ctx)
		close(done)
	}()

	// Run publishes an initial beat immediately; poll until visible.
	deadline := time.Now().Add(2 * time.Second)
	var hb MasterHeartbeat
	for {
		live, err := ListLiveMasters(context.Background(), js)
		if err != nil {
			t.Fatalf("ListLiveMasters: %v", err)
		}
		if got, ok := live[masterID]; ok {
			hb = got
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("heartbeat never appeared in live masters")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if hb.MasterID != masterID {
		t.Errorf("MasterID = %q, want %q", hb.MasterID, masterID)
	}
	if len(hb.ActiveJobs) != 2 || hb.ActiveJobs[0] != "job-1" || hb.ActiveJobs[1] != "job-2" {
		t.Errorf("ActiveJobs = %v, want [job-1 job-2]", hb.ActiveJobs)
	}
	if hb.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}

	// Stop the heartbeat loop (master "dies").
	cancel()
	<-done

	// Without renewal the entry expires and the master drops out.
	time.Sleep(ttl + 100*time.Millisecond)
	live, err := ListLiveMasters(context.Background(), js)
	if err != nil {
		t.Fatalf("ListLiveMasters after expiry: %v", err)
	}
	if _, ok := live[masterID]; ok {
		t.Error("master should be considered dead after heartbeat TTL expiry")
	}
}

// TestListLiveMastersEmpty verifies an empty heartbeat bucket yields an
// empty (non-nil, non-error) live-master set.
func TestListLiveMastersEmpty(t *testing.T) {
	_, js := testSetup(t)

	live, err := ListLiveMasters(context.Background(), js)
	if err != nil {
		t.Fatalf("ListLiveMasters: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("expected no live masters, got %d", len(live))
	}
}

// TestListLiveMastersErrorPropagation verifies that a real KV failure is
// returned as an error (nil map), NOT swallowed as "no live masters" —
// the orphan scanner relies on this to abort instead of mass-reclaiming.
func TestListLiveMastersErrorPropagation(t *testing.T) {
	_, js := testSetup(t)

	wantErr := errors.New("jetstream unavailable")
	failing := &failingListJS{JetStreamAPI: js, bucket: bus.BucketMasterHeartbeat, err: wantErr}

	live, err := ListLiveMasters(context.Background(), failing)
	if err == nil {
		t.Fatal("expected error when heartbeat ListKeys fails")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want wrapped %v", err, wantErr)
	}
	if live != nil {
		t.Errorf("live masters = %v, want nil on error", live)
	}
}
