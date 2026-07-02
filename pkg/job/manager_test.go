package job

import (
	"context"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/bus/bustest"
)

// testSetup creates in-memory fakes for pub/sub and JetStream, and
// initializes the standard KV buckets. Shared by all test files in this package.
func testSetup(t *testing.T) (bus.PubSub, bus.JetStreamAPI) {
	t.Helper()
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}
	return bustest.NewFakePubSub(), js
}

func TestManagerDispatchAndGetJob(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgr := NewManager(ps, js, "test-master", nil)

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"web-01"}, 5*time.Second)

	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// Retrieve from KV.
	got, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}

	if got.JID != j.JID {
		t.Errorf("JID: got %q, want %q", got.JID, j.JID)
	}
	if got.Function != "cmd.run" {
		t.Errorf("Function: got %q, want %q", got.Function, "cmd.run")
	}
	if got.Status != StatusRunning {
		t.Errorf("Status: got %q, want %q", got.Status, StatusRunning)
	}
}

func TestManagerActiveJobs(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgr := NewManager(ps, js, "test-master", nil)

	j := NewJob("test.fn", nil, []string{"peel-01"}, 5*time.Second)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	active := mgr.ActiveJobs()
	if len(active) != 1 {
		t.Errorf("ActiveJobs: got %d, want 1", len(active))
	}
	if len(active) > 0 && active[0] != j.JID {
		t.Errorf("ActiveJobs[0]: got %q, want %q", active[0], j.JID)
	}
}

func TestManagerCancel(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgr := NewManager(ps, js, "test-master", nil)

	j := NewJob("test.fn", nil, []string{"peel-01"}, 10*time.Second)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// Cancel should not error.
	if err := mgr.Cancel(ctx, j.JID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}

func TestManagerShutdown(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgr := NewManager(ps, js, "test-master", nil)

	j := NewJob("test.fn", nil, []string{"peel-01"}, 10*time.Second)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// Shutdown should cancel all watchers and wait for them to finish.
	mgr.Shutdown()

	active := mgr.ActiveJobs()
	if len(active) != 0 {
		t.Errorf("ActiveJobs after shutdown: got %d, want 0", len(active))
	}
}

// TestManagerShutdownDetachesInFlightJobs verifies that Shutdown does NOT
// finalize in-flight jobs as canceled: the job stays "running" with its
// collected returns persisted per-peel, so another master's orphan scanner
// can reclaim it and collect the outstanding returns.
func TestManagerShutdownDetachesInFlightJobs(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgrA := NewManager(ps, js, "master-a", nil)

	j := NewJob("cmd.run", nil, []string{"peel-01", "peel-02"}, 30*time.Second)
	if err := mgrA.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// peel-01 returns before the master shuts down; peel-02 is still busy.
	ret1 := Return{JID: j.JID, PeelID: "peel-01", Success: true, Timestamp: time.Now().UTC()}
	data1, _ := bus.Encode(ret1)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-01"), data1)

	mgrA.Shutdown()

	// The job must remain running (NOT canceled) so it can be reclaimed.
	stored, err := mgrA.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob after shutdown: %v", err)
	}
	if stored.Status != StatusRunning {
		t.Fatalf("Status after shutdown = %q, want %q (detach, not cancel)", stored.Status, StatusRunning)
	}

	// The collected return was persisted per-peel.
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Return
	if err := bus.KVGet(ctx, returnsBucket, j.JID+".peel-01", &persisted); err != nil {
		t.Fatalf("per-peel return not persisted on shutdown: %v", err)
	}

	// A surviving master's orphan scanner reclaims the job (master-a has
	// no heartbeat, so it is dead) and hands it to its manager. This test
	// exercises detach/reclaim semantics, not the consecutive-miss grace
	// (covered by TestOrphanScannerRequiresConsecutiveMisses).
	mgrB := NewManager(ps, js, "master-b", nil)
	scanner := NewOrphanScanner("master-b", js, nil, mgrB.ReclaimJob)
	scanner.MissThreshold = 1
	scanner.scan(ctx)

	time.Sleep(50 * time.Millisecond)

	// peel-02's late return reaches the new master's watcher.
	ret2 := Return{JID: j.JID, PeelID: "peel-02", Success: true, Timestamp: time.Now().UTC()}
	data2, _ := bus.Encode(ret2)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-02"), data2)

	waitForNoActiveJobs(t, mgrB)

	final, err := mgrB.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob after reclaim: %v", err)
	}
	if final.Status != StatusComplete {
		t.Errorf("final Status = %q, want %q", final.Status, StatusComplete)
	}
	if final.Owner != "master-b" {
		t.Errorf("final Owner = %q, want master-b", final.Owner)
	}

	returns, err := mgrB.GetReturns(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetReturns: %v", err)
	}
	if len(returns) != 2 {
		t.Errorf("returns: got %d, want 2 (persisted + late)", len(returns))
	}
}

// TestManagerCancelStillFinalizesCanceled verifies operator cancellation
// keeps its terminal semantics after the shutdown-detach change.
func TestManagerCancelStillFinalizesCanceled(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgr := NewManager(ps, js, "test-master", nil)

	j := NewJob("test.fn", nil, []string{"peel-01"}, 30*time.Second)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if err := mgr.Cancel(ctx, j.JID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	waitForNoActiveJobs(t, mgr)

	stored, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if stored.Status != StatusCanceled {
		t.Errorf("Status = %q, want %q (operator cancel must finalize)", stored.Status, StatusCanceled)
	}
}

func TestManagerGetReturnsEmpty(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgr := NewManager(ps, js, "test-master", nil)

	returns, err := mgr.GetReturns(ctx, "nonexistent-jid")
	if err != nil {
		t.Fatalf("GetReturns: %v", err)
	}
	if len(returns) != 0 {
		t.Errorf("expected empty returns, got %d", len(returns))
	}
}

// TestGetReturnsReadsPerPeelKeys verifies that GetReturns reads the
// per-peel keys, including for a job whose master crashed before
// finalizeJob (in-flight returns are persisted per-peel immediately).
func TestGetReturnsReadsPerPeelKeys(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgr := NewManager(ps, js, "test-master", nil)

	jid := "crash-recovery-job"

	// Simulate per-peel keys written by persistReturns before crash.
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	for _, peelID := range []string{"peel-01", "peel-02", "peel-03"} {
		ret := Return{
			JID:       jid,
			PeelID:    peelID,
			Success:   true,
			Duration:  100 * time.Millisecond,
			Timestamp: time.Now().UTC(),
		}
		if _, err := bus.KVPut(ctx, returnsBucket, jid+"."+peelID, ret); err != nil {
			t.Fatalf("write per-peel key %s: %v", peelID, err)
		}
	}

	returns, err := mgr.GetReturns(ctx, jid)
	if err != nil {
		t.Fatalf("GetReturns: %v", err)
	}
	if len(returns) != 3 {
		t.Fatalf("returns: got %d, want 3", len(returns))
	}

	peelIDs := make(map[string]bool)
	for _, r := range returns {
		peelIDs[r.PeelID] = true
	}
	for _, want := range []string{"peel-01", "peel-02", "peel-03"} {
		if !peelIDs[want] {
			t.Errorf("missing peel %s in returns", want)
		}
	}
}

func TestManagerDispatchIdempotentSamePayload(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgrA := NewManager(ps, js, "master-a", nil)
	mgrB := NewManager(ps, js, "master-b", nil)

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"web-01"}, 5*time.Second)
	jid := j.JID

	if err := mgrA.Dispatch(ctx, j); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}

	// Retry with same JID and same immutable payload should be accepted.
	retry := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"web-01"}, 5*time.Second)
	retry.JID = jid
	if err := mgrB.Dispatch(ctx, retry); err != nil {
		t.Fatalf("retry Dispatch should be idempotent, got error: %v", err)
	}
}

func TestManagerDispatchRejectsConflictingPayloadOnSameJID(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgrA := NewManager(ps, js, "master-a", nil)
	mgrB := NewManager(ps, js, "master-b", nil)

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"web-01"}, 5*time.Second)
	jid := j.JID
	if err := mgrA.Dispatch(ctx, j); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}

	// Same JID, different args should be rejected as conflict.
	conflict := NewJob("cmd.run", map[string]any{"cmd": "hostname"}, []string{"web-01"}, 5*time.Second)
	conflict.JID = jid
	if err := mgrB.Dispatch(ctx, conflict); err == nil {
		t.Fatal("expected conflict error for same JID with different payload")
	}
}
