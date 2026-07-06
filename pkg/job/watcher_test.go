package job

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

func TestWatcherCompletesOnAllReturns(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-01", "peel-02"}, 10*time.Second)
	j.Status = StatusRunning

	// Store job first.
	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	bus.KVPut(ctx, jobsBucket, j.JID, j)

	w := NewWatcher(j, ps, js, nil)

	go w.Watch(ctx)

	// Send returns for both peels.
	time.Sleep(50 * time.Millisecond)

	for _, peelID := range []string{"peel-01", "peel-02"} {
		ret := Return{
			JID:        j.JID,
			PeelID:     peelID,
			Success:    true,
			ReturnData: "ok",
			Duration:   100 * time.Millisecond,
			Timestamp:  time.Now().UTC(),
		}
		data, _ := bus.Encode(ret)
		subject := bus.JobReturnSubject(j.JID, peelID)
		ps.Publish(subject, data)
	}

	// Wait for watcher to finish.
	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish in time")
	}

	if j.Status != StatusComplete {
		t.Errorf("Status: got %q, want %q", j.Status, StatusComplete)
	}

	returns := w.Returns()
	if len(returns) != 2 {
		t.Errorf("returns: got %d, want 2", len(returns))
	}
}

func TestWatcherTimeout(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-01", "peel-02"}, 200*time.Millisecond)
	j.Status = StatusRunning

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	bus.KVPut(ctx, jobsBucket, j.JID, j)

	w := NewWatcher(j, ps, js, nil)
	go w.Watch(ctx)

	// Only send one return (of two expected).
	time.Sleep(50 * time.Millisecond)
	ret := Return{
		JID:       j.JID,
		PeelID:    "peel-01",
		Success:   true,
		Duration:  50 * time.Millisecond,
		Timestamp: time.Now().UTC(),
	}
	data, _ := bus.Encode(ret)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-01"), data)

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish in time")
	}

	// Should be partial (1 of 2 returned).
	if j.Status != StatusPartial {
		t.Errorf("Status: got %q, want %q", j.Status, StatusPartial)
	}

	returns := w.Returns()
	if len(returns) != 1 {
		t.Errorf("returns: got %d, want 1", len(returns))
	}
}

func TestWatcherTimeoutNoReturns(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-01"}, 200*time.Millisecond)
	j.Status = StatusRunning

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	bus.KVPut(ctx, jobsBucket, j.JID, j)

	w := NewWatcher(j, ps, js, nil)
	go w.Watch(ctx)

	// Don't send any returns.
	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish in time")
	}

	if j.Status != StatusTimeout {
		t.Errorf("Status: got %q, want %q", j.Status, StatusTimeout)
	}
}

func TestWatcherCancel(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-01"}, 30*time.Second)
	j.Status = StatusRunning

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	bus.KVPut(ctx, jobsBucket, j.JID, j)

	w := NewWatcher(j, ps, js, nil)
	go w.Watch(ctx)

	time.Sleep(50 * time.Millisecond)
	w.Cancel()

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish after cancel")
	}

	// Cancelled with incomplete returns -> canceled status.
	if j.Status != StatusCanceled {
		t.Errorf("Status: got %q, want %q", j.Status, StatusCanceled)
	}
}

func TestWatcherAcks(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-01"}, 500*time.Millisecond)
	j.Status = StatusRunning

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	bus.KVPut(ctx, jobsBucket, j.JID, j)

	w := NewWatcher(j, ps, js, nil)
	go w.Watch(ctx)

	time.Sleep(50 * time.Millisecond)

	// Send ack.
	ack := Ack{
		JID:       j.JID,
		PeelID:    "peel-01",
		Timestamp: time.Now().UTC(),
	}
	data, _ := bus.Encode(ack)
	ps.Publish(bus.JobAckSubject(j.JID, "peel-01"), data)

	// Send return.
	ret := Return{
		JID:       j.JID,
		PeelID:    "peel-01",
		Success:   true,
		Duration:  10 * time.Millisecond,
		Timestamp: time.Now().UTC(),
	}
	data, _ = bus.Encode(ret)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-01"), data)

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}

	acks := w.Acks()
	if len(acks) != 1 {
		t.Errorf("acks: got %d, want 1", len(acks))
	}
}

// TestWatcherDetachLeavesJobRunning verifies the shutdown-detach path:
// collected returns are persisted per-peel, but no terminal status is
// written — the job record keeps its revision (epoch) and "running"
// status so another master's orphan scanner can reclaim it.
func TestWatcherDetachLeavesJobRunning(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-01", "peel-02"}, 30*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-a"

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	jobData, _ := bus.Encode(j)
	rev, err := jobsBucket.Create(ctx, j.JID, jobData)
	if err != nil {
		t.Fatalf("store job: %v", err)
	}
	j.Epoch = rev

	w := NewWatcher(j, ps, js, nil)
	go w.Watch(ctx)

	time.Sleep(50 * time.Millisecond)

	// One of two peels returns, then the master shuts down.
	ret := Return{JID: j.JID, PeelID: "peel-01", Success: true, Timestamp: time.Now().UTC()}
	data, _ := bus.Encode(ret)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-01"), data)

	w.Detach()

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not exit after detach")
	}

	// Job record untouched: same revision, still running, not canceled.
	entry, err := jobsBucket.Get(ctx, j.JID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if entry.Revision() != rev {
		t.Errorf("job revision = %d, want %d (unchanged by detach)", entry.Revision(), rev)
	}
	var stored Job
	if err := bus.Decode(entry.Value(), &stored); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if stored.Status != StatusRunning {
		t.Errorf("Status = %q, want %q (detach must not finalize)", stored.Status, StatusRunning)
	}

	// The collected return was persisted per-peel for the reclaiming master.
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Return
	if err := bus.KVGet(ctx, returnsBucket, j.JID+".peel-01", &persisted); err != nil {
		t.Fatalf("per-peel return not persisted on detach: %v", err)
	}
	if persisted.PeelID != "peel-01" {
		t.Errorf("persisted PeelID = %q, want peel-01", persisted.PeelID)
	}

	// No terminal aggregated key may be written on detach.
	var agg []Return
	if err := bus.KVGet(ctx, returnsBucket, j.JID, &agg); err == nil {
		t.Error("detach must not write the aggregated returns key")
	}
}

// TestWatcherFinalizeCASFencedWhenSuperseded exercises the finalize
// fencing branch: when the KV revision moved past the watcher's epoch
// (the job was reclaimed by a new owner), finalize must not overwrite
// the newer owner's record or write aggregated returns.
func TestWatcherFinalizeCASFencedWhenSuperseded(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-01"}, 5*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-old"

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	jobData, _ := bus.Encode(j)
	rev1, err := jobsBucket.Create(ctx, j.JID, jobData)
	if err != nil {
		t.Fatalf("store job: %v", err)
	}
	j.Epoch = rev1

	// A new master reclaims the job, moving the revision past rev1.
	newOwner := *j
	newOwner.Owner = "master-new"
	newData, _ := bus.Encode(&newOwner)
	rev2, err := jobsBucket.Update(ctx, j.JID, newData, rev1)
	if err != nil {
		t.Fatalf("reclaim update: %v", err)
	}

	// The superseded watcher (stale epoch rev1) tries to finalize.
	w := NewWatcher(j, ps, js, nil)
	w.SeedReturns([]Return{{JID: j.JID, PeelID: "peel-01", Success: true}})
	w.finalizeJob(ctx, StatusComplete)

	// The record must keep the new owner's data at the new revision.
	entry, err := jobsBucket.Get(ctx, j.JID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if entry.Revision() != rev2 {
		t.Errorf("job revision = %d, want %d (fenced finalize must not write)", entry.Revision(), rev2)
	}
	var stored Job
	if err := bus.Decode(entry.Value(), &stored); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if stored.Owner != "master-new" {
		t.Errorf("Owner = %q, want master-new (stale owner must not overwrite)", stored.Owner)
	}
	if stored.Status != StatusRunning {
		t.Errorf("Status = %q, want %q", stored.Status, StatusRunning)
	}

	// The fenced finalize must bail before writing aggregated returns.
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	var agg []Return
	if err := bus.KVGet(ctx, returnsBucket, j.JID, &agg); err == nil {
		t.Error("fenced finalize must not write aggregated returns")
	}
}

// TestWatcherPerPeelPersistence verifies that incremental persist writes
// per-peel keys ("{jid}.{peelID}") and that finalize never writes an
// aggregated bare-JID key (per-peel keys are the sole store; the job
// record carries only summary counts). With returnPersistThreshold=1 every
// return must land as a per-peel key.
func TestWatcherPerPeelPersistence(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	// Create enough targets to trigger persistence (> returnPersistThreshold).
	targets := make([]string, returnPersistThreshold+2)
	for i := range targets {
		targets[i] = fmt.Sprintf("peel-%03d", i)
	}

	j := NewJob("test.fn", nil, targets, 10*time.Second)
	j.Status = StatusRunning

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	bus.KVPut(ctx, jobsBucket, j.JID, j)

	w := NewWatcher(j, ps, js, nil)
	go w.Watch(ctx)

	time.Sleep(50 * time.Millisecond)

	// Send all returns.
	for _, peelID := range targets {
		ret := Return{
			JID:       j.JID,
			PeelID:    peelID,
			Success:   true,
			Duration:  10 * time.Millisecond,
			Timestamp: time.Now().UTC(),
		}
		data, _ := bus.Encode(ret)
		ps.Publish(bus.JobReturnSubject(j.JID, peelID), data)
	}

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish in time")
	}

	// Verify per-peel keys exist in KV.
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}

	keys, err := returnsBucket.Keys(ctx)
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}

	// Should have per-peel keys and NO aggregated final key.
	peelKeyCount := 0
	hasAggregated := false
	for _, k := range keys {
		if k == j.JID {
			hasAggregated = true
		} else if len(k) > len(j.JID)+1 && k[:len(j.JID)+1] == j.JID+"." {
			peelKeyCount++
		}
	}

	if peelKeyCount != len(targets) {
		t.Errorf("per-peel keys: got %d, want %d (threshold=1 persists every return)", peelKeyCount, len(targets))
	}
	if hasAggregated {
		t.Error("finalize must not write the aggregated returns key (finding 9: ~1MB job-width ceiling)")
	}

	// The job record carries the summary counts instead.
	var stored Job
	if err := bus.KVGet(ctx, jobsBucket, j.JID, &stored); err != nil {
		t.Fatalf("get finalized job: %v", err)
	}
	if stored.ReturnCount != len(targets) {
		t.Errorf("ReturnCount = %d, want %d", stored.ReturnCount, len(targets))
	}
	if stored.SuccessCount != len(targets) {
		t.Errorf("SuccessCount = %d, want %d", stored.SuccessCount, len(targets))
	}

	// Verify a per-peel key contains a single Return.
	var single Return
	if err := bus.KVGet(ctx, returnsBucket, j.JID+".peel-000", &single); err != nil {
		t.Fatalf("get per-peel key: %v", err)
	}
	if single.PeelID != "peel-000" {
		t.Errorf("per-peel return peelID: got %q, want peel-000", single.PeelID)
	}
}

// TestWatcherPersistsEachReturnImmediately verifies threshold=1 behavior:
// a single return is written to its per-peel KV key as soon as it arrives,
// while the job is still in flight (crash recovery loses at most the
// in-flight return).
func TestWatcherPersistsEachReturnImmediately(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-01", "peel-02"}, 10*time.Second)
	j.Status = StatusRunning

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	bus.KVPut(ctx, jobsBucket, j.JID, j)

	w := NewWatcher(j, ps, js, nil)
	go w.Watch(ctx)

	time.Sleep(50 * time.Millisecond)

	// One return of two: below any batch size > 1.
	ret := Return{JID: j.JID, PeelID: "peel-01", Success: true, Timestamp: time.Now().UTC()}
	data, _ := bus.Encode(ret)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-01"), data)

	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var persisted Return
		if err := bus.KVGet(ctx, returnsBucket, j.JID+".peel-01", &persisted); err == nil {
			if persisted.PeelID != "peel-01" {
				t.Errorf("persisted PeelID = %q, want peel-01", persisted.PeelID)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("single return was not persisted immediately (threshold != 1?)")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The watcher must still be running: one of two returns is not terminal.
	select {
	case <-w.Done():
		t.Fatal("watcher finished before all targets returned")
	default:
	}

	w.Cancel()
	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish after cancel")
	}
}

// TestWatcherRecoveryFromPerPeelKeys verifies that the Manager can recover
// watcher state from per-peel KV keys after a crash (orphan recovery).
func TestWatcherRecoveryFromPerPeelKeys(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-01", "peel-02", "peel-03"}, 5*time.Second)
	j.Status = StatusRunning

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	bus.KVPut(ctx, jobsBucket, j.JID, j)

	// Simulate per-peel keys written before crash (2 of 3 peels returned).
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	for _, peelID := range []string{"peel-01", "peel-02"} {
		ret := Return{
			JID:       j.JID,
			PeelID:    peelID,
			Success:   true,
			Duration:  50 * time.Millisecond,
			Timestamp: time.Now().UTC(),
		}
		bus.KVPut(ctx, returnsBucket, j.JID+"."+peelID, ret)
	}

	// Create a manager and recover.
	mgr := NewManager(ps, js, "master-recovery", nil)
	recovered := mgr.recoverPerPeelReturns(ctx, j.JID)

	if len(recovered) != 2 {
		t.Fatalf("recovered returns: got %d, want 2", len(recovered))
	}

	peelIDs := make(map[string]bool)
	for _, r := range recovered {
		peelIDs[r.PeelID] = true
	}
	if !peelIDs["peel-01"] || !peelIDs["peel-02"] {
		t.Errorf("recovered peels: got %v, want peel-01 and peel-02", peelIDs)
	}
}
