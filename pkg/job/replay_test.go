package job

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

// TestManagerReclaimRunningJobMergesReplayedReturns verifies findings 18/30:
// returns published during the ownerless failover window are recovered from
// the job-events stream (via the ReplayReturns hook), merged with KV-seeded
// returns (KV wins on collision), persisted per-peel, and counted toward
// the terminal status. Recovery watchers must not re-publish ExecRequests.
func TestManagerReclaimRunningJobMergesReplayedReturns(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", nil, []string{"peel-01", "peel-02", "peel-03"}, 5*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-dead"
	storeJob(t, js, j)

	// peel-01 was persisted to KV by the dead master before the crash.
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	kvCopy := Return{JID: j.JID, PeelID: "peel-01", Success: true, ReturnData: "kv-copy", Timestamp: time.Now().UTC()}
	if _, err := bus.KVPut(ctx, returnsBucket, j.JID+".peel-01", kvCopy); err != nil {
		t.Fatalf("persist kv return: %v", err)
	}

	// Recovery watchers must never re-publish the ExecRequest.
	cmdPublishes := make(chan struct{}, 8)
	cmdSub, err := ps.Subscribe(bus.CmdSubjectAll(), func(msg *bus.Msg) {
		cmdPublishes <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cmdSub.Unsubscribe()

	// The stream replay saw peel-01 (duplicate of the KV copy — must
	// lose) and peel-02 (published during the ownerless window). The fake
	// is called twice (pre-watch seed + post-subscribe gap merge), the
	// second time from a goroutine, so the JID capture must be
	// mutex-guarded.
	var replayMu sync.Mutex
	var replayedJID string
	mgr := NewManager(ps, js, "master-new", nil)
	mgr.ReplayReturns = func(ctx context.Context, jid string) ([]Return, error) {
		replayMu.Lock()
		replayedJID = jid
		replayMu.Unlock()
		return []Return{
			{JID: jid, PeelID: "peel-01", Success: true, ReturnData: "stream-copy", Timestamp: time.Now().UTC()},
			{JID: jid, PeelID: "peel-02", Success: true, ReturnData: "stream-only", Timestamp: time.Now().UTC()},
		}, nil
	}

	mgr.ReclaimJob(ctx, j)

	replayMu.Lock()
	gotJID := replayedJID
	replayMu.Unlock()
	if gotJID != j.JID {
		t.Fatalf("ReplayReturns called with jid %q, want %q", gotJID, j.JID)
	}

	// peel-03's late return arrives on the new master's watcher.
	time.Sleep(50 * time.Millisecond)
	late := Return{JID: j.JID, PeelID: "peel-03", Success: true, Timestamp: time.Now().UTC()}
	data, _ := bus.Encode(late)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-03"), data)

	waitForNoActiveJobs(t, mgr)

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusComplete {
		t.Errorf("final Status = %q, want %q", final.Status, StatusComplete)
	}
	if final.ReturnCount != 3 || final.SuccessCount != 3 {
		t.Errorf("counts = %d/%d, want 3/3", final.ReturnCount, final.SuccessCount)
	}

	returns, err := mgr.GetReturns(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetReturns: %v", err)
	}
	if len(returns) != 3 {
		t.Fatalf("returns: got %d, want 3 (kv + replayed + late)", len(returns))
	}
	byPeel := map[string]Return{}
	for _, r := range returns {
		byPeel[r.PeelID] = r
	}
	if got := byPeel["peel-01"].ReturnData; got != "kv-copy" {
		t.Errorf("peel-01 data = %v, want kv-copy (KV wins over replay)", got)
	}
	if got := byPeel["peel-02"].ReturnData; got != "stream-only" {
		t.Errorf("peel-02 data = %v, want stream-only (replayed return persisted per-peel)", got)
	}

	select {
	case <-cmdPublishes:
		t.Error("recovery watcher must not re-publish ExecRequests")
	default:
	}
}

// TestManagerReclaimRunningJobReplayCoversAllTargets verifies that a
// recovered watcher whose merged seeds already cover every target finalizes
// immediately instead of burning the remaining deadline.
func TestManagerReclaimRunningJobReplayCoversAllTargets(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", nil, []string{"peel-01", "peel-02"}, 30*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-dead"
	storeJob(t, js, j)

	mgr := NewManager(ps, js, "master-new", nil)
	mgr.ReplayReturns = func(ctx context.Context, jid string) ([]Return, error) {
		return []Return{
			{JID: jid, PeelID: "peel-01", Success: true, Timestamp: time.Now().UTC()},
			{JID: jid, PeelID: "peel-02", Success: true, Timestamp: time.Now().UTC()},
		}, nil
	}

	mgr.ReclaimJob(ctx, j)

	// The 30s deadline is far away: only the seeded-complete fast path
	// lets this finish within the helper's 3s budget.
	waitForNoActiveJobs(t, mgr)

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusComplete {
		t.Errorf("final Status = %q, want %q", final.Status, StatusComplete)
	}
	if final.ReturnCount != 2 {
		t.Errorf("ReturnCount = %d, want 2", final.ReturnCount)
	}

	// Replayed returns were persisted as per-peel keys.
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := bus.ListKeysWithPrefix(ctx, returnsBucket, j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Errorf("per-peel keys: got %d, want 2 (replayed returns persisted)", len(keys))
	}
}

// TestManagerReclaimRunningJobReplayErrorFallsBack verifies that a failing
// replay degrades gracefully to the KV-seeded returns.
func TestManagerReclaimRunningJobReplayErrorFallsBack(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", nil, []string{"peel-01", "peel-02"}, 5*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-dead"
	storeJob(t, js, j)

	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	prev := Return{JID: j.JID, PeelID: "peel-01", Success: true, Timestamp: time.Now().UTC()}
	if _, err := bus.KVPut(ctx, returnsBucket, j.JID+".peel-01", prev); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(ps, js, "master-new", nil)
	mgr.ReplayReturns = func(ctx context.Context, jid string) ([]Return, error) {
		return nil, errors.New("stream unavailable")
	}

	mgr.ReclaimJob(ctx, j)

	time.Sleep(50 * time.Millisecond)
	late := Return{JID: j.JID, PeelID: "peel-02", Success: true, Timestamp: time.Now().UTC()}
	data, _ := bus.Encode(late)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-02"), data)

	waitForNoActiveJobs(t, mgr)

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusComplete {
		t.Errorf("final Status = %q, want %q (replay failure must not lose KV seeds)", final.Status, StatusComplete)
	}
	if final.ReturnCount != 2 {
		t.Errorf("ReturnCount = %d, want 2", final.ReturnCount)
	}
}

// TestManagerReclaimRunningJobGapReplayMergesAfterSubscribe verifies the
// replay→subscribe gap fix (finding C1): a return published after the
// pre-watch stream replay finished but before the recovery watcher's live
// return subscription attached is recovered by the SECOND, post-subscribe
// replay and merged via MergeReturns — the job finalizes complete and the
// return is persisted per-peel, with no live publish and no re-dispatch.
func TestManagerReclaimRunningJobGapReplayMergesAfterSubscribe(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", nil, []string{"peel-gap"}, 30*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-dead"
	storeJob(t, js, j)

	// Recovery watchers must never re-publish the ExecRequest — including
	// when the gap merge completes the job.
	cmdPublishes := make(chan struct{}, 8)
	cmdSub, err := ps.Subscribe(bus.CmdSubjectAll(), func(msg *bus.Msg) {
		cmdPublishes <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cmdSub.Unsubscribe()

	// First replay (pre-watch): the peel's return had not reached the
	// stream reader yet — empty. Second replay (post-subscribe): the
	// return that landed in the gap is now visible.
	var replayMu sync.Mutex
	replayCalls := 0
	mgr := NewManager(ps, js, "master-new", nil)
	mgr.ReplayReturns = func(ctx context.Context, jid string) ([]Return, error) {
		replayMu.Lock()
		defer replayMu.Unlock()
		replayCalls++
		if replayCalls == 1 {
			return nil, nil
		}
		return []Return{
			{JID: jid, PeelID: "peel-gap", Success: true, ReturnData: "gap-return", Timestamp: time.Now().UTC()},
		}, nil
	}

	mgr.ReclaimJob(ctx, j)

	// Only the gap merge can complete the job: the 30s deadline is far
	// away and no live return is ever published.
	waitForNoActiveJobs(t, mgr)

	replayMu.Lock()
	calls := replayCalls
	replayMu.Unlock()
	if calls != 2 {
		t.Errorf("ReplayReturns calls = %d, want 2 (pre-watch seed + post-subscribe gap merge)", calls)
	}

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusComplete {
		t.Errorf("final Status = %q, want %q (gap return must count)", final.Status, StatusComplete)
	}
	if final.ReturnCount != 1 || final.SuccessCount != 1 {
		t.Errorf("counts = %d/%d, want 1/1", final.ReturnCount, final.SuccessCount)
	}

	// The merged return is persisted under its per-peel key.
	returns, err := mgr.GetReturns(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetReturns: %v", err)
	}
	if len(returns) != 1 || returns[0].PeelID != "peel-gap" || returns[0].ReturnData != "gap-return" {
		t.Errorf("persisted returns = %+v, want the merged gap return for peel-gap", returns)
	}

	select {
	case <-cmdPublishes:
		t.Error("recovery watcher must not re-publish ExecRequests")
	default:
	}
}

// TestWatcherMergeReturnsDedup pins MergeReturns semantics: returns for
// peels already collected are skipped (existing copies win) and are not
// re-queued for persistence; new peels are recorded and queued. Calling it
// on a watcher that has not started (nil cancelFunc) must not panic even
// when the merge completes the target set.
func TestWatcherMergeReturnsDedup(t *testing.T) {
	ps, js := testSetup(t)

	j := NewJob("cmd.run", nil, []string{"peel-01", "peel-02"}, time.Second)
	w := NewWatcher(j, ps, js, nil)
	w.SeedReturns([]Return{{JID: j.JID, PeelID: "peel-01", Success: true, ReturnData: "original"}})

	w.MergeReturns([]Return{
		{JID: j.JID, PeelID: "peel-01", Success: true, ReturnData: "stream-dup"},
		{JID: j.JID, PeelID: "peel-02", Success: true, ReturnData: "stream-new"},
		{JID: j.JID, PeelID: "", Success: true}, // no peel ID: dropped
	})

	got := w.Returns()
	if len(got) != 2 {
		t.Fatalf("returns = %d, want 2", len(got))
	}
	if got["peel-01"].ReturnData != "original" {
		t.Errorf("peel-01 data = %v, want original (existing copy wins)", got["peel-01"].ReturnData)
	}
	if got["peel-02"].ReturnData != "stream-new" {
		t.Errorf("peel-02 data = %v, want stream-new", got["peel-02"].ReturnData)
	}

	w.mu.Lock()
	queued := append([]string(nil), w.newReturns...)
	w.mu.Unlock()
	if len(queued) != 1 || queued[0] != "peel-02" {
		t.Errorf("newReturns = %v, want [peel-02] (only the merged-new peel queued for persistence)", queued)
	}
}

// TestNewManagerLeavesReplayNilWithoutConsumerAPI pins the constructor
// wiring contract: bustest.FakeJS does not implement bus.ConsumerAPI, so
// ReplayReturns must stay nil (tests opt in with a fake function).
func TestNewManagerLeavesReplayNilWithoutConsumerAPI(t *testing.T) {
	ps, js := testSetup(t)
	mgr := NewManager(ps, js, "test-master", nil)
	if mgr.ReplayReturns != nil {
		t.Fatal("ReplayReturns must be nil when js does not implement bus.ConsumerAPI")
	}
}
