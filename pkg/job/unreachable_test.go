package job

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
)

// beatHeartbeat writes a live peel-heartbeat entry (value content is
// irrelevant — classification checks key existence only).
func beatHeartbeat(t *testing.T, js bus.JetStreamAPI, peelID string) {
	t.Helper()
	hb, err := bus.GetBucket(context.Background(), js, bus.BucketPeelHeartbeat)
	if err != nil {
		t.Fatalf("get heartbeat bucket: %v", err)
	}
	if _, err := bus.KVPut(context.Background(), hb, peelID, map[string]any{"ts": time.Now().UTC()}); err != nil {
		t.Fatalf("put heartbeat: %v", err)
	}
}

// fetchJobRecord reads the job record back from KV.
func fetchJobRecord(t *testing.T, js bus.JetStreamAPI, jid string) Job {
	t.Helper()
	jobs, err := bus.GetBucket(context.Background(), js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get jobs bucket: %v", err)
	}
	entry, err := jobs.Get(context.Background(), jid)
	if err != nil {
		t.Fatalf("get job %s: %v", jid, err)
	}
	var j Job
	if err := bus.Decode(entry.Value(), &j); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	return j
}

// waitTerminal polls the job record until it reaches a terminal status.
func waitTerminal(t *testing.T, js bus.JetStreamAPI, jid string, timeout time.Duration) Job {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		rec := fetchJobRecord(t, js, jid)
		switch rec.Status {
		case StatusComplete, StatusFailed, StatusPartial, StatusTimeout, StatusCanceled:
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never reached a terminal status (last: %s)", jid, rec.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Classification is a pure presence-set diff: live heartbeat = online,
// anything else = suspected offline. An unreadable bucket disables the fast
// path entirely (nil) — a presence-plane problem must never affect dispatch.
func TestClassifyOfflineTargets(t *testing.T) {
	ps, js := testSetup(t)
	mgr := NewManager(ps, js, "master-classify", nil)
	beatHeartbeat(t, js, "peel-live")

	got := mgr.classifyOfflineTargets(context.Background(), []string{"peel-live", "peel-dead", "peel-gone"})
	want := []string{"peel-dead", "peel-gone"}
	if !slices.Equal(got, want) {
		t.Errorf("classifyOfflineTargets = %v, want %v", got, want)
	}

	// Empty target list: nothing to classify.
	if got := mgr.classifyOfflineTargets(context.Background(), nil); got != nil {
		t.Errorf("classify(nil targets) = %v, want nil", got)
	}

	// Missing heartbeat bucket (fresh FakeJS, no InitializeStorage): the
	// classification is skipped — nil, full-deadline behavior.
	bare := NewManager(bustest.NewFakePubSub(), bustest.NewFakeJS(), "master-bare", nil)
	if got := bare.classifyOfflineTargets(context.Background(), []string{"peel-x"}); got != nil {
		t.Errorf("classify with unavailable bucket = %v, want nil (fast path off)", got)
	}
}

// Dispatch records the presence hint on the job record (audit trail) and
// still publishes to EVERY resolved target — heartbeat state never gates
// delivery.
func TestDispatchRecordsOfflineAtDispatchAndStillDelivers(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()
	counter := newCmdCounter(t, ps)

	mgr := NewManager(ps, js, "master-hint", nil)
	mgr.AckWindow = -1 // no redispatch/fast-path noise in this test
	beatHeartbeat(t, js, "peel-live")

	j := NewJob("test.ping", nil, []string{"peel-live", "peel-dead"}, 2*time.Second)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	t.Cleanup(mgr.Shutdown)

	if !slices.Equal(j.OfflineAtDispatch, []string{"peel-dead"}) {
		t.Errorf("OfflineAtDispatch = %v, want [peel-dead]", j.OfflineAtDispatch)
	}
	rec := fetchJobRecord(t, js, j.JID)
	if !slices.Equal(rec.OfflineAtDispatch, []string{"peel-dead"}) {
		t.Errorf("persisted OfflineAtDispatch = %v, want [peel-dead]", rec.OfflineAtDispatch)
	}
	// Delivery attempted to BOTH targets regardless of presence.
	for _, target := range []string{"peel-live", "peel-dead"} {
		if counter.count(target) != 1 {
			t.Errorf("publishes to %s = %d, want 1 (presence must not gate delivery)", target, counter.count(target))
		}
	}
}

// The fast path end to end: a suspected-offline target that stays completely
// silent is finalized as UNREACHABLE at ~ackWindow+grace — synthetic return
// persisted per-peel, published on the return subject for live listeners,
// job finalized without burning the full timeout.
func TestUnreachableFastPathFinalizesEarly(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgr := NewManager(ps, js, "master-unreach", nil)
	mgr.AckWindow = 100 * time.Millisecond
	mgr.UnreachableGrace = 100 * time.Millisecond
	beatHeartbeat(t, js, "peel-live")

	// The job timeout is LONG — proving early finalize means not waiting it.
	j := NewJob("test.ping", nil, []string{"peel-live", "peel-dead"}, 30*time.Second)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// Capture the synthetic return published for live listeners.
	published := make(chan Return, 1)
	sub, err := ps.Subscribe(bus.JobReturnSubject(j.JID, "peel-dead"), func(msg *bus.Msg) {
		var ret Return
		if bus.Decode(msg.Data, &ret) == nil {
			select {
			case published <- ret:
			default:
			}
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { sub.Unsubscribe() })

	// The live peel returns promptly.
	ret := Return{JID: j.JID, PeelID: "peel-live", Success: true, Timestamp: time.Now().UTC()}
	retData, _ := bus.Encode(ret)
	time.Sleep(30 * time.Millisecond) // let the watcher subscribe
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-live"), retData)

	// The job must finalize at ~ackWindow+grace, not after 30s.
	start := time.Now()
	rec := waitTerminal(t, js, j.JID, 5*time.Second)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("fast path took %s, expected ~ackWindow+grace", elapsed)
	}

	// The synthetic return was published for live listeners.
	select {
	case pub := <-published:
		if !pub.Unreachable || pub.Error != UnreachableError {
			t.Errorf("published synthetic return = %+v, want Unreachable with the canonical error", pub)
		}
	case <-time.After(2 * time.Second):
		t.Error("synthetic unreachable return was never published on the return subject")
	}

	// Job record: finalized with both returns counted, one success.
	if rec.Status != StatusFailed || rec.ReturnCount != 2 || rec.SuccessCount != 1 {
		t.Errorf("job record = status %s returns %d success %d, want failed/2/1", rec.Status, rec.ReturnCount, rec.SuccessCount)
	}

	// Per-peel KV return persisted with the unreachable marker.
	retKV, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatalf("get returns bucket: %v", err)
	}
	var deadRet Return
	if err := bus.KVGet(ctx, retKV, j.JID+".peel-dead", &deadRet); err != nil {
		t.Fatalf("get persisted unreachable return: %v", err)
	}
	if !deadRet.Unreachable || deadRet.Error != UnreachableError || deadRet.Success {
		t.Errorf("persisted return = %+v, want the synthetic UNREACHABLE record", deadRet)
	}
}

// The promotion rule, ack side: a suspected-offline target that ACKS is
// delivery-proven — it must never be marked unreachable, even if it has not
// returned yet (long-running execution). The job then honors its deadline.
func TestUnreachablePromoteOnAck(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgr := NewManager(ps, js, "master-promote", nil)
	mgr.AckWindow = 80 * time.Millisecond
	mgr.UnreachableGrace = 80 * time.Millisecond

	// Single target, no heartbeat: suspected offline.
	j := NewJob("test.ping", nil, []string{"peel-slow"}, 600*time.Millisecond)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// It acks quickly (delivery proven) but never returns.
	time.Sleep(30 * time.Millisecond)
	ack := Ack{JID: j.JID, PeelID: "peel-slow", Timestamp: time.Now().UTC()}
	ackData, _ := bus.Encode(ack)
	ps.Publish(bus.JobAckSubject(j.JID, "peel-slow"), ackData)

	rec := waitTerminal(t, js, j.JID, 5*time.Second)

	// The job timed out normally — no synthetic return was fabricated for
	// an acked target.
	if rec.Status != StatusTimeout || rec.ReturnCount != 0 {
		t.Errorf("job record = status %s returns %d, want timeout/0 (acked target must wait the deadline)", rec.Status, rec.ReturnCount)
	}
	retKV, _ := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	var ghost Return
	if err := bus.KVGet(ctx, retKV, j.JID+".peel-slow", &ghost); err == nil {
		t.Errorf("synthetic return fabricated for an ACKED target: %+v", ghost)
	}
}

// The promotion rule, return side: a suspected-offline target that RETURNS
// before the fast path fires completes the job normally — no unreachable
// marking, real result preserved.
func TestUnreachablePromoteOnReturn(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgr := NewManager(ps, js, "master-promret", nil)
	mgr.AckWindow = 80 * time.Millisecond
	mgr.UnreachableGrace = 200 * time.Millisecond

	j := NewJob("test.ping", nil, []string{"peel-back"}, 10*time.Second)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	ret := Return{JID: j.JID, PeelID: "peel-back", Success: true, Timestamp: time.Now().UTC()}
	retData, _ := bus.Encode(ret)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-back"), retData)

	rec := waitTerminal(t, js, j.JID, 5*time.Second)

	if rec.Status != StatusComplete || rec.SuccessCount != 1 {
		t.Errorf("job record = status %s success %d, want complete/1", rec.Status, rec.SuccessCount)
	}
	retKV, _ := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	var got Return
	if err := bus.KVGet(ctx, retKV, j.JID+".peel-back", &got); err != nil {
		t.Fatalf("get return: %v", err)
	}
	if got.Unreachable || !got.Success {
		t.Errorf("real return was overwritten by the fast path: %+v", got)
	}
}

// A negative grace disables the fast path: a silent suspected-offline target
// burns the job's full timeout (the pre-unreachable behavior).
func TestUnreachableGraceDisabled(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgr := NewManager(ps, js, "master-nograce", nil)
	mgr.AckWindow = 50 * time.Millisecond
	mgr.UnreachableGrace = -1

	j := NewJob("test.ping", nil, []string{"peel-dead"}, 400*time.Millisecond)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	start := time.Now()
	rec := waitTerminal(t, js, j.JID, 5*time.Second)
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("finished after %s — fast path ran despite being disabled", elapsed)
	}
	if rec.Status != StatusTimeout || rec.ReturnCount != 0 {
		t.Errorf("job record = status %s returns %d, want timeout/0", rec.Status, rec.ReturnCount)
	}
}
