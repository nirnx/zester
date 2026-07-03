package job

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestDispatchConflictIsErrJIDConflict pins the typed-sentinel contract
// (reactor amendment 8): a second Dispatch of an already-claimed JID with
// DIFFERENT intent must fail with an error that matches ErrJIDConflict via
// errors.Is, so the reactor can classify conflicts on its exclusive "rxn-"
// keyspace as duplicate-suppressed without string matching.
func TestDispatchConflictIsErrJIDConflict(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	mgrA := NewManager(ps, js, "master-a", nil)
	mgrB := NewManager(ps, js, "master-b", nil)

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"web-01"}, 5*time.Second)
	jid := j.JID
	if err := mgrA.Dispatch(ctx, j); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(c *Job)
		differs string
	}{
		{
			name:    "different args",
			mutate:  func(c *Job) { c.Args = map[string]any{"cmd": "hostname"} },
			differs: "args",
		},
		{
			name:    "different function",
			mutate:  func(c *Job) { c.Function = "test.ping" },
			differs: "function",
		},
		{
			name:    "different targets",
			mutate:  func(c *Job) { c.Targets = []string{"web-01", "web-02"} },
			differs: "targets",
		},
		{
			name:    "different metadata",
			mutate:  func(c *Job) { c.Metadata = map[string]string{"source": "reactor"} },
			differs: "metadata",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conflict := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"web-01"}, 5*time.Second)
			conflict.JID = jid
			tt.mutate(conflict)

			err := mgrB.Dispatch(ctx, conflict)
			if err == nil {
				t.Fatal("expected conflict error for same JID with different intent")
			}
			if !errors.Is(err, ErrJIDConflict) {
				t.Errorf("errors.Is(err, ErrJIDConflict) = false, want true; err = %v", err)
			}
			// The human-readable message keeps its original content.
			if !strings.Contains(err.Error(), "conflict: existing job differs ("+tt.differs+")") {
				t.Errorf("conflict message lost its detail: %v", err)
			}
		})
	}

	// A same-intent retry stays idempotent: no error, no sentinel.
	retry := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"web-01"}, 5*time.Second)
	retry.JID = jid
	if err := mgrB.Dispatch(ctx, retry); err != nil {
		t.Fatalf("same-intent retry must not conflict: %v", err)
	}
}

// TestReactorDepthFromMetadata covers the Metadata["reactor_depth"] →
// ExecRequest.ReactorDepth extraction (reactor amendment 21): a base-10
// int string forwards; absent, malformed, or negative values degrade to 0.
func TestReactorDepthFromMetadata(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]string
		want int
	}{
		{name: "nil metadata", meta: nil, want: 0},
		{name: "absent key", meta: map[string]string{"source": "reactor"}, want: 0},
		{name: "valid depth", meta: map[string]string{MetadataReactorDepth: "3"}, want: 3},
		{name: "depth one", meta: map[string]string{MetadataReactorDepth: "1"}, want: 1},
		{name: "zero", meta: map[string]string{MetadataReactorDepth: "0"}, want: 0},
		{name: "non-integer", meta: map[string]string{MetadataReactorDepth: "abc"}, want: 0},
		{name: "empty string", meta: map[string]string{MetadataReactorDepth: ""}, want: 0},
		{name: "negative", meta: map[string]string{MetadataReactorDepth: "-2"}, want: 0},
		{name: "float", meta: map[string]string{MetadataReactorDepth: "1.5"}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := NewJob("test.fn", nil, []string{"peel-01"}, time.Second)
			j.Metadata = tt.meta
			req := execRequestForJob(j)
			if req.ReactorDepth != tt.want {
				t.Errorf("ReactorDepth = %d, want %d", req.ReactorDepth, tt.want)
			}
			// Identity fields must always carry over unchanged.
			if req.JID != j.JID || req.Module != j.Function {
				t.Errorf("request identity mismatch: %+v vs job %+v", req, j)
			}
		})
	}
}

// TestDispatchForwardsReactorDepth verifies the wiring end to end: a job
// carrying reactor provenance metadata publishes ExecRequests stamped with
// ReactorDepth, on the initial dispatch AND on the silent-target re-send
// (both go through execRequestForJob).
func TestDispatchForwardsReactorDepth(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	counter := newCmdCounter(t, ps)

	mgr := NewManager(ps, js, "master-depth", nil)
	mgr.AckWindow = 100 * time.Millisecond

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-rx"}, 5*time.Second)
	j.Metadata = map[string]string{
		"source":             "reactor",
		MetadataReactorDepth: "2",
	}
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if req := counter.lastReq("peel-rx"); req.ReactorDepth != 2 {
		t.Errorf("dispatched ReactorDepth = %d, want 2", req.ReactorDepth)
	}

	// Stay silent past the ack window: the re-sent request must carry the
	// same depth.
	deadline := time.Now().Add(2 * time.Second)
	for counter.count("peel-rx") < 2 {
		if time.Now().After(deadline) {
			t.Fatal("silent-target re-send did not fire")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if req := counter.lastReq("peel-rx"); req.ReactorDepth != 2 {
		t.Errorf("re-sent ReactorDepth = %d, want 2", req.ReactorDepth)
	}

	mgr.Cancel(ctx, j.JID)
	waitForNoActiveJobs(t, mgr)
}

// TestDispatchWithoutReactorMetadataOmitsDepth verifies ordinary operator
// jobs keep ReactorDepth zero on the wire.
func TestDispatchWithoutReactorMetadataOmitsDepth(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	counter := newCmdCounter(t, ps)

	mgr := NewManager(ps, js, "master-plain", nil)

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-plain"}, 5*time.Second)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if req := counter.lastReq("peel-plain"); req.ReactorDepth != 0 {
		t.Errorf("ReactorDepth = %d, want 0 for a non-reactor job", req.ReactorDepth)
	}

	mgr.Cancel(ctx, j.JID)
	waitForNoActiveJobs(t, mgr)
}

// TestReclaimClaimedForwardsReactorDepth verifies the claimed-job reclaim
// re-dispatch also stamps ReactorDepth: a reactor job whose master died
// between claim and publish must not lose its chain depth on failover.
func TestReclaimClaimedForwardsReactorDepth(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	counter := newCmdCounter(t, ps)

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-reclaim"}, 5*time.Second)
	j.Status = StatusClaimed
	j.Owner = "master-dead"
	j.Metadata = map[string]string{MetadataReactorDepth: "1"}
	storeJob(t, js, j)

	mgr := NewManager(ps, js, "master-survivor", nil)
	mgr.ReclaimJob(ctx, j)

	if got := counter.count("peel-reclaim"); got != 1 {
		t.Fatalf("reclaim publishes = %d, want 1", got)
	}
	if req := counter.lastReq("peel-reclaim"); req.ReactorDepth != 1 {
		t.Errorf("reclaimed ReactorDepth = %d, want 1", req.ReactorDepth)
	}

	mgr.Cancel(ctx, j.JID)
	waitForNoActiveJobs(t, mgr)
}
