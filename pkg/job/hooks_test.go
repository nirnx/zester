package job

// Tests for the optional metric hooks: Manager/Watcher.OnJobFinalized and
// OrphanScanner.OnReclaim. Nil hooks are additionally exercised by every
// other test in this package (they all run with hooks unset).

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
)

type finalizeRecord struct {
	function string
	status   string
	duration time.Duration
}

// finalizeRecorder is a concurrency-safe OnJobFinalized recorder.
type finalizeRecorder struct {
	mu    sync.Mutex
	calls []finalizeRecord
}

func (r *finalizeRecorder) hook(function, status string, duration time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, finalizeRecord{function, status, duration})
}

func (r *finalizeRecorder) snapshot() []finalizeRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]finalizeRecord, len(r.calls))
	copy(out, r.calls)
	return out
}

func publishReturn(t *testing.T, ps bus.PubSub, jid, peelID string, success bool) {
	t.Helper()
	ret := Return{
		JID:       jid,
		PeelID:    peelID,
		Success:   success,
		Timestamp: time.Now().UTC(),
	}
	data, err := bus.Encode(ret)
	if err != nil {
		t.Fatalf("encode return: %v", err)
	}
	if err := ps.Publish(bus.JobReturnSubject(jid, peelID), data); err != nil {
		t.Fatalf("publish return: %v", err)
	}
}

func TestWatcherOnJobFinalizedFiresComplete(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-01"}, 10*time.Second)
	j.Status = StatusRunning
	storeJob(t, js, j)

	rec := &finalizeRecorder{}
	w := NewWatcher(j, ps, js, nil)
	w.OnJobFinalized = rec.hook
	go w.Watch(ctx)

	time.Sleep(50 * time.Millisecond)
	publishReturn(t, ps, j.JID, "peel-01", true)

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish in time")
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("hook calls = %d, want 1", len(calls))
	}
	if calls[0].function != "test.fn" {
		t.Errorf("hook function = %q, want %q", calls[0].function, "test.fn")
	}
	if calls[0].status != string(StatusComplete) {
		t.Errorf("hook status = %q, want %q", calls[0].status, StatusComplete)
	}
	if calls[0].duration <= 0 {
		t.Errorf("hook duration = %v, want > 0", calls[0].duration)
	}
}

func TestWatcherOnJobFinalizedFiresFailed(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-01"}, 10*time.Second)
	j.Status = StatusRunning
	storeJob(t, js, j)

	rec := &finalizeRecorder{}
	w := NewWatcher(j, ps, js, nil)
	w.OnJobFinalized = rec.hook
	go w.Watch(ctx)

	time.Sleep(50 * time.Millisecond)
	publishReturn(t, ps, j.JID, "peel-01", false)

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish in time")
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("hook calls = %d, want 1", len(calls))
	}
	if calls[0].status != string(StatusFailed) {
		t.Errorf("hook status = %q, want %q", calls[0].status, StatusFailed)
	}
}

// TestWatcherOnJobFinalizedNotFiredOnDetach: a shutdown detach with
// outstanding targets persists returns but does NOT finalize, so the hook
// must not fire (the surviving master finalizes and counts the job).
func TestWatcherOnJobFinalizedNotFiredOnDetach(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-01", "peel-02"}, 10*time.Second)
	j.Status = StatusRunning
	storeJob(t, js, j)

	rec := &finalizeRecorder{}
	w := NewWatcher(j, ps, js, nil)
	w.OnJobFinalized = rec.hook
	go w.Watch(ctx)

	time.Sleep(50 * time.Millisecond)
	publishReturn(t, ps, j.JID, "peel-01", true) // 1 of 2

	w.Detach()
	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish in time")
	}

	if calls := rec.snapshot(); len(calls) != 0 {
		t.Fatalf("hook calls = %d, want 0 on detach", len(calls))
	}
}

func TestWatcherNilOnJobFinalizedIsSafe(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-01"}, 10*time.Second)
	j.Status = StatusRunning
	storeJob(t, js, j)

	w := NewWatcher(j, ps, js, nil) // OnJobFinalized left nil
	go w.Watch(ctx)

	time.Sleep(50 * time.Millisecond)
	publishReturn(t, ps, j.JID, "peel-01", true)

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish in time")
	}
	if j.Status != StatusComplete {
		t.Errorf("Status = %q, want %q", j.Status, StatusComplete)
	}
}

// TestManagerThreadsOnJobFinalizedToWatchers verifies the Manager copies its
// hook onto watchers it starts via Dispatch.
func TestManagerThreadsOnJobFinalizedToWatchers(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	rec := &finalizeRecorder{}
	mgr := NewManager(ps, js, "master-hooks", nil)
	mgr.OnJobFinalized = rec.hook

	j := NewJob("test.fn", nil, []string{"peel-01"}, 5*time.Second)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	publishReturn(t, ps, j.JID, "peel-01", true)
	waitForNoActiveJobs(t, mgr)

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("hook calls = %d, want 1", len(calls))
	}
	if calls[0].function != "test.fn" || calls[0].status != string(StatusComplete) {
		t.Errorf("hook call = %+v, want function test.fn / status complete", calls[0])
	}
}

// TestOrphanScannerOnReclaimFires verifies the OnReclaim hook fires once per
// successfully CAS-reclaimed job, and that a nil hook is safe (covered by
// TestOrphanScannerReclaimsDeadMastersJob which runs with OnReclaim unset).
func TestOrphanScannerOnReclaimFires(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	ttl := 300 * time.Millisecond
	shortHeartbeatTTL(t, js, ttl)

	hbBucket, err := bus.GetBucket(ctx, js, bus.BucketMasterHeartbeat)
	if err != nil {
		t.Fatalf("get heartbeat bucket: %v", err)
	}
	deadMaster := "master-dead"
	if _, err := bus.KVPut(ctx, hbBucket, deadMaster, MasterHeartbeat{
		MasterID: deadMaster, Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("dead master heartbeat: %v", err)
	}

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-01"}, 30*time.Second)
	j.Status = StatusRunning
	j.Owner = deadMaster
	storeJob(t, js, j)

	// Let the dead master's heartbeat expire; keep the scanner alive.
	time.Sleep(ttl + 100*time.Millisecond)
	if _, err := bus.KVPut(ctx, hbBucket, "master-live", MasterHeartbeat{
		MasterID: "master-live", Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("live master heartbeat: %v", err)
	}

	reclaims := 0
	s := NewOrphanScanner("master-live", js, nil, nil)
	s.MissThreshold = 1
	s.OnReclaim = func() { reclaims++ }
	s.scan(ctx)

	if reclaims != 1 {
		t.Errorf("OnReclaim fired %d times, want 1", reclaims)
	}

	// Second scan: job now owned by master-live (us) — no further reclaim.
	s.scan(ctx)
	if reclaims != 1 {
		t.Errorf("OnReclaim fired %d times after second scan, want still 1", reclaims)
	}
}
