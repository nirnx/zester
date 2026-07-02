package update

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/bus/bustest"
)

// setupRolloutTestFull is setupRolloutTest plus the update-status KV bucket,
// for tests exercising degraded exclusion and min-protocol checks.
func setupRolloutTestFull(t *testing.T) (*RolloutStore, *ManifestStore, bus.KV) {
	t.Helper()
	ctx := context.Background()
	js := bustest.NewFakeJS()
	bus.InitializeStorage(ctx, js)

	rolloutKV, err := bus.GetBucket(ctx, js, bus.BucketUpdateRollouts)
	if err != nil {
		t.Fatal(err)
	}
	manifestKV, err := bus.GetBucket(ctx, js, bus.BucketUpdateManifests)
	if err != nil {
		t.Fatal(err)
	}
	statusKV, err := bus.GetBucket(ctx, js, bus.BucketUpdateStatus)
	if err != nil {
		t.Fatal(err)
	}
	return NewRolloutStore(rolloutKV), NewManifestStore(manifestKV), statusKV
}

// recordingRequestFunc returns success for every command and records the
// subjects it was invoked with.
func recordingRequestFunc() (RequestFunc, func() []string) {
	var mu sync.Mutex
	var subjects []string
	fn := func(ctx context.Context, subject string, req *UpdateCommand) (*UpdateResponse, error) {
		mu.Lock()
		subjects = append(subjects, subject)
		mu.Unlock()
		switch req.Command {
		case CmdPrepare:
			return &UpdateResponse{Status: "staged"}, nil
		case CmdApply:
			return &UpdateResponse{Status: "applying"}, nil
		case CmdConfirm:
			return &UpdateResponse{Status: "confirmed"}, nil
		}
		return &UpdateResponse{Status: "error", Error: "unexpected command"}, nil
	}
	get := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(subjects))
		copy(out, subjects)
		return out
	}
	return fn, get
}

func publishTestManifest(t *testing.T, ms *ManifestStore, minProtocol int) {
	t.Helper()
	if err := ms.Publish(context.Background(), &Manifest{
		Version:     "v1.0.0",
		Component:   "peel",
		GOOS:        "linux",
		GOARCH:      "amd64",
		SHA256:      "abc123",
		ObjectKey:   "peel/linux/amd64/v1.0.0",
		MinProtocol: minProtocol,
	}); err != nil {
		t.Fatalf("Publish manifest failed: %v", err)
	}
}

func waitForRolloutState(t *testing.T, store *RolloutStore, id string, want string) *RolloutState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last *RolloutState
	for time.Now().Before(deadline) {
		got, err := store.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("Get rollout failed: %v", err)
		}
		last = got
		if got.State == want {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last != nil {
		t.Fatalf("rollout %s did not reach state %q within 5s (last state %q)", id, want, last.State)
	}
	t.Fatalf("rollout %s did not reach state %q within 5s", id, want)
	return nil
}

// TestRolloutController_ResumeOrphaned_AdoptsStale verifies that a
// non-terminal rollout with a stale driver heartbeat is CAS-adopted and
// resumed from its persisted batch index: only the remaining batches get
// commands, and the rollout runs to completion.
func TestRolloutController_ResumeOrphaned_AdoptsStale(t *testing.T) {
	rolloutStore, manifestStore, _ := setupRolloutTest(t)
	ctx := context.Background()
	publishTestManifest(t, manifestStore, 0)

	orphan := &RolloutState{
		ID:    "rol-orphan-1",
		State: RolloutRolling,
		Config: RolloutConfig{
			Version:    "v1.0.0",
			Component:  "peel",
			BatchSize:  1,
			SoakTime:   50 * time.Millisecond,
			BatchPause: 10 * time.Millisecond,
			MaxFailed:  1,
		},
		Batches:      [][]string{{"node-1"}, {"node-2"}},
		CurrentBatch: 1, // batch 0 already done by the dead driver
		NodeResults: map[string]*NodeResult{
			"node-1": {ID: "node-1", Status: "confirmed"},
		},
		StartedAt:       time.Now().Add(-time.Hour),
		DriverID:        "drv-dead-master",
		DriverHeartbeat: time.Now().Add(-5 * time.Minute), // stale
	}
	if err := rolloutStore.Save(ctx, orphan); err != nil {
		t.Fatalf("Save orphan failed: %v", err)
	}

	reqFn, subjects := recordingRequestFunc()
	ctrl := NewRolloutController(context.Background(), reqFn, rolloutStore, manifestStore, nil, nil)
	ctrl.DriverID = "drv-new-master"

	n, err := ctrl.ResumeOrphaned(ctx)
	if err != nil {
		t.Fatalf("ResumeOrphaned failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("adopted count: got %d, want 1", n)
	}

	final := waitForRolloutState(t, rolloutStore, orphan.ID, RolloutCompleted)

	if final.DriverID != "drv-new-master" {
		t.Errorf("DriverID: got %q, want %q", final.DriverID, "drv-new-master")
	}
	if final.CurrentBatch != 1 {
		t.Errorf("CurrentBatch: got %d, want 1", final.CurrentBatch)
	}
	if nr := final.NodeResults["node-2"]; nr == nil || nr.Status != "confirmed" {
		t.Errorf("node-2 result: got %+v, want confirmed", nr)
	}
	// Batch 0 was already done — node-1 must not have been re-driven.
	if nr := final.NodeResults["node-1"]; nr == nil || nr.Status != "confirmed" {
		t.Errorf("node-1 result: got %+v, want untouched confirmed", nr)
	}
	node1Subject := bus.UpdateCmdSubject("node-1")
	for _, s := range subjects() {
		if s == node1Subject {
			t.Errorf("resume re-drove already-completed batch 0 node: sent command to %s", s)
		}
	}
	if len(subjects()) == 0 {
		t.Error("expected commands sent to node-2, got none")
	}
}

// TestRolloutController_ResumeOrphaned_SkipsFreshTerminalDryRun verifies that
// rollouts with a fresh driver heartbeat, terminal rollouts, and dry-run
// records are all left alone.
func TestRolloutController_ResumeOrphaned_SkipsFreshTerminalDryRun(t *testing.T) {
	rolloutStore, manifestStore, _ := setupRolloutTest(t)
	ctx := context.Background()
	publishTestManifest(t, manifestStore, 0)

	stale := time.Now().Add(-5 * time.Minute)
	records := []*RolloutState{
		{
			ID: "rol-fresh", State: RolloutRolling,
			Config:  RolloutConfig{Version: "v1.0.0", Component: "peel"},
			Batches: [][]string{{"node-1"}}, NodeResults: map[string]*NodeResult{},
			DriverID: "drv-alive", DriverHeartbeat: time.Now(), // fresh
		},
		{
			ID: "rol-done", State: RolloutCompleted,
			Config:  RolloutConfig{Version: "v1.0.0", Component: "peel"},
			Batches: [][]string{{"node-2"}}, NodeResults: map[string]*NodeResult{},
			DriverID: "drv-dead", DriverHeartbeat: stale,
		},
		{
			ID: "rol-aborted", State: RolloutAborted,
			Config:  RolloutConfig{Version: "v1.0.0", Component: "peel"},
			Batches: [][]string{{"node-3"}}, NodeResults: map[string]*NodeResult{},
			DriverID: "drv-dead", DriverHeartbeat: stale,
		},
		{
			ID: "rol-dry", State: RolloutCreated,
			Config:  RolloutConfig{Version: "v1.0.0", Component: "peel", DryRun: true},
			Batches: [][]string{{"node-4"}}, NodeResults: map[string]*NodeResult{},
			DriverID: "drv-dead", DriverHeartbeat: stale,
		},
	}
	for _, st := range records {
		if err := rolloutStore.Save(ctx, st); err != nil {
			t.Fatalf("Save %s failed: %v", st.ID, err)
		}
	}

	reqFn, subjects := recordingRequestFunc()
	ctrl := NewRolloutController(context.Background(), reqFn, rolloutStore, manifestStore, nil, nil)

	n, err := ctrl.ResumeOrphaned(ctx)
	if err != nil {
		t.Fatalf("ResumeOrphaned failed: %v", err)
	}
	if n != 0 {
		t.Errorf("adopted count: got %d, want 0", n)
	}
	if got := subjects(); len(got) != 0 {
		t.Errorf("expected no commands, got %v", got)
	}

	// The fresh rollout must still belong to its original driver.
	fresh, err := rolloutStore.Get(ctx, "rol-fresh")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.DriverID != "drv-alive" {
		t.Errorf("fresh rollout DriverID: got %q, want %q", fresh.DriverID, "drv-alive")
	}
}

// TestRolloutController_AbortRollout_NotActive_WritesKV verifies the
// multi-master abort fix: an abort landing on a controller that is not
// driving the rollout CAS-writes RolloutAborted straight to KV.
func TestRolloutController_AbortRollout_NotActive_WritesKV(t *testing.T) {
	rolloutStore, manifestStore, _ := setupRolloutTest(t)
	ctx := context.Background()

	st := &RolloutState{
		ID:    "rol-remote-1",
		State: RolloutRolling,
		Config: RolloutConfig{
			Version: "v1.0.0", Component: "peel",
		},
		Batches:         [][]string{{"node-1"}},
		NodeResults:     map[string]*NodeResult{},
		DriverID:        "drv-other-master",
		DriverHeartbeat: time.Now(),
	}
	if err := rolloutStore.Save(ctx, st); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	reqFn := mockRequestFunc(nil)
	ctrl := NewRolloutController(context.Background(), reqFn, rolloutStore, manifestStore, nil, nil)

	if err := ctrl.AbortRollout(ctx, st.ID); err != nil {
		t.Fatalf("AbortRollout (not active) failed: %v", err)
	}

	got, err := rolloutStore.Get(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != RolloutAborted {
		t.Errorf("state: got %q, want %q", got.State, RolloutAborted)
	}
	if got.FinishedAt.IsZero() {
		t.Error("FinishedAt: got zero, want set")
	}

	// Repeat abort is idempotent.
	if err := ctrl.AbortRollout(ctx, st.ID); err != nil {
		t.Errorf("repeat AbortRollout: got %v, want nil", err)
	}
}

// TestRolloutController_AbortRollout_Completed verifies aborting a completed
// rollout returns an error and does not rewrite the record.
func TestRolloutController_AbortRollout_Completed(t *testing.T) {
	rolloutStore, manifestStore, _ := setupRolloutTest(t)
	ctx := context.Background()

	st := &RolloutState{
		ID:          "rol-done-1",
		State:       RolloutCompleted,
		Config:      RolloutConfig{Version: "v1.0.0", Component: "peel"},
		NodeResults: map[string]*NodeResult{},
	}
	if err := rolloutStore.Save(ctx, st); err != nil {
		t.Fatal(err)
	}

	ctrl := NewRolloutController(context.Background(), mockRequestFunc(nil), rolloutStore, manifestStore, nil, nil)
	if err := ctrl.AbortRollout(ctx, st.ID); err == nil {
		t.Fatal("expected error aborting completed rollout, got nil")
	}

	got, err := rolloutStore.Get(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != RolloutCompleted {
		t.Errorf("state: got %q, want %q (unchanged)", got.State, RolloutCompleted)
	}
}

// flakyKV wraps a bus.KV and fails every write after the first
// failAfter successful ones.
type flakyKV struct {
	bus.KV
	mu        sync.Mutex
	writes    int
	failAfter int
}

func (f *flakyKV) allow() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	return f.writes <= f.failAfter
}

func (f *flakyKV) Create(ctx context.Context, key string, value []byte) (uint64, error) {
	if !f.allow() {
		return 0, fmt.Errorf("injected create failure")
	}
	return f.KV.Create(ctx, key, value)
}

func (f *flakyKV) Update(ctx context.Context, key string, value []byte, revision uint64) (uint64, error) {
	if !f.allow() {
		return 0, fmt.Errorf("injected update failure")
	}
	return f.KV.Update(ctx, key, value, revision)
}

// TestRolloutController_SaveFailureAbortsRollout verifies finding 26: when
// rollout state persistence keeps failing, the driver stops instead of
// driving a rollout whose persisted state is untrustworthy.
func TestRolloutController_SaveFailureAbortsRollout(t *testing.T) {
	ctx := context.Background()
	js := bustest.NewFakeJS()
	bus.InitializeStorage(ctx, js)

	rolloutKV, err := bus.GetBucket(ctx, js, bus.BucketUpdateRollouts)
	if err != nil {
		t.Fatal(err)
	}
	manifestKV, err := bus.GetBucket(ctx, js, bus.BucketUpdateManifests)
	if err != nil {
		t.Fatal(err)
	}
	flaky := &flakyKV{KV: rolloutKV, failAfter: 1} // initial Create succeeds, everything after fails
	rolloutStore := NewRolloutStore(flaky)
	manifestStore := NewManifestStore(manifestKV)
	publishTestManifest(t, manifestStore, 0)

	reqFn, subjects := recordingRequestFunc()
	ctrl := NewRolloutController(context.Background(), reqFn, rolloutStore, manifestStore, nil, nil)

	state, err := ctrl.StartRollout(ctx, RolloutConfig{
		Version:    "v1.0.0",
		Component:  "peel",
		BatchSize:  1,
		SoakTime:   50 * time.Millisecond,
		BatchPause: 10 * time.Millisecond,
	}, []string{"node-1"})
	if err != nil {
		t.Fatalf("StartRollout failed: %v", err)
	}

	// The batch-start save fails (with retries), so the driver must stop
	// before sending any command, and the goroutine must exit.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(ctrl.ActiveRollouts()) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := ctrl.ActiveRollouts(); len(got) != 0 {
		t.Fatalf("controller still driving after persistent save failure: %v", got)
	}
	if got := subjects(); len(got) != 0 {
		t.Errorf("expected no node commands after save failure, got %v", got)
	}

	// KV writes were blocked, so the persisted record still holds the
	// initial created state — the point is the driver stopped, loudly.
	got, err := rolloutStore.Get(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != RolloutCreated {
		t.Errorf("persisted state: got %q, want %q (writes were blocked)", got.State, RolloutCreated)
	}
}

// TestRolloutController_ExcludesDegradedNodes verifies that degraded nodes
// are removed from the target set before batching.
func TestRolloutController_ExcludesDegradedNodes(t *testing.T) {
	rolloutStore, manifestStore, statusKV := setupRolloutTestFull(t)
	ctx := context.Background()
	publishTestManifest(t, manifestStore, 0)

	putStatus(t, statusKV, &NodeStatus{ID: "node-1", Component: "peel", GOOS: "linux", GOARCH: "amd64"})
	putStatus(t, statusKV, &NodeStatus{ID: "node-2", Component: "peel", GOOS: "linux", GOARCH: "amd64", Degraded: true})

	ctrl := NewRolloutController(context.Background(), mockRequestFunc(nil), rolloutStore, manifestStore, statusKV, nil)

	state, err := ctrl.StartRollout(ctx, RolloutConfig{
		Version: "v1.0.0", Component: "peel", BatchSize: 10, DryRun: true,
	}, []string{"node-1", "node-2"})
	if err != nil {
		t.Fatalf("StartRollout failed: %v", err)
	}

	var flat []string
	for _, b := range state.Batches {
		flat = append(flat, b...)
	}
	if len(flat) != 1 || flat[0] != "node-1" {
		t.Errorf("batched nodes: got %v, want [node-1] (node-2 is degraded)", flat)
	}
}

// TestRolloutController_AllDegradedFails verifies that a target set reduced
// to nothing by degraded exclusion refuses to start.
func TestRolloutController_AllDegradedFails(t *testing.T) {
	rolloutStore, manifestStore, statusKV := setupRolloutTestFull(t)
	ctx := context.Background()
	publishTestManifest(t, manifestStore, 0)

	putStatus(t, statusKV, &NodeStatus{ID: "node-1", Component: "peel", GOOS: "linux", GOARCH: "amd64", Degraded: true})

	ctrl := NewRolloutController(context.Background(), mockRequestFunc(nil), rolloutStore, manifestStore, statusKV, nil)

	_, err := ctrl.StartRollout(ctx, RolloutConfig{
		Version: "v1.0.0", Component: "peel", DryRun: true,
	}, []string{"node-1"})
	if err == nil {
		t.Fatal("expected error when all target nodes are degraded, got nil")
	}
	if !strings.Contains(err.Error(), "no eligible nodes") {
		t.Errorf("error: got %q, want mention of no eligible nodes", err)
	}
}

// TestRolloutController_MinProtocol covers the finding 36 / B12 semantics:
// a manifest with MinProtocol > 0 refuses nodes below it — including nodes
// reporting 0 (legacy) and nodes without a status record — naming them in
// the error, while MinProtocol 0 never refuses anyone.
func TestRolloutController_MinProtocol(t *testing.T) {
	t.Run("refuses node below min protocol", func(t *testing.T) {
		rolloutStore, manifestStore, statusKV := setupRolloutTestFull(t)
		publishTestManifest(t, manifestStore, 2)
		putStatus(t, statusKV, &NodeStatus{ID: "node-ok", Component: "peel", GOOS: "linux", GOARCH: "amd64", Protocol: 2})
		putStatus(t, statusKV, &NodeStatus{ID: "node-old", Component: "peel", GOOS: "linux", GOARCH: "amd64", Protocol: 1})

		ctrl := NewRolloutController(context.Background(), mockRequestFunc(nil), rolloutStore, manifestStore, statusKV, nil)
		_, err := ctrl.StartRollout(context.Background(), RolloutConfig{
			Version: "v1.0.0", Component: "peel", DryRun: true,
		}, []string{"node-ok", "node-old"})
		if err == nil {
			t.Fatal("expected min-protocol refusal, got nil")
		}
		if !strings.Contains(err.Error(), "node-old") {
			t.Errorf("error must name the incompatible node: %q", err)
		}
		if strings.Contains(err.Error(), "node-ok") {
			t.Errorf("error must not name the compatible node: %q", err)
		}
	})

	t.Run("legacy protocol 0 fails only when min protocol set", func(t *testing.T) {
		rolloutStore, manifestStore, statusKV := setupRolloutTestFull(t)
		publishTestManifest(t, manifestStore, 2)
		putStatus(t, statusKV, &NodeStatus{ID: "node-legacy", Component: "peel", GOOS: "linux", GOARCH: "amd64"}) // Protocol 0

		ctrl := NewRolloutController(context.Background(), mockRequestFunc(nil), rolloutStore, manifestStore, statusKV, nil)
		_, err := ctrl.StartRollout(context.Background(), RolloutConfig{
			Version: "v1.0.0", Component: "peel", DryRun: true,
		}, []string{"node-legacy"})
		if err == nil {
			t.Fatal("expected refusal for legacy protocol 0 when MinProtocol > 0, got nil")
		}
		if !strings.Contains(err.Error(), "node-legacy") {
			t.Errorf("error must name the legacy node: %q", err)
		}
	})

	t.Run("legacy protocol 0 passes when min protocol unset", func(t *testing.T) {
		rolloutStore, manifestStore, statusKV := setupRolloutTestFull(t)
		publishTestManifest(t, manifestStore, 0)
		putStatus(t, statusKV, &NodeStatus{ID: "node-legacy", Component: "peel", GOOS: "linux", GOARCH: "amd64"})

		ctrl := NewRolloutController(context.Background(), mockRequestFunc(nil), rolloutStore, manifestStore, statusKV, nil)
		state, err := ctrl.StartRollout(context.Background(), RolloutConfig{
			Version: "v1.0.0", Component: "peel", DryRun: true,
		}, []string{"node-legacy"})
		if err != nil {
			t.Fatalf("StartRollout failed: %v", err)
		}
		if len(state.Batches) != 1 {
			t.Errorf("batches: got %d, want 1", len(state.Batches))
		}
	})

	t.Run("node without status record fails when min protocol set", func(t *testing.T) {
		rolloutStore, manifestStore, statusKV := setupRolloutTestFull(t)
		publishTestManifest(t, manifestStore, 1)

		ctrl := NewRolloutController(context.Background(), mockRequestFunc(nil), rolloutStore, manifestStore, statusKV, nil)
		_, err := ctrl.StartRollout(context.Background(), RolloutConfig{
			Version: "v1.0.0", Component: "peel", DryRun: true,
		}, []string{"node-unknown"})
		if err == nil {
			t.Fatal("expected refusal for node without status record when MinProtocol > 0, got nil")
		}
		if !strings.Contains(err.Error(), "node-unknown") {
			t.Errorf("error must name the unknown node: %q", err)
		}
	})
}

func putStatus(t *testing.T, kv bus.KV, ns *NodeStatus) {
	t.Helper()
	if _, err := bus.KVPut(context.Background(), kv, StatusKey(ns.Component, ns.ID), ns); err != nil {
		t.Fatalf("put status %s: %v", ns.ID, err)
	}
}

// TestRolloutState_MsgpackAdditiveDriverFields verifies the driver fields
// round-trip and that records without them decode to zero values.
func TestRolloutState_MsgpackAdditiveDriverFields(t *testing.T) {
	hb := time.Now().UTC().Round(time.Millisecond)
	original := RolloutState{
		ID:              "rol-x",
		State:           RolloutRolling,
		NodeResults:     map[string]*NodeResult{},
		DriverID:        "drv-abc",
		DriverHeartbeat: hb,
	}
	data, err := bus.Encode(&original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RolloutState
	if err := bus.Decode(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.DriverID != "drv-abc" {
		t.Errorf("DriverID: got %q, want %q", decoded.DriverID, "drv-abc")
	}
	if !decoded.DriverHeartbeat.Equal(hb) {
		t.Errorf("DriverHeartbeat: got %v, want %v", decoded.DriverHeartbeat, hb)
	}

	// Legacy record: no driver fields present → zero values.
	legacy := RolloutState{ID: "rol-y", State: RolloutRolling, NodeResults: map[string]*NodeResult{}}
	data, err = bus.Encode(&legacy)
	if err != nil {
		t.Fatal(err)
	}
	var legacyDecoded RolloutState
	if err := bus.Decode(data, &legacyDecoded); err != nil {
		t.Fatal(err)
	}
	if legacyDecoded.DriverID != "" || !legacyDecoded.DriverHeartbeat.IsZero() {
		t.Errorf("legacy decode: got DriverID=%q heartbeat=%v, want zero values",
			legacyDecoded.DriverID, legacyDecoded.DriverHeartbeat)
	}
}
