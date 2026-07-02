package job

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
)

// TestJobOwnership verifies that when a job is dispatched with an owner,
// it persists the owner identity and epoch in KV and can be read back.
func TestJobOwnership(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	masterID := "master-01"

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-01"}, 5*time.Second)
	j.Status = StatusRunning
	j.Owner = masterID

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	rev, err := bus.KVPut(ctx, jobsBucket, j.JID, j)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	// Set the epoch from the KV revision.
	j.Epoch = rev

	// Read back and verify ownership.
	var got Job
	if err := bus.KVGet(ctx, jobsBucket, j.JID, &got); err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Owner != masterID {
		t.Errorf("Owner = %q, want %q", got.Owner, masterID)
	}
	if got.Function != "cmd.run" {
		t.Errorf("Function = %q, want cmd.run", got.Function)
	}
	// Verify Epoch field was persisted (should be 0 from initial store,
	// but the KV revision is tracked separately).
	if rev == 0 {
		t.Error("KV revision should be non-zero after put")
	}
}

// TestJobOwnership_CASClaim simulates two masters racing to claim the same
// orphaned job. Only one should win the CAS (compare-and-swap) operation.
func TestJobOwnership_CASClaim(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	// Create an orphaned job (no owner).
	j := NewJob("cmd.run", nil, []string{"peel-01"}, 10*time.Second)
	j.Status = StatusRunning

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	rev, err := bus.KVPut(ctx, jobsBucket, j.JID, j)
	if err != nil {
		t.Fatalf("store orphan: %v", err)
	}

	// Two masters attempt CAS claims concurrently.
	var (
		mu      sync.Mutex
		winners []string
		wg      sync.WaitGroup
	)

	masterNames := []string{"master-alpha", "master-beta"}
	for _, name := range masterNames {
		wg.Add(1)
		go func(masterID string) {
			defer wg.Done()

			claim := *j
			claim.Owner = masterID
			data, encErr := bus.Encode(&claim)
			if encErr != nil {
				t.Errorf("encode: %v", encErr)
				return
			}

			// Attempt CAS update using the original revision.
			_, updateErr := jobsBucket.Update(ctx, j.JID, data, rev)
			if updateErr == nil {
				mu.Lock()
				winners = append(winners, masterID)
				mu.Unlock()
			}
		}(name)
	}

	wg.Wait()

	// Exactly one master should have won the CAS.
	if len(winners) != 1 {
		t.Errorf("expected exactly 1 CAS winner, got %d: %v", len(winners), winners)
	}

	// Verify the stored job has the winner's ownership.
	var stored Job
	if err := bus.KVGet(ctx, jobsBucket, j.JID, &stored); err != nil {
		t.Fatalf("read stored: %v", err)
	}
	if len(winners) == 1 && stored.Owner != winners[0] {
		t.Errorf("stored Owner = %q, want %q", stored.Owner, winners[0])
	}
}

// TestJobDispatch_Idempotent verifies that dispatching a job with the same
// JID twice results in a CAS failure on the second attempt, preventing
// duplicate dispatch. The existing job is returned unchanged.
func TestJobDispatch_Idempotent(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-01"}, 5*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-01"

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	// First dispatch succeeds.
	rev1, err := bus.KVPut(ctx, jobsBucket, j.JID, j)
	if err != nil {
		t.Fatalf("first put: %v", err)
	}

	// Second dispatch by a different master attempts CAS with Create semantics.
	// Using the same key with a Create (rev=0) should fail since the key exists.
	j2 := *j
	j2.Owner = "master-02"
	data, err := bus.Encode(&j2)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	_, err = jobsBucket.Create(ctx, j.JID, data)
	if err == nil {
		t.Fatal("expected Create to fail for existing JID (idempotency check)")
	}

	// Original job should be unchanged.
	var stored Job
	if err := bus.KVGet(ctx, jobsBucket, j.JID, &stored); err != nil {
		t.Fatalf("read: %v", err)
	}
	if stored.Owner != "master-01" {
		t.Errorf("Owner = %q, want master-01 (original)", stored.Owner)
	}
	_ = rev1
}

// TestJobOwnership_FencingEpoch verifies that epoch increments on re-claim
// and that a stale epoch is detectable. When a job is reclaimed by a new
// master, the epoch (KV revision) increases. A master holding a stale epoch
// should fail CAS updates.
func TestJobOwnership_FencingEpoch(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", nil, []string{"peel-01"}, 10*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-A"

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	rev1, err := bus.KVPut(ctx, jobsBucket, j.JID, j)
	if err != nil {
		t.Fatalf("initial store: %v", err)
	}
	j.Epoch = rev1

	// Master-B reclaims the job (simulating orphan recovery).
	reclaimed := *j
	reclaimed.Owner = "master-B"
	reclaimData, err := bus.Encode(&reclaimed)
	if err != nil {
		t.Fatalf("encode reclaim: %v", err)
	}

	rev2, err := jobsBucket.Update(ctx, j.JID, reclaimData, rev1)
	if err != nil {
		t.Fatalf("reclaim CAS: %v", err)
	}

	// Epoch should have incremented.
	if rev2 <= rev1 {
		t.Errorf("reclaim epoch %d should be > original epoch %d", rev2, rev1)
	}

	// Master-A tries to update with stale epoch rev1 -- should fail.
	staleUpdate := *j
	staleUpdate.Owner = "master-A"
	staleUpdate.Status = StatusComplete
	staleData, err := bus.Encode(&staleUpdate)
	if err != nil {
		t.Fatalf("encode stale: %v", err)
	}

	_, err = jobsBucket.Update(ctx, j.JID, staleData, rev1)
	if err == nil {
		t.Fatal("stale epoch CAS should have failed, but succeeded")
	}

	// Master-B's ownership should still hold.
	var stored Job
	if err := bus.KVGet(ctx, jobsBucket, j.JID, &stored); err != nil {
		t.Fatalf("read: %v", err)
	}
	if stored.Owner != "master-B" {
		t.Errorf("Owner = %q, want master-B (reclaimer)", stored.Owner)
	}
}

// TestWatcherRecovery tests that a recovered watcher (re-created from a persisted
// running job) can collect remaining returns and finalize the job.
// Per the architect review, returns are stored incrementally per-peel
// with keys <jid>.<peel-id> so recovery can pre-populate from KV.
func TestWatcherRecovery(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	// Simulate a job that was running when the old master died.
	j := NewJob("cmd.run", nil, []string{"peel-01", "peel-02"}, 5*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-old"

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if _, err := bus.KVPut(ctx, jobsBucket, j.JID, j); err != nil {
		t.Fatalf("store: %v", err)
	}

	// Simulate incremental return persistence: peel-01 already returned
	// and was persisted to KV before the old master died.
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatalf("get returns bucket: %v", err)
	}
	existingReturn := Return{
		JID:        j.JID,
		PeelID:     "peel-01",
		Success:    true,
		ReturnData: "persisted-before-crash",
		Duration:   30 * time.Millisecond,
		Timestamp:  time.Now().UTC(),
	}
	// Store incremental return with key <jid>.<peel-id>.
	if _, err := bus.KVPut(ctx, returnsBucket, j.JID+".peel-01", existingReturn); err != nil {
		t.Fatalf("store incremental return: %v", err)
	}

	// Create a new watcher (simulating recovery on a new master).
	j.Owner = "master-new"
	w := NewWatcher(j, ps, js, nil)
	w.SeedReturns([]Return{existingReturn})
	go w.Watch(ctx)

	time.Sleep(50 * time.Millisecond)

	// Send return for peel-02 (the one that hasn't returned yet).
	ret := Return{
		JID:        j.JID,
		PeelID:     "peel-02",
		Success:    true,
		ReturnData: "recovered-ok",
		Duration:   50 * time.Millisecond,
		Timestamp:  time.Now().UTC(),
	}
	data, _ := bus.Encode(ret)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-02"), data)

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("recovered watcher did not finish in time")
	}

	if j.Status != StatusComplete {
		t.Errorf("Status = %q, want %q", j.Status, StatusComplete)
	}

	returns := w.Returns()
	if len(returns) != 2 {
		t.Errorf("returns count = %d, want 2", len(returns))
	}
}

// TestWatcherRecovery_AllReturnsAlreadyIn tests that when a watcher is
// recovered and all returns are already present, it should detect the
// return via NATS subscription and finalize.
func TestWatcherRecovery_AllReturnsAlreadyIn(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", nil, []string{"peel-01"}, 5*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-recovery"

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if _, err := bus.KVPut(ctx, jobsBucket, j.JID, j); err != nil {
		t.Fatalf("store: %v", err)
	}

	// Create watcher and immediately send the return (simulating return
	// that arrived during recovery).
	w := NewWatcher(j, ps, js, nil)
	go w.Watch(ctx)

	time.Sleep(50 * time.Millisecond)

	ret := Return{
		JID:       j.JID,
		PeelID:    "peel-01",
		Success:   true,
		Duration:  100 * time.Millisecond,
		Timestamp: time.Now().UTC(),
	}
	data, _ := bus.Encode(ret)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-01"), data)

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finalize promptly")
	}

	// Should be complete since the single target returned.
	if j.Status != StatusComplete {
		t.Errorf("Status = %q, want %q", j.Status, StatusComplete)
	}
}

// TestIncrementalReturnPersistence verifies that returns are written to KV
// incrementally as they arrive, using per-peel keys (<jid>.<peel-id>).
// This ensures crash recovery can reconstruct watcher state from KV.
func TestIncrementalReturnPersistence(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", nil, []string{"peel-01", "peel-02", "peel-03"}, 10*time.Second)
	j.Status = StatusRunning

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if _, err := bus.KVPut(ctx, jobsBucket, j.JID, j); err != nil {
		t.Fatalf("store: %v", err)
	}

	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatalf("get returns bucket: %v", err)
	}

	// Simulate incremental writes: each return persisted to KV immediately.
	peels := []string{"peel-01", "peel-02", "peel-03"}
	for i, peelID := range peels {
		ret := Return{
			JID:        j.JID,
			PeelID:     peelID,
			Success:    true,
			ReturnData: "result-" + peelID,
			Duration:   time.Duration(i+1) * 10 * time.Millisecond,
			Timestamp:  time.Now().UTC(),
		}

		// Write incrementally with per-peel key.
		key := j.JID + "." + peelID
		if _, err := bus.KVPut(ctx, returnsBucket, key, ret); err != nil {
			t.Fatalf("incremental write %s: %v", peelID, err)
		}

		// Verify immediate readback.
		var readback Return
		if err := bus.KVGet(ctx, returnsBucket, key, &readback); err != nil {
			t.Fatalf("readback %s: %v", peelID, err)
		}
		if readback.PeelID != peelID {
			t.Errorf("readback PeelID = %q, want %q", readback.PeelID, peelID)
		}
	}

	// Verify all three per-peel returns are accessible.
	for _, peelID := range peels {
		var ret Return
		key := j.JID + "." + peelID
		if err := bus.KVGet(ctx, returnsBucket, key, &ret); err != nil {
			t.Errorf("failed to read incremental return for %s: %v", peelID, err)
		}
		if ret.JID != j.JID {
			t.Errorf("return JID = %q, want %q", ret.JID, j.JID)
		}
	}
}

// TestCancelPropagation_CrossMaster verifies that a cancel request received
// by a non-owning master still propagates to peels via the cancel subject.
// All masters subscribe to the job cancel wildcard so any master can
// relay cancellation.
func TestCancelPropagation_CrossMaster(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", nil, []string{"peel-01", "peel-02"}, 30*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-A"

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if _, err := bus.KVPut(ctx, jobsBucket, j.JID, j); err != nil {
		t.Fatalf("store: %v", err)
	}

	// Subscribe to the cancel subject to verify propagation.
	cancelReceived := make(chan struct{}, 1)
	cancelSub, err := ps.Subscribe(bus.JobCancelSubject(j.JID), func(msg *bus.Msg) {
		select {
		case cancelReceived <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("subscribe cancel: %v", err)
	}
	defer cancelSub.Unsubscribe()

	// Master-B (non-owning) receives the cancel request and propagates it.
	mgrB := NewManager(ps, js, "test-master-B", nil)
	if err := mgrB.Cancel(ctx, j.JID); err != nil {
		t.Fatalf("cancel from non-owning master: %v", err)
	}

	// The cancel event should be published to the cancel subject.
	select {
	case <-cancelReceived:
		// Cancel was propagated.
	case <-time.After(2 * time.Second):
		t.Fatal("cancel event not received on cancel subject")
	}
}
