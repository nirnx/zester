package peeld

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// noSave is a debounce delay long enough that tests control persistence
// explicitly via Flush.
const noSave = time.Hour

func TestDedupTrackerSemantics(t *testing.T) {
	d := newDedupTracker("", 16, noSave, discardLogger())

	// First observation of a JID is accepted.
	if _, rejected := d.Observe("j1", 5); rejected {
		t.Fatal("first observation rejected")
	}
	// Same epoch again = duplicate delivery → rejected.
	if prev, rejected := d.Observe("j1", 5); !rejected || prev != 5 {
		t.Fatalf("duplicate: rejected=%v prev=%d, want true/5", rejected, prev)
	}
	// Lower epoch = stale dispatch → rejected, previous epoch reported.
	if prev, rejected := d.Observe("j1", 4); !rejected || prev != 5 {
		t.Fatalf("stale: rejected=%v prev=%d, want true/5", rejected, prev)
	}
	// Higher epoch (reclaimed job, newer master) → accepted.
	if _, rejected := d.Observe("j1", 6); rejected {
		t.Fatal("higher epoch rejected")
	}
	// And the new epoch becomes the fence.
	if _, rejected := d.Observe("j1", 6); !rejected {
		t.Fatal("duplicate at new epoch not rejected")
	}
}

func TestDedupTrackerPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dedup.msgpack")

	d := newDedupTracker(path, 16, noSave, discardLogger())
	d.Observe("j1", 3)
	d.Observe("j2", 7)
	if err := d.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}

	// Simulate a peel restart: a fresh tracker loads the persisted state and
	// still rejects the re-delivered dispatches.
	d2 := newDedupTracker(path, 16, noSave, discardLogger())
	if err := d2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, rejected := d2.Observe("j1", 3); !rejected {
		t.Error("re-delivered j1@3 not rejected after restart")
	}
	if _, rejected := d2.Observe("j2", 6); !rejected {
		t.Error("stale j2@6 not rejected after restart")
	}
	if _, rejected := d2.Observe("j2", 8); rejected {
		t.Error("newer epoch j2@8 rejected after restart")
	}
	if _, rejected := d2.Observe("j3", 1); rejected {
		t.Error("unseen j3 rejected after restart")
	}
}

// TestDedupTrackerObserveDurableCrashSafe simulates the C0 crash window: the
// peel accepts a job dispatch and crashes BEFORE the debounced save would
// fire. ObserveDurable must have made the record durable synchronously, so a
// fresh tracker loaded from disk (the restarted peel) rejects the master's
// same-epoch ack-window redispatch instead of re-executing the job.
func TestDedupTrackerObserveDurableCrashSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dedup.msgpack")
	d := newDedupTracker(path, 16, noSave, discardLogger()) // debounce never fires in test time

	if _, rejected := d.ObserveDurable("j1", 2); rejected {
		t.Fatal("first observation rejected")
	}

	// Crash simulation: no Flush, no debounce wait — reload straight from
	// disk as a restarted peel would.
	d2 := newDedupTracker(path, 16, noSave, discardLogger())
	if err := d2.Load(); err != nil {
		t.Fatalf("load after simulated crash: %v", err)
	}
	if _, rejected := d2.Observe("j1", 2); !rejected {
		t.Error("record lost across simulated crash: same-epoch redispatch would re-execute the job")
	}

	// Rejected re-deliveries keep the Observe fencing semantics.
	if prev, rejected := d.ObserveDurable("j1", 2); !rejected || prev != 2 {
		t.Fatalf("duplicate: rejected=%v prev=%d, want true/2", rejected, prev)
	}
	if prev, rejected := d.ObserveDurable("j1", 1); !rejected || prev != 2 {
		t.Fatalf("stale: rejected=%v prev=%d, want true/2", rejected, prev)
	}

	// A higher epoch (reclaimed job) is accepted and immediately durable too.
	if _, rejected := d.ObserveDurable("j1", 3); rejected {
		t.Fatal("higher epoch rejected")
	}
	d3 := newDedupTracker(path, 16, noSave, discardLogger())
	if err := d3.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, rejected := d3.Observe("j1", 3); !rejected {
		t.Error("higher-epoch record not durable across simulated crash")
	}
}

// TestDedupTrackerObserveDurableNoPersistence pins that a persistence-less
// tracker (path == "", tests) accepts and rejects identically — the
// synchronous flush is a no-op, never an error path.
func TestDedupTrackerObserveDurableNoPersistence(t *testing.T) {
	d := newDedupTracker("", 16, noSave, discardLogger())
	if _, rejected := d.ObserveDurable("j1", 1); rejected {
		t.Fatal("first observation rejected")
	}
	if _, rejected := d.ObserveDurable("j1", 1); !rejected {
		t.Fatal("duplicate not rejected")
	}
}

func TestDedupTrackerLoadMissingFile(t *testing.T) {
	d := newDedupTracker(filepath.Join(t.TempDir(), "nope.msgpack"), 16, noSave, discardLogger())
	if err := d.Load(); err != nil {
		t.Fatalf("load of missing file should be nil, got %v", err)
	}
}

func TestDedupTrackerEviction(t *testing.T) {
	d := newDedupTracker("", 3, noSave, discardLogger())

	d.Observe("a", 1)
	d.Observe("b", 1)
	d.Observe("c", 1)
	if d.Len() != 3 {
		t.Fatalf("len = %d, want 3", d.Len())
	}

	// Updating an existing JID must not evict anyone.
	d.Observe("a", 2)
	if d.Len() != 3 {
		t.Fatalf("len after update = %d, want 3", d.Len())
	}
	if _, rejected := d.Observe("b", 1); !rejected {
		t.Error("b evicted by an update of a")
	}

	// A fourth JID evicts the oldest ("a").
	d.Observe("d", 1)
	if d.Len() != 3 {
		t.Fatalf("len after eviction = %d, want 3", d.Len())
	}
	if _, rejected := d.Observe("a", 1); rejected {
		t.Error("a still tracked after eviction — duplicate should have been accepted")
	}
	// "a" was re-inserted by the check above, evicting "b".
	if _, rejected := d.Observe("c", 1); !rejected {
		t.Error("c should still be tracked")
	}
}

func TestDedupTrackerCapacityBound(t *testing.T) {
	d := newDedupTracker("", 100, noSave, discardLogger())
	for i := 0; i < 1000; i++ {
		d.Observe(fmt.Sprintf("j%d", i), 1)
	}
	if d.Len() != 100 {
		t.Fatalf("len = %d, want capped at 100", d.Len())
	}
}

func TestDedupTrackerDebouncedSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dedup.msgpack")
	d := newDedupTracker(path, 16, 10*time.Millisecond, discardLogger())
	d.Observe("j1", 1)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("debounced save never wrote the file")
		}
		time.Sleep(5 * time.Millisecond)
	}

	d2 := newDedupTracker(path, 16, noSave, discardLogger())
	if err := d2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, rejected := d2.Observe("j1", 1); !rejected {
		t.Error("debounced save did not persist j1")
	}
}
