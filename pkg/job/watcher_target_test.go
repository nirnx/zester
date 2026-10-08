package job

import (
	"context"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

// publishReturn (hooks_test.go) publishes a legitimate return on the peel's
// own NATS-permission-scoped return subject; publishAck is its ack twin.
func publishAck(t *testing.T, ps bus.PubSub, jid, peelID string) {
	t.Helper()
	ack := Ack{JID: jid, PeelID: peelID, Timestamp: time.Now().UTC()}
	data, err := bus.Encode(ack)
	if err != nil {
		t.Fatal(err)
	}
	if err := ps.Publish(bus.JobAckSubject(jid, peelID), data); err != nil {
		t.Fatal(err)
	}
}

// assertNoPersistedReturn fails when a per-peel return key exists for peelID.
func assertNoPersistedReturn(t *testing.T, js bus.JetStreamAPI, jid, peelID string) {
	t.Helper()
	returnsBucket, err := bus.GetBucket(context.Background(), js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Return
	if err := bus.KVGet(context.Background(), returnsBucket, jid+"."+peelID, &persisted); err == nil {
		t.Errorf("non-target return persisted under %s.%s: %+v", jid, peelID, persisted)
	}
}

// TestWatcherIgnoresReturnsFromNonTargetPeels pins the target gate on the
// live return path: a peel that is NOT a job target can publish a perfectly
// well-formed return on its OWN subject (its JWT allows
// zester.job.*.return.<own-id> for any jid it can guess — reactor JIDs are
// content-addressed). Such returns must be dropped: never recorded, never
// persisted, and never allowed to reach TargetCount and finalize the job
// early. Two non-target peels are used so that, without the gate, their
// returns alone would have completed this two-target job.
func TestWatcherIgnoresReturnsFromNonTargetPeels(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-a", "peel-b"}, 10*time.Second)
	j.Status = StatusRunning
	storeJob(t, js, j)

	w := NewWatcher(j, ps, js, nil)
	go w.Watch(ctx)
	time.Sleep(50 * time.Millisecond)

	// Two intruders return: enough to hit TargetCount if they were counted.
	publishReturn(t, ps, j.JID, "peel-x", true)
	publishReturn(t, ps, j.JID, "peel-y", false)
	// The same intruder again: exercises the warn-once bookkeeping path.
	publishReturn(t, ps, j.JID, "peel-x", true)

	if got := w.Returns(); len(got) != 0 {
		t.Fatalf("non-target returns were recorded: %v", got)
	}
	select {
	case <-w.Done():
		t.Fatal("watcher finalized on non-target returns")
	default:
	}
	assertNoPersistedReturn(t, js, j.JID, "peel-x")
	assertNoPersistedReturn(t, js, j.JID, "peel-y")

	// Real targets still complete the job normally.
	publishReturn(t, ps, j.JID, "peel-a", true)
	publishReturn(t, ps, j.JID, "peel-b", true)

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish after the real targets returned")
	}
	if j.Status != StatusComplete {
		t.Errorf("Status = %q, want %q", j.Status, StatusComplete)
	}
	if j.ReturnCount != 2 || j.SuccessCount != 2 {
		t.Errorf("counts = %d/%d, want 2/2", j.ReturnCount, j.SuccessCount)
	}
	returns := w.Returns()
	if len(returns) != 2 {
		t.Fatalf("returns = %v, want exactly the two targets", returns)
	}
	for _, peel := range []string{"peel-x", "peel-y"} {
		if _, ok := returns[peel]; ok {
			t.Errorf("non-target %s present in collected returns", peel)
		}
	}
}

// TestWatcherIgnoresAcksFromNonTargetPeels verifies the same gate on acks: a
// non-target ack is not recorded, so the ack set only ever describes targets
// (redispatchSilent and markUnreachable consult it).
func TestWatcherIgnoresAcksFromNonTargetPeels(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-a"}, 500*time.Millisecond)
	j.Status = StatusRunning
	storeJob(t, js, j)

	w := NewWatcher(j, ps, js, nil)
	go w.Watch(ctx)
	time.Sleep(50 * time.Millisecond)

	publishAck(t, ps, j.JID, "peel-x")
	if got := w.Acks(); len(got) != 0 {
		t.Fatalf("non-target ack was recorded: %v", got)
	}

	publishAck(t, ps, j.JID, "peel-a")
	acks := w.Acks()
	if len(acks) != 1 {
		t.Fatalf("acks = %v, want only the target's", acks)
	}
	if _, ok := acks["peel-a"]; !ok {
		t.Errorf("target ack missing: %v", acks)
	}

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish")
	}
}

// TestRedispatchIgnoresNonTargetAck closes the loop on the ack gate through
// the Manager: a non-target peel acking (and even returning) within the ack
// window must neither be counted as "heard" nor suppress the one-shot
// re-dispatch owed to the genuinely silent target.
func TestRedispatchIgnoresNonTargetAck(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	counter := newCmdCounter(t, ps)

	mgr := NewManager(ps, js, "master-nontarget", nil)
	mgr.AckWindow = 100 * time.Millisecond

	j := NewJob("cmd.run", nil, []string{"peel-silent"}, 5*time.Second)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	// Only a non-target speaks inside the window.
	publishAck(t, ps, j.JID, "peel-x")
	publishReturn(t, ps, j.JID, "peel-x", true)

	deadline := time.Now().Add(2 * time.Second)
	for counter.count("peel-silent") < 2 {
		if time.Now().After(deadline) {
			t.Fatal("silent target was not re-dispatched")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := counter.count("peel-x"); got != 0 {
		t.Errorf("non-target received %d ExecRequest publishes, want 0", got)
	}

	// The non-target's return must not have completed the single-target job.
	if len(mgr.ActiveJobs()) == 0 {
		t.Fatal("job finalized on a non-target return")
	}

	mgr.Cancel(ctx, j.JID)
	waitForNoActiveJobs(t, mgr)
}

// TestWatcherSeedAndMergeIgnoreNonTargetReturns pins the gate on every
// durable-source path a reclaim uses: KV seed (SeedReturns), pre-watch stream
// seed (SeedReturnsPersist), and post-subscribe gap merge (MergeReturns).
// Non-target returns are dropped from the collected set AND from the
// persistence queue, while target returns keep their existing semantics.
func TestWatcherSeedAndMergeIgnoreNonTargetReturns(t *testing.T) {
	ps, js := testSetup(t)

	j := NewJob("cmd.run", nil, []string{"peel-01", "peel-02", "peel-03"}, time.Second)
	w := NewWatcher(j, ps, js, nil)

	w.SeedReturns([]Return{
		{JID: j.JID, PeelID: "peel-01", Success: true, ReturnData: "kv"},
		{JID: j.JID, PeelID: "peel-x", Success: true, ReturnData: "kv-intruder"},
	})
	w.SeedReturnsPersist([]Return{
		{JID: j.JID, PeelID: "peel-02", Success: true, ReturnData: "stream"},
		{JID: j.JID, PeelID: "peel-y", Success: true, ReturnData: "stream-intruder"},
	})
	w.MergeReturns([]Return{
		{JID: j.JID, PeelID: "peel-03", Success: true, ReturnData: "gap"},
		{JID: j.JID, PeelID: "peel-z", Success: true, ReturnData: "gap-intruder"},
		{JID: j.JID, PeelID: "", Success: true},
	})

	got := w.Returns()
	if len(got) != 3 {
		t.Fatalf("returns = %v, want exactly the three targets", got)
	}
	for peel, want := range map[string]string{"peel-01": "kv", "peel-02": "stream", "peel-03": "gap"} {
		if got[peel].ReturnData != want {
			t.Errorf("%s data = %v, want %v", peel, got[peel].ReturnData, want)
		}
	}
	for _, peel := range []string{"peel-x", "peel-y", "peel-z", ""} {
		if _, ok := got[peel]; ok {
			t.Errorf("non-target %q present in collected returns", peel)
		}
	}

	w.mu.Lock()
	queued := append([]string(nil), w.newReturns...)
	w.mu.Unlock()
	if len(queued) != 2 || queued[0] != "peel-02" || queued[1] != "peel-03" {
		t.Errorf("newReturns = %v, want [peel-02 peel-03] (non-targets never queued for persistence)", queued)
	}
}

// TestManagerReclaimIgnoresNonTargetReplayedReturns runs the gate through a
// real reclaim: the job-events stream replay hands back a return from a
// non-target peel (which the stream captures like any other publish on a
// return subject). The recovery watcher must keep waiting for the real
// target, never persist the intruder's return, and finalize correctly once
// the target's live return arrives.
func TestManagerReclaimIgnoresNonTargetReplayedReturns(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("cmd.run", nil, []string{"peel-target"}, 30*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-dead"
	storeJob(t, js, j)

	mgr := NewManager(ps, js, "master-new", nil)
	mgr.ReplayReturns = func(ctx context.Context, jid string) ([]Return, error) {
		return []Return{
			{JID: jid, PeelID: "peel-x", Success: true, ReturnData: "intruder", Timestamp: time.Now().UTC()},
		}, nil
	}

	mgr.ReclaimJob(ctx, j)

	// Both replays (pre-watch seed + post-subscribe gap merge) only ever
	// yield the intruder: the single-target job must still be active.
	time.Sleep(100 * time.Millisecond)
	if len(mgr.ActiveJobs()) == 0 {
		t.Fatal("reclaimed job finalized on a non-target replayed return")
	}
	assertNoPersistedReturn(t, js, j.JID, "peel-x")

	publishReturn(t, ps, j.JID, "peel-target", true)
	waitForNoActiveJobs(t, mgr)

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusComplete {
		t.Errorf("final Status = %q, want %q", final.Status, StatusComplete)
	}
	if final.ReturnCount != 1 || final.SuccessCount != 1 {
		t.Errorf("counts = %d/%d, want 1/1 (intruder must not count)", final.ReturnCount, final.SuccessCount)
	}
	returns, err := mgr.GetReturns(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetReturns: %v", err)
	}
	if len(returns) != 1 || returns[0].PeelID != "peel-target" {
		t.Errorf("persisted returns = %+v, want only peel-target", returns)
	}
}
