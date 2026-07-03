package job

// Tests for the active-jobs index (architecture review finding 8 / B4) and
// the persist-before-publish dispatch ordering (finding 17 / B9), plus the
// Job V/TargetExpr wire fields.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/bus/bustest"
	"github.com/ptorbus/zester/pkg/proto"
)

// TestDispatchActiveKeyLifecycle: Dispatch creates "active.<jid>" with the
// claiming master as owner; the watcher's finalize deletes it.
func TestDispatchActiveKeyLifecycle(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgr := NewManager(ps, js, "master-idx", nil)
	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-01"}, 5*time.Second)

	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	entry, ok := getActiveEntry(t, js, j.JID)
	if !ok {
		t.Fatal("active key not created on dispatch")
	}
	if entry.Owner != "master-idx" {
		t.Errorf("active entry Owner = %q, want master-idx", entry.Owner)
	}
	if entry.Updated.IsZero() {
		t.Error("active entry Updated should be set")
	}

	// Complete the job: the terminal transition must delete the index key.
	time.Sleep(50 * time.Millisecond)
	publishReturn(t, ps, j.JID, "peel-01", true)
	waitForNoActiveJobs(t, mgr)

	if _, ok := getActiveEntry(t, js, j.JID); ok {
		t.Error("active key must be deleted when the job finalizes")
	}
	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusComplete {
		t.Errorf("final Status = %q, want %q", final.Status, StatusComplete)
	}
}

// TestDispatchPublishesOnlyAfterRunningPersisted captures the KV state of
// the job record at the moment each ExecRequest is published: it must
// already be StatusRunning (persist-intent-before-publish, finding 17). A
// StatusClaimed record in KV therefore provably means "never published".
func TestDispatchPublishesOnlyAfterRunningPersisted(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-01"}, 5*time.Second)

	// FakePubSub delivers synchronously, so the handler observes the exact
	// KV state at publish time.
	type observation struct {
		status Status
		req    proto.ExecRequest
	}
	observed := make(chan observation, 1)
	sub, err := ps.Subscribe(bus.CmdSubject("peel-01"), func(msg *bus.Msg) {
		var req proto.ExecRequest
		if err := bus.Decode(msg.Data, &req); err != nil {
			t.Errorf("decode exec request: %v", err)
			return
		}
		var stored Job
		if err := bus.KVGet(ctx, jobsBucket, req.JID, &stored); err != nil {
			t.Errorf("get job at publish time: %v", err)
			return
		}
		observed <- observation{status: stored.Status, req: req}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	mgr := NewManager(ps, js, "master-order", nil)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	select {
	case obs := <-observed:
		if obs.status != StatusRunning {
			t.Errorf("job status at publish time = %q, want %q (persist before publish)",
				obs.status, StatusRunning)
		}
		if obs.req.V != proto.ProtocolVersion {
			t.Errorf("ExecRequest.V = %d, want %d", obs.req.V, proto.ProtocolVersion)
		}
		if obs.req.JID != j.JID {
			t.Errorf("ExecRequest.JID = %q, want %q", obs.req.JID, j.JID)
		}
	default:
		t.Fatal("ExecRequest was not published")
	}
}

// TestDispatchResumesOwnInterruptedClaim: a claimed record owned by this
// master (a dispatch that persisted the claim but died before the running
// CAS) is resumed on retry — CAS to running, then published.
func TestDispatchResumesOwnInterruptedClaim(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-01"}, 30*time.Second)
	j.Status = StatusClaimed
	j.Owner = "master-a"
	storeJob(t, js, j)

	published := make(chan proto.ExecRequest, 1)
	sub, err := ps.Subscribe(bus.CmdSubject("peel-01"), func(msg *bus.Msg) {
		var req proto.ExecRequest
		if err := bus.Decode(msg.Data, &req); err != nil {
			t.Errorf("decode exec request: %v", err)
			return
		}
		published <- req
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	retry := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-01"}, 30*time.Second)
	retry.JID = j.JID
	mgr := NewManager(ps, js, "master-a", nil)
	if err := mgr.Dispatch(ctx, retry); err != nil {
		t.Fatalf("Dispatch resume: %v", err)
	}

	select {
	case req := <-published:
		if req.JID != j.JID {
			t.Errorf("resumed ExecRequest JID = %q, want %q", req.JID, j.JID)
		}
	default:
		t.Fatal("resumed dispatch must publish the ExecRequest")
	}

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusRunning {
		t.Errorf("Status = %q, want %q after resume", final.Status, StatusRunning)
	}
	if entry, ok := getActiveEntry(t, js, j.JID); !ok || entry.Owner != "master-a" {
		t.Errorf("active entry = %+v (ok=%v), want owner master-a", entry, ok)
	}
}

// TestDispatchResumesOwnClaimWithDriftedIntent: the own-claim resume check
// runs BEFORE the intent comparison. Deterministic-JID dispatches (the
// reactor) legitimately re-render with drifted args between attempts; the
// claimed record is provably unpublished, so the retry must resume with the
// requested intent instead of wedging the claim behind ErrJIDConflict.
func TestDispatchResumesOwnClaimWithDriftedIntent(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-01"}, 30*time.Second)
	j.Status = StatusClaimed
	j.Owner = "master-a"
	storeJob(t, js, j)

	published := make(chan proto.ExecRequest, 1)
	sub, err := ps.Subscribe(bus.CmdSubject("peel-01"), func(msg *bus.Msg) {
		var req proto.ExecRequest
		if err := bus.Decode(msg.Data, &req); err != nil {
			t.Errorf("decode exec request: %v", err)
			return
		}
		published <- req
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	retry := NewJob("cmd.run", map[string]any{"cmd": "hostname"}, []string{"peel-01"}, 30*time.Second)
	retry.JID = j.JID
	mgr := NewManager(ps, js, "master-a", nil)
	if err := mgr.Dispatch(ctx, retry); err != nil {
		t.Fatalf("Dispatch resume with drifted intent: %v", err)
	}

	select {
	case req := <-published:
		if req.Args["cmd"] != "hostname" {
			t.Errorf("resumed ExecRequest args = %v, want the requested (drifted) intent", req.Args)
		}
	default:
		t.Fatal("resumed dispatch must publish the ExecRequest")
	}

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusRunning {
		t.Errorf("Status = %q, want %q after resume", final.Status, StatusRunning)
	}
	if final.Args["cmd"] != "hostname" {
		t.Errorf("persisted args = %v, want the requested (drifted) intent", final.Args)
	}
}

// TestDispatchClaimedByOtherMasterIsClaimPending: a claimed record owned by
// ANOTHER master is left alone — no publish, no status change — but the
// collision is NOT reported as success: the record is provably unpublished
// and its live owner may never resume it, so Dispatch returns the typed
// retryable ErrJobClaimPending (regardless of intent match — the claim state
// dominates) and the caller retries until the claim resumes or the orphan
// scanner reclaims it.
func TestDispatchClaimedByOtherMasterIsClaimPending(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-01"}, 30*time.Second)
	j.Status = StatusClaimed
	j.Owner = "master-a"
	storeJob(t, js, j)

	publishCount := 0
	sub, err := ps.Subscribe(bus.CmdSubject("peel-01"), func(*bus.Msg) { publishCount++ })
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	mgrB := NewManager(ps, js, "master-b", nil)
	tests := []struct {
		name   string
		mutate func(c *Job)
	}{
		{name: "same intent", mutate: func(*Job) {}},
		{name: "drifted intent", mutate: func(c *Job) { c.Args = map[string]any{"cmd": "hostname"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retry := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-01"}, 30*time.Second)
			retry.JID = j.JID
			tt.mutate(retry)

			err := mgrB.Dispatch(ctx, retry)
			if !errors.Is(err, ErrJobClaimPending) {
				t.Fatalf("errors.Is(err, ErrJobClaimPending) = false, want true; err = %v", err)
			}
			if errors.Is(err, ErrJIDConflict) {
				t.Error("claim-pending must not classify as ErrJIDConflict (it would be duplicate-suppressed)")
			}
		})
	}

	if publishCount != 0 {
		t.Errorf("published %d ExecRequests, want 0 (other master's claim)", publishCount)
	}
	stored, err := mgrB.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if stored.Status != StatusClaimed || stored.Owner != "master-a" {
		t.Errorf("job = %s/%s, want claimed/master-a (untouched)", stored.Status, stored.Owner)
	}
}

// TestOrphanScannerRewritesActiveKeyOnReclaim: a successful CAS reclaim
// rewrites the index entry with the new owner.
func TestOrphanScannerRewritesActiveKeyOnReclaim(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", nil, []string{"peel-01"}, 30*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-dead"
	storeJob(t, js, j)

	if entry, ok := getActiveEntry(t, js, j.JID); !ok || entry.Owner != "master-dead" {
		t.Fatalf("precondition: active entry = %+v (ok=%v), want owner master-dead", entry, ok)
	}

	// No heartbeats at all: the owner is observed missing.
	s := NewOrphanScanner("master-live", js, nil, nil)
	s.MissThreshold = 1
	s.scan(ctx)

	entry, ok := getActiveEntry(t, js, j.JID)
	if !ok {
		t.Fatal("active key must survive a reclaim (job is still live)")
	}
	if entry.Owner != "master-live" {
		t.Errorf("active entry Owner after reclaim = %q, want master-live", entry.Owner)
	}
}

// TestReclaimLimitExceededDeletesActiveKey: the reclaim-cap finalize is a
// terminal transition and must drop the index entry.
func TestReclaimLimitExceededDeletesActiveKey(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", nil, []string{"peel-01"}, 30*time.Second)
	j.Status = StatusClaimed
	j.Owner = "master-new"
	j.ReclaimCount = maxReclaims + 1
	storeJob(t, js, j)

	mgr := NewManager(ps, js, "master-new", nil)
	mgr.ReclaimJob(ctx, j)

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusFailed {
		t.Fatalf("Status = %q, want %q", final.Status, StatusFailed)
	}
	if _, ok := getActiveEntry(t, js, j.JID); ok {
		t.Error("active key must be deleted on the reclaim-limit finalize path")
	}
}

// TestOrphanScannerReadsOnlyActiveKeys seeds the jobs bucket with terminal
// noise (no active keys, like 7 days of retained history and
// scheduler-synthetic jobs) and one indexed orphan. The scanner must Get
// only the indexed job's record — zero reads of the noise records.
func TestOrphanScannerReadsOnlyActiveKeys(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}

	// Terminal noise: job records without active keys.
	noiseJIDs := make([]string, 0, 50)
	for range 50 {
		n := NewJob("cmd.run", nil, []string{"peel-01"}, time.Second)
		n.Status = StatusComplete
		n.Owner = "master-old"
		data, err := bus.Encode(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := jobsBucket.Create(ctx, n.JID, data); err != nil {
			t.Fatal(err)
		}
		noiseJIDs = append(noiseJIDs, n.JID)
	}

	// One real orphan with an active key.
	orphan := NewJob("cmd.run", nil, []string{"peel-01"}, 30*time.Second)
	orphan.Status = StatusRunning
	orphan.Owner = "master-dead"
	storeJob(t, js, orphan)

	fkv, ok := jobsBucket.(*bustest.FakeKV)
	if !ok {
		t.Fatalf("jobs bucket is %T, want *bustest.FakeKV", jobsBucket)
	}
	fkv.ResetGetCounts()

	reclaimed := 0
	s := NewOrphanScanner("master-live", js, nil, func(context.Context, *Job) { reclaimed++ })
	s.MissThreshold = 1
	s.scan(ctx)

	if reclaimed != 1 {
		t.Errorf("reclaimed = %d, want 1 (the indexed orphan)", reclaimed)
	}
	if got := fkv.GetCount(orphan.JID); got != 1 {
		t.Errorf("Gets on indexed job record = %d, want 1", got)
	}
	for _, jid := range noiseJIDs {
		if got := fkv.GetCount(jid); got != 0 {
			t.Fatalf("scanner read terminal noise record %s %d times, want 0", jid, got)
		}
	}
}

// TestOrphanScannerSelfHealsStaleActiveKeys: an active key whose job record
// is missing, or whose job record is already terminal, is deleted and never
// reclaimed.
func TestOrphanScannerSelfHealsStaleActiveKeys(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}

	// Case (a): active key with no job record at all.
	ghostJID := NewJID()
	ghostEntry, err := bus.Encode(ActiveJobEntry{Owner: "master-dead", Updated: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobsBucket.Create(ctx, ActiveJobKey(ghostJID), ghostEntry); err != nil {
		t.Fatal(err)
	}

	// Case (b): active key whose job record is terminal (finalize-path
	// delete was lost).
	doneJob := NewJob("cmd.run", nil, []string{"peel-01"}, time.Second)
	doneJob.Status = StatusComplete
	doneJob.Owner = "master-dead"
	data, err := bus.Encode(doneJob)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobsBucket.Create(ctx, doneJob.JID, data); err != nil {
		t.Fatal(err)
	}
	doneEntry, err := bus.Encode(ActiveJobEntry{Owner: "master-dead", Updated: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobsBucket.Create(ctx, ActiveJobKey(doneJob.JID), doneEntry); err != nil {
		t.Fatal(err)
	}

	reclaimed := 0
	s := NewOrphanScanner("master-live", js, nil, func(context.Context, *Job) { reclaimed++ })
	s.MissThreshold = 1
	s.scan(ctx)

	if reclaimed != 0 {
		t.Errorf("reclaimed = %d, want 0 (stale keys must not be reclaimed)", reclaimed)
	}
	if _, ok := getActiveEntry(t, js, ghostJID); ok {
		t.Error("active key without job record must be self-heal deleted")
	}
	if _, ok := getActiveEntry(t, js, doneJob.JID); ok {
		t.Error("active key of terminal job must be self-heal deleted")
	}
	// The terminal record itself is untouched.
	var stored Job
	if err := bus.KVGet(ctx, jobsBucket, doneJob.JID, &stored); err != nil {
		t.Fatalf("terminal job record must survive self-healing: %v", err)
	}
	if stored.Status != StatusComplete {
		t.Errorf("terminal job Status = %q, want %q", stored.Status, StatusComplete)
	}
}

// TestScheduledResultCreatesNoActiveKey: scheduler-synthetic jobs are
// terminal at creation, bypass Dispatch, and must never appear in the
// active-jobs index.
func TestScheduledResultCreatesNoActiveKey(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	res := ScheduledResult{
		JID:       NewJID(),
		Entry:     "nightly",
		Module:    "state.highstate",
		Success:   true,
		Duration:  time.Second,
		Timestamp: time.Now().UTC(),
	}
	if err := HandleScheduledResult(ctx, js, "peel-01", res, nil); err != nil {
		t.Fatalf("HandleScheduledResult: %v", err)
	}

	if _, ok := getActiveEntry(t, js, res.JID); ok {
		t.Error("scheduler-synthetic job must not get an active-index key")
	}

	// And the scanner ignores it entirely (no active keys at all).
	reclaimed := 0
	s := NewOrphanScanner("master-live", js, nil, func(context.Context, *Job) { reclaimed++ })
	s.MissThreshold = 1
	s.scan(ctx)
	if reclaimed != 0 {
		t.Errorf("reclaimed = %d, want 0", reclaimed)
	}
}

// TestJobVersionAndTargetExprRoundTrip: NewJob stamps the protocol version;
// V and TargetExpr survive the msgpack round trip; records without the
// fields decode to zero values (additive-only schema evolution: a future
// reader must tolerate their absence).
func TestJobVersionAndTargetExprRoundTrip(t *testing.T) {
	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"web-01"}, 5*time.Second)
	if j.V != proto.ProtocolVersion {
		t.Errorf("NewJob V = %d, want %d", j.V, proto.ProtocolVersion)
	}
	j.TargetExpr = "G@os:ubuntu and web*"

	data, err := bus.Encode(j)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var got Job
	if err := bus.Decode(data, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.V != proto.ProtocolVersion {
		t.Errorf("round-trip V = %d, want %d", got.V, proto.ProtocolVersion)
	}
	if got.TargetExpr != "G@os:ubuntu and web*" {
		t.Errorf("round-trip TargetExpr = %q, want the original expression", got.TargetExpr)
	}

	// Minimal record: fields absent -> zero values.
	minimal := Job{JID: NewJID(), Function: "cmd.run"}
	data, err = bus.Encode(&minimal)
	if err != nil {
		t.Fatalf("encode minimal: %v", err)
	}
	var gotMinimal Job
	if err := bus.Decode(data, &gotMinimal); err != nil {
		t.Fatalf("decode minimal: %v", err)
	}
	if gotMinimal.V != 0 || gotMinimal.TargetExpr != "" {
		t.Errorf("minimal decode V=%d TargetExpr=%q, want zero values", gotMinimal.V, gotMinimal.TargetExpr)
	}
}
