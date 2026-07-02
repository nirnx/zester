package job

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/proto"
)

// storeJob writes a job to the jobs bucket via Create and sets its Epoch
// from the resulting revision (mirrors Dispatch's claim step). Non-terminal
// jobs also get their active-index key ("active.<jid>"), like Dispatch
// creates, so the orphan scanner can see them.
func storeJob(t *testing.T, js bus.JetStreamAPI, j *Job) uint64 {
	t.Helper()
	ctx := context.Background()

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get jobs bucket: %v", err)
	}
	data, err := bus.Encode(j)
	if err != nil {
		t.Fatalf("encode job: %v", err)
	}
	rev, err := jobsBucket.Create(ctx, j.JID, data)
	if err != nil {
		t.Fatalf("store job: %v", err)
	}
	j.Epoch = rev

	if !j.IsTerminal() {
		entry, err := bus.Encode(ActiveJobEntry{Owner: j.Owner, Updated: time.Now().UTC()})
		if err != nil {
			t.Fatalf("encode active entry: %v", err)
		}
		if _, err := jobsBucket.Create(ctx, ActiveJobKey(j.JID), entry); err != nil {
			t.Fatalf("store active key: %v", err)
		}
	}
	return rev
}

// getActiveEntry reads the active-index entry for a JID; ok is false when
// the key does not exist (deleted or never created).
func getActiveEntry(t *testing.T, js bus.JetStreamAPI, jid string) (ActiveJobEntry, bool) {
	t.Helper()
	ctx := context.Background()

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get jobs bucket: %v", err)
	}
	var entry ActiveJobEntry
	if err := bus.KVGet(ctx, jobsBucket, ActiveJobKey(jid), &entry); err != nil {
		return ActiveJobEntry{}, false
	}
	return entry, true
}

// waitForNoActiveJobs polls until the manager's watchers have all finished.
func waitForNoActiveJobs(t *testing.T, mgr *Manager) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(mgr.ActiveJobs()) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("watchers did not finish in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestOrphanScannerReclaimsDeadMastersJob exercises the production
// OrphanScanner: a running job owned by a master whose heartbeat expired
// is reclaimed via CAS, the reclaim callback fires with the new owner and
// bumped epoch, and the dead master's stale epoch is fenced out.
func TestOrphanScannerReclaimsDeadMastersJob(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	ttl := 300 * time.Millisecond
	shortHeartbeatTTL(t, js, ttl)

	hbBucket, err := bus.GetBucket(ctx, js, bus.BucketMasterHeartbeat)
	if err != nil {
		t.Fatalf("get heartbeat bucket: %v", err)
	}

	// The soon-to-be-dead master writes one heartbeat, then stops.
	deadMaster := "master-dead"
	if _, err := bus.KVPut(ctx, hbBucket, deadMaster, MasterHeartbeat{
		MasterID: deadMaster, Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("dead master heartbeat: %v", err)
	}

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-01"}, 30*time.Second)
	j.Status = StatusRunning
	j.Owner = deadMaster
	origRev := storeJob(t, js, j)

	// Let the dead master's heartbeat expire, then keep the scanning
	// master alive with a fresh heartbeat.
	time.Sleep(ttl + 100*time.Millisecond)
	if _, err := bus.KVPut(ctx, hbBucket, "master-live", MasterHeartbeat{
		MasterID: "master-live", Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("live master heartbeat: %v", err)
	}

	var reclaimed []*Job
	s := NewOrphanScanner("master-live", js, nil, func(_ context.Context, rj *Job) {
		reclaimed = append(reclaimed, rj)
	})
	// This test exercises the reclaim mechanics, not the grace period
	// (covered by TestOrphanScannerRequiresConsecutiveMisses).
	s.MissThreshold = 1
	s.scan(ctx)

	if len(reclaimed) != 1 {
		t.Fatalf("reclaimed jobs: got %d, want 1", len(reclaimed))
	}
	got := reclaimed[0]
	if got.JID != j.JID {
		t.Errorf("reclaimed JID = %q, want %q", got.JID, j.JID)
	}
	if got.Owner != "master-live" {
		t.Errorf("reclaimed Owner = %q, want master-live", got.Owner)
	}
	if got.Epoch <= origRev {
		t.Errorf("reclaimed Epoch = %d, want > %d (bumped by CAS)", got.Epoch, origRev)
	}
	if got.ReclaimCount != 1 {
		t.Errorf("reclaimed ReclaimCount = %d, want 1", got.ReclaimCount)
	}

	// KV record reflects the new owner.
	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get jobs bucket: %v", err)
	}
	var stored Job
	if err := bus.KVGet(ctx, jobsBucket, j.JID, &stored); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if stored.Owner != "master-live" {
		t.Errorf("stored Owner = %q, want master-live", stored.Owner)
	}
	if stored.Status != StatusRunning {
		t.Errorf("stored Status = %q, want %q", stored.Status, StatusRunning)
	}
	if stored.ReclaimCount != 1 {
		t.Errorf("stored ReclaimCount = %d, want 1 (persisted with CAS ownership update)", stored.ReclaimCount)
	}

	// The dead master's pre-reclaim epoch is fenced: CAS with it must fail.
	staleData, err := bus.Encode(j)
	if err != nil {
		t.Fatalf("encode stale: %v", err)
	}
	if _, err := jobsBucket.Update(ctx, j.JID, staleData, origRev); err == nil {
		t.Fatal("stale-epoch CAS should fail after reclaim")
	}
}

// TestOrphanScannerSkipsNonOrphans verifies the scanner ignores jobs owned
// by live masters, terminal jobs, its own jobs, and unclaimed jobs.
func TestOrphanScannerSkipsNonOrphans(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	hbBucket, err := bus.GetBucket(ctx, js, bus.BucketMasterHeartbeat)
	if err != nil {
		t.Fatalf("get heartbeat bucket: %v", err)
	}
	if _, err := bus.KVPut(ctx, hbBucket, "master-alive", MasterHeartbeat{
		MasterID: "master-alive", Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("heartbeat put: %v", err)
	}

	mk := func(owner string, status Status) {
		j := NewJob("cmd.run", nil, []string{"peel-01"}, 10*time.Second)
		j.Status = status
		j.Owner = owner
		storeJob(t, js, j)
	}
	mk("master-alive", StatusRunning) // owner alive
	mk("master-gone", StatusComplete) // terminal, even though owner is dead
	mk("master-self", StatusRunning)  // scanner's own job
	mk("", StatusRunning)             // unclaimed (no owner yet)

	reclaimed := 0
	s := NewOrphanScanner("master-self", js, nil, func(context.Context, *Job) {
		reclaimed++
	})
	s.scan(ctx)

	if reclaimed != 0 {
		t.Errorf("reclaimed = %d, want 0", reclaimed)
	}
}

// TestManagerReclaimJobClaimedRedispatches exercises the "claimed" reclaim
// path: the job is re-dispatched to its target peels with an ExecRequest,
// CAS-updated to running with a bumped epoch, and watched to completion.
func TestManagerReclaimJobClaimedRedispatches(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-01"}, 5*time.Second)
	j.Status = StatusClaimed
	j.Owner = "master-new"
	origRev := storeJob(t, js, j)

	// Capture the re-dispatched ExecRequest.
	received := make(chan proto.ExecRequest, 1)
	sub, err := ps.Subscribe(bus.CmdSubject("peel-01"), func(msg *bus.Msg) {
		var req proto.ExecRequest
		if err := bus.Decode(msg.Data, &req); err != nil {
			t.Errorf("decode exec request: %v", err)
			return
		}
		received <- req
	})
	if err != nil {
		t.Fatalf("subscribe cmd subject: %v", err)
	}
	defer sub.Unsubscribe()

	mgr := NewManager(ps, js, "master-new", nil)
	mgr.ReclaimJob(ctx, j)

	select {
	case req := <-received:
		if req.JID != j.JID {
			t.Errorf("ExecRequest JID = %q, want %q", req.JID, j.JID)
		}
		if req.Module != "cmd.run" {
			t.Errorf("ExecRequest Module = %q, want cmd.run", req.Module)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ExecRequest was not re-dispatched to the target peel")
	}

	if j.Status != StatusRunning {
		t.Errorf("Status = %q, want %q", j.Status, StatusRunning)
	}
	if j.Epoch <= origRev {
		t.Errorf("Epoch = %d, want > %d (bumped by CAS update to running)", j.Epoch, origRev)
	}
	if len(mgr.ActiveJobs()) != 1 {
		t.Errorf("ActiveJobs = %d, want 1 (recovery watcher)", len(mgr.ActiveJobs()))
	}

	// Complete the job so the recovery watcher finalizes.
	time.Sleep(50 * time.Millisecond)
	ret := Return{JID: j.JID, PeelID: "peel-01", Success: true, Timestamp: time.Now().UTC()}
	data, _ := bus.Encode(ret)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-01"), data)

	waitForNoActiveJobs(t, mgr)

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusComplete {
		t.Errorf("final Status = %q, want %q", final.Status, StatusComplete)
	}
}

// TestManagerReclaimJobRunningRecoversPersistedReturns exercises the
// "running" reclaim path: returns persisted per-peel by the previous
// master are recovered (recoverPerPeelReturns) and seeded into the new
// watcher, which finalizes once the outstanding peel returns.
func TestManagerReclaimJobRunningRecoversPersistedReturns(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", nil, []string{"peel-01", "peel-02"}, 5*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-new"
	storeJob(t, js, j)

	// peel-01's return was persisted by the previous master before it died.
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatalf("get returns bucket: %v", err)
	}
	prev := Return{JID: j.JID, PeelID: "peel-01", Success: true, Timestamp: time.Now().UTC()}
	if _, err := bus.KVPut(ctx, returnsBucket, j.JID+".peel-01", prev); err != nil {
		t.Fatalf("persist previous return: %v", err)
	}

	mgr := NewManager(ps, js, "master-new", nil)
	mgr.ReclaimJob(ctx, j)

	// peel-02's late return reaches the new master's watcher.
	time.Sleep(50 * time.Millisecond)
	ret := Return{JID: j.JID, PeelID: "peel-02", Success: true, Timestamp: time.Now().UTC()}
	data, _ := bus.Encode(ret)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-02"), data)

	waitForNoActiveJobs(t, mgr)

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusComplete {
		t.Errorf("final Status = %q, want %q", final.Status, StatusComplete)
	}

	returns, err := mgr.GetReturns(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetReturns: %v", err)
	}
	if len(returns) != 2 {
		t.Fatalf("returns: got %d, want 2 (seeded + late)", len(returns))
	}
	peels := map[string]bool{}
	for _, r := range returns {
		peels[r.PeelID] = true
	}
	if !peels["peel-01"] || !peels["peel-02"] {
		t.Errorf("returns peels = %v, want peel-01 and peel-02", peels)
	}
}

// TestOrphanScannerAbortsOnLivenessError verifies that a KV failure while
// reading heartbeats aborts the whole scan cycle: no reclaims, no job
// mutation, and no consecutive-miss bookkeeping.
func TestOrphanScannerAbortsOnLivenessError(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	// An orphaned running job that WOULD be reclaimed if the scan ran.
	j := NewJob("cmd.run", nil, []string{"peel-01"}, 30*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-dead"
	origRev := storeJob(t, js, j)

	failing := &failingListJS{
		JetStreamAPI: js,
		bucket:       bus.BucketMasterHeartbeat,
		err:          errors.New("transient jetstream blip"),
	}

	reclaimed := 0
	s := NewOrphanScanner("master-live", failing, nil, func(context.Context, *Job) {
		reclaimed++
	})
	s.MissThreshold = 1 // even the most aggressive threshold must not reclaim
	s.scan(ctx)
	s.scan(ctx)

	if reclaimed != 0 {
		t.Errorf("reclaimed = %d, want 0 (scan must abort on liveness error)", reclaimed)
	}
	if len(s.missCounts) != 0 {
		t.Errorf("missCounts = %v, want empty (aborted scans must not count misses)", s.missCounts)
	}

	// The job record is untouched.
	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get jobs bucket: %v", err)
	}
	entry, err := jobsBucket.Get(ctx, j.JID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if entry.Revision() != origRev {
		t.Errorf("job revision = %d, want %d (unchanged)", entry.Revision(), origRev)
	}
}

// TestOrphanScannerRequiresConsecutiveMisses verifies the default grace
// period: an owner absent for a single scan cycle is NOT reclaimed; it
// takes two consecutive missing cycles (DefaultMissThreshold).
func TestOrphanScannerRequiresConsecutiveMisses(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	// No heartbeats at all: liveness read is genuinely empty (not an
	// error), so the scan proceeds and observes the owner missing.
	j := NewJob("cmd.run", nil, []string{"peel-01"}, 30*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-dead"
	origRev := storeJob(t, js, j)

	var reclaimed []*Job
	s := NewOrphanScanner("master-live", js, nil, func(_ context.Context, rj *Job) {
		reclaimed = append(reclaimed, rj)
	})
	if s.MissThreshold != DefaultMissThreshold {
		t.Fatalf("MissThreshold default = %d, want %d", s.MissThreshold, DefaultMissThreshold)
	}

	// First scan: one miss -> grace period, no reclaim, job untouched.
	s.scan(ctx)
	if len(reclaimed) != 0 {
		t.Fatalf("reclaimed after 1 miss = %d, want 0 (grace period)", len(reclaimed))
	}
	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get jobs bucket: %v", err)
	}
	entry, err := jobsBucket.Get(ctx, j.JID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if entry.Revision() != origRev {
		t.Errorf("job revision after 1 miss = %d, want %d (unchanged)", entry.Revision(), origRev)
	}

	// Second consecutive scan: threshold reached -> reclaim.
	s.scan(ctx)
	if len(reclaimed) != 1 {
		t.Fatalf("reclaimed after 2 misses = %d, want 1", len(reclaimed))
	}
	if reclaimed[0].ReclaimCount != 1 {
		t.Errorf("ReclaimCount = %d, want 1", reclaimed[0].ReclaimCount)
	}
	var stored Job
	if err := bus.KVGet(ctx, jobsBucket, j.JID, &stored); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if stored.Owner != "master-live" {
		t.Errorf("stored Owner = %q, want master-live", stored.Owner)
	}
	if stored.ReclaimCount != 1 {
		t.Errorf("stored ReclaimCount = %d, want 1", stored.ReclaimCount)
	}
}

// TestOrphanScannerMissCountResetsWhenOwnerAlive verifies that a master
// reappearing in the live set resets its consecutive-miss count: a
// flapping heartbeat never accumulates enough misses to trigger reclaim.
func TestOrphanScannerMissCountResetsWhenOwnerAlive(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	owner := "master-flappy"
	j := NewJob("cmd.run", nil, []string{"peel-01"}, 30*time.Second)
	j.Status = StatusRunning
	j.Owner = owner
	storeJob(t, js, j)

	hbBucket, err := bus.GetBucket(ctx, js, bus.BucketMasterHeartbeat)
	if err != nil {
		t.Fatalf("get heartbeat bucket: %v", err)
	}

	reclaimed := 0
	s := NewOrphanScanner("master-live", js, nil, func(context.Context, *Job) {
		reclaimed++
	})

	// Miss 1: no heartbeat yet.
	s.scan(ctx)
	if reclaimed != 0 {
		t.Fatalf("reclaimed after 1 miss = %d, want 0", reclaimed)
	}

	// Owner heartbeats again: its miss count must reset.
	if _, err := bus.KVPut(ctx, hbBucket, owner, MasterHeartbeat{
		MasterID: owner, Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("owner heartbeat: %v", err)
	}
	s.scan(ctx)
	if reclaimed != 0 {
		t.Fatalf("reclaimed while owner alive = %d, want 0", reclaimed)
	}
	if len(s.missCounts) != 0 {
		t.Errorf("missCounts = %v, want empty after owner seen alive", s.missCounts)
	}

	// Heartbeat disappears again: the count starts over at 1 -> grace.
	if err := hbBucket.Delete(ctx, owner); err != nil {
		t.Fatalf("delete heartbeat: %v", err)
	}
	s.scan(ctx)
	if reclaimed != 0 {
		t.Fatalf("reclaimed after 1 fresh miss = %d, want 0 (count must restart)", reclaimed)
	}

	// Second consecutive miss: now the reclaim happens.
	s.scan(ctx)
	if reclaimed != 1 {
		t.Fatalf("reclaimed after 2 fresh misses = %d, want 1", reclaimed)
	}
}

// TestManagerReclaimJobExceedingLimitFinalizesFailed verifies the reclaim
// cap: a job reclaimed more than maxReclaims times is finalized as failed
// (reason recorded) instead of being re-dispatched or re-watched.
func TestManagerReclaimJobExceedingLimitFinalizesFailed(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-01"}, 30*time.Second)
	j.Status = StatusClaimed
	j.Owner = "master-new"
	j.ReclaimCount = maxReclaims + 1
	storeJob(t, js, j)

	// No ExecRequest may be re-published past the limit.
	dispatched := make(chan struct{}, 1)
	sub, err := ps.Subscribe(bus.CmdSubject("peel-01"), func(*bus.Msg) {
		dispatched <- struct{}{}
	})
	if err != nil {
		t.Fatalf("subscribe cmd subject: %v", err)
	}
	defer sub.Unsubscribe()

	mgr := NewManager(ps, js, "master-new", nil)
	mgr.ReclaimJob(ctx, j)

	if n := len(mgr.ActiveJobs()); n != 0 {
		t.Errorf("ActiveJobs = %d, want 0 (no watcher past reclaim limit)", n)
	}
	select {
	case <-dispatched:
		t.Fatal("job must not be re-dispatched past the reclaim limit")
	case <-time.After(100 * time.Millisecond):
	}

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusFailed {
		t.Errorf("final Status = %q, want %q", final.Status, StatusFailed)
	}
	reason := final.Metadata["failed_reason"]
	if !strings.Contains(reason, "reclaim limit exceeded") {
		t.Errorf("failed_reason = %q, want reclaim-limit reason recorded", reason)
	}
}

// TestManagerReclaimRunningJobExpiredDeadline verifies that a recovered
// watcher honors the job's remaining time budget: with the deadline
// already past, the job finalizes immediately (partial, from the returns
// persisted by the previous master) instead of waiting a fresh timeout.
func TestManagerReclaimRunningJobExpiredDeadline(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	// Timeout is long (30s); only an expired-deadline fast path can
	// finalize within the few seconds this test allows.
	j := NewJob("cmd.run", nil, []string{"peel-01", "peel-02"}, 30*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-new"
	j.ReclaimCount = 1
	j.Deadline = time.Now().UTC().Add(-time.Second) // budget exhausted
	storeJob(t, js, j)

	// One of two returns was persisted by the previous master.
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatalf("get returns bucket: %v", err)
	}
	prev := Return{JID: j.JID, PeelID: "peel-01", Success: true, Timestamp: time.Now().UTC()}
	if _, err := bus.KVPut(ctx, returnsBucket, j.JID+".peel-01", prev); err != nil {
		t.Fatalf("persist previous return: %v", err)
	}

	mgr := NewManager(ps, js, "master-new", nil)
	start := time.Now()
	mgr.ReclaimJob(ctx, j)
	waitForNoActiveJobs(t, mgr) // fails the test after 3s

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("finalize took %v, want immediate (no fresh 30s timeout)", elapsed)
	}

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusPartial {
		t.Errorf("final Status = %q, want %q (1 of 2 returns collected)", final.Status, StatusPartial)
	}
}

// TestManagerReclaimRunningJobZeroDeadlineDerived verifies the defensive
// deadline normalization: dispatched jobs always carry a deadline, so a
// zero value (only producible by a hand-crafted KV record) is derived as
// Created + Timeout and then honored like any other deadline. Here the
// derived deadline is long expired, so the recovery watcher finalizes
// immediately instead of granting a fresh full-timeout budget.
func TestManagerReclaimRunningJobZeroDeadlineDerived(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	// Hand-crafted record: Deadline is the zero value and the job was
	// created an hour ago with a 300ms timeout.
	j := &Job{
		JID:      NewJID(),
		Function: "cmd.run",
		Targets:  []string{"peel-01"},
		Timeout:  300 * time.Millisecond,
		Status:   StatusRunning,
		Owner:    "master-new",
		Created:  time.Now().UTC().Add(-time.Hour),
		Updated:  time.Now().UTC(),
	}
	storeJob(t, js, j)

	mgr := NewManager(ps, js, "master-new", nil)
	start := time.Now()
	mgr.ReclaimJob(ctx, j)
	waitForNoActiveJobs(t, mgr)

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("finalize took %v, want immediate (derived deadline already expired)", elapsed)
	}

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusTimeout {
		t.Errorf("final Status = %q, want %q (no returns collected)", final.Status, StatusTimeout)
	}
}

// TestManagerReclaimClaimedJobExpiredDeadlineNoRedispatch verifies that a
// claimed-but-never-dispatched job whose deadline passed is finalized
// without re-publishing ExecRequests to peels.
func TestManagerReclaimClaimedJobExpiredDeadlineNoRedispatch(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-01"}, 30*time.Second)
	j.Status = StatusClaimed
	j.Owner = "master-new"
	j.ReclaimCount = 1
	j.Deadline = time.Now().UTC().Add(-time.Second)
	storeJob(t, js, j)

	dispatched := make(chan struct{}, 1)
	sub, err := ps.Subscribe(bus.CmdSubject("peel-01"), func(*bus.Msg) {
		dispatched <- struct{}{}
	})
	if err != nil {
		t.Fatalf("subscribe cmd subject: %v", err)
	}
	defer sub.Unsubscribe()

	mgr := NewManager(ps, js, "master-new", nil)
	mgr.ReclaimJob(ctx, j)
	waitForNoActiveJobs(t, mgr)

	select {
	case <-dispatched:
		t.Fatal("expired claimed job must not be re-dispatched")
	case <-time.After(100 * time.Millisecond):
	}

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusTimeout {
		t.Errorf("final Status = %q, want %q (nothing executed, nothing returned)", final.Status, StatusTimeout)
	}
}
