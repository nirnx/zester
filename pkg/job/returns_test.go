package job

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
)

// TestWatcherFinalizeWritesCountsNotAggregate verifies the finding-9 fix:
// finalize flushes per-peel returns and records ReturnCount/SuccessCount on
// the job record instead of writing the ~1MB-bounded aggregated value.
func TestWatcherFinalizeWritesCountsNotAggregate(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-01", "peel-02"}, 10*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-counts"
	storeJob(t, js, j)

	w := NewWatcher(j, ps, js, nil)
	go w.Watch(ctx)

	time.Sleep(50 * time.Millisecond)

	ok := Return{JID: j.JID, PeelID: "peel-01", Success: true, Timestamp: time.Now().UTC()}
	bad := Return{JID: j.JID, PeelID: "peel-02", Success: false, Error: "boom", Timestamp: time.Now().UTC()}
	for _, ret := range []Return{ok, bad} {
		data, _ := bus.Encode(ret)
		ps.Publish(bus.JobReturnSubject(j.JID, ret.PeelID), data)
	}

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish in time")
	}

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	var stored Job
	if err := bus.KVGet(ctx, jobsBucket, j.JID, &stored); err != nil {
		t.Fatalf("get finalized job: %v", err)
	}
	if stored.Status != StatusFailed {
		t.Errorf("Status = %q, want %q (all returned, one failed)", stored.Status, StatusFailed)
	}
	if stored.ReturnCount != 2 {
		t.Errorf("ReturnCount = %d, want 2", stored.ReturnCount)
	}
	if stored.SuccessCount != 1 {
		t.Errorf("SuccessCount = %d, want 1", stored.SuccessCount)
	}

	// No aggregated value under the bare JID; per-peel keys exist.
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	var agg []Return
	if err := bus.KVGet(ctx, returnsBucket, j.JID, &agg); err == nil {
		t.Error("finalize must not write the aggregated returns key")
	}
	for _, peelID := range []string{"peel-01", "peel-02"} {
		var ret Return
		if err := bus.KVGet(ctx, returnsBucket, j.JID+"."+peelID, &ret); err != nil {
			t.Errorf("per-peel key %s missing after finalize: %v", peelID, err)
		}
	}
}

// TestGetReturnsPerPeelOnly verifies the read path: per-peel keys
// (prefix-listed) are the only storage format. A stray value under the
// bare JID (nothing writes one) is never surfaced, and an empty prefix
// result means the job simply has no returns.
func TestGetReturnsPerPeelOnly(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()
	mgr := NewManager(ps, js, "test-master", nil)

	jid := "per-peel-only-job"
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}

	// A stray bare-JID value must be invisible to GetReturns.
	stray := []Return{
		{JID: jid, PeelID: "peel-01", Success: true},
		{JID: jid, PeelID: "peel-02", Success: false, Error: "boom"},
	}
	if _, err := bus.KVPut(ctx, returnsBucket, jid, stray); err != nil {
		t.Fatalf("write stray bare-JID value: %v", err)
	}

	returns, err := mgr.GetReturns(ctx, jid)
	if err != nil {
		t.Fatalf("GetReturns: %v", err)
	}
	if len(returns) != 0 {
		t.Fatalf("no per-peel keys means no returns: got %+v", returns)
	}

	perPeel := Return{JID: jid, PeelID: "peel-09", Success: true}
	if _, err := bus.KVPut(ctx, returnsBucket, jid+".peel-09", perPeel); err != nil {
		t.Fatalf("write per-peel key: %v", err)
	}
	returns, err = mgr.GetReturns(ctx, jid)
	if err != nil {
		t.Fatalf("GetReturns: %v", err)
	}
	if len(returns) != 1 || returns[0].PeelID != "peel-09" {
		t.Fatalf("per-peel keys are the only read path: got %+v", returns)
	}
}

// TestRecoverPerPeelReturnsPrefixScoped verifies that per-peel recovery is
// scoped to one job's keys — other jobs' per-peel keys and bare aggregate
// keys must not leak in — and that multi-token (dotted) peel IDs are still
// matched (the prefix filter uses the ">" wildcard, not "*").
func TestRecoverPerPeelReturnsPrefixScoped(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()
	mgr := NewManager(ps, js, "test-master", nil)

	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}

	put := func(key string, ret Return) {
		t.Helper()
		if _, err := bus.KVPut(ctx, returnsBucket, key, ret); err != nil {
			t.Fatalf("write %s: %v", key, err)
		}
	}
	put("jid-mine.peel-a", Return{JID: "jid-mine", PeelID: "peel-a", Success: true})
	put("jid-mine.web-01.example.com", Return{JID: "jid-mine", PeelID: "web-01.example.com", Success: true})
	put("jid-other.peel-a", Return{JID: "jid-other", PeelID: "peel-a", Success: true})
	// A bare aggregate key for a third job must never be decoded as a
	// per-peel Return of ours.
	if _, err := bus.KVPut(ctx, returnsBucket, "jid-third", []Return{{JID: "jid-third", PeelID: "x"}}); err != nil {
		t.Fatal(err)
	}

	recovered := mgr.recoverPerPeelReturns(ctx, "jid-mine")
	if len(recovered) != 2 {
		t.Fatalf("recovered: got %d, want 2 (incl. dotted peel ID)", len(recovered))
	}
	peels := map[string]bool{}
	for _, r := range recovered {
		peels[r.PeelID] = true
	}
	if !peels["peel-a"] || !peels["web-01.example.com"] {
		t.Errorf("recovered peels = %v, want peel-a and web-01.example.com", peels)
	}
}

// TestWatcherWriterFlushesAllReturnsUnderLoad hammers the watcher with
// concurrent returns and verifies the persist writer goroutine (plus its
// pre-finalize drain) lands every per-peel key before the terminal status.
// Run with -race: the callback, writer, and finalize all touch the queue.
func TestWatcherWriterFlushesAllReturnsUnderLoad(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	const n = 300
	targets := make([]string, n)
	for i := range targets {
		targets[i] = fmt.Sprintf("peel-%03d", i)
	}

	j := NewJob("test.fn", nil, targets, 30*time.Second)
	j.Status = StatusRunning
	j.Owner = "master-load"
	storeJob(t, js, j)

	w := NewWatcher(j, ps, js, nil)
	go w.Watch(ctx)

	time.Sleep(50 * time.Millisecond)

	var wg sync.WaitGroup
	const workers = 8
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := g; i < n; i += workers {
				ret := Return{JID: j.JID, PeelID: targets[i], Success: true, Timestamp: time.Now().UTC()}
				data, _ := bus.Encode(ret)
				ps.Publish(bus.JobReturnSubject(j.JID, targets[i]), data)
			}
		}(g)
	}
	wg.Wait()

	select {
	case <-w.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("watcher did not finish in time")
	}

	if j.Status != StatusComplete {
		t.Fatalf("Status = %q, want %q", j.Status, StatusComplete)
	}
	if j.ReturnCount != n || j.SuccessCount != n {
		t.Errorf("counts = %d/%d, want %d/%d", j.ReturnCount, j.SuccessCount, n, n)
	}

	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := bus.ListKeysWithPrefix(ctx, returnsBucket, j.JID)
	if err != nil {
		t.Fatalf("list per-peel keys: %v", err)
	}
	if len(keys) != n {
		t.Errorf("per-peel keys after finalize: got %d, want %d", len(keys), n)
	}
}
