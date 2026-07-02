package update

import (
	"context"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/bus/bustest"
)

func setupRolloutTest(t *testing.T) (*RolloutStore, *ManifestStore, *BinaryStore) {
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

	return NewRolloutStore(rolloutKV), NewManifestStore(manifestKV), NewBinaryStore(newFakeObjectStore())
}

func mockRequestFunc(responses map[string]UpdateResponse) RequestFunc {
	return func(ctx context.Context, subject string, req *UpdateCommand) (*UpdateResponse, error) {
		resp, ok := responses[req.Command]
		if !ok {
			return &UpdateResponse{Status: "error", Error: "unexpected command"}, nil
		}
		return &resp, nil
	}
}

func TestComputeBatches_BySize(t *testing.T) {
	tests := []struct {
		name          string
		nodes         []string
		batchSize     int
		wantBatchLens []int
	}{
		{
			name:          "5 nodes batchSize 2",
			nodes:         []string{"a", "b", "c", "d", "e"},
			batchSize:     2,
			wantBatchLens: []int{2, 2, 1},
		},
		{
			name:          "3 nodes batchSize 1",
			nodes:         []string{"a", "b", "c"},
			batchSize:     1,
			wantBatchLens: []int{1, 1, 1},
		},
		{
			name:          "3 nodes batchSize 5",
			nodes:         []string{"a", "b", "c"},
			batchSize:     5,
			wantBatchLens: []int{3},
		},
		{
			name:          "0 nodes",
			nodes:         []string{},
			batchSize:     2,
			wantBatchLens: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			batches := computeBatches(tc.nodes, tc.batchSize, 0)
			if tc.wantBatchLens == nil {
				if batches != nil {
					t.Fatalf("expected nil batches, got %v", batches)
				}
				return
			}
			if len(batches) != len(tc.wantBatchLens) {
				t.Fatalf("expected %d batches, got %d", len(tc.wantBatchLens), len(batches))
			}
			for i, want := range tc.wantBatchLens {
				if len(batches[i]) != want {
					t.Errorf("batch[%d]: expected len %d, got %d", i, want, len(batches[i]))
				}
			}
		})
	}
}

func TestComputeBatches_ByPercent(t *testing.T) {
	t.Run("10 nodes 30 percent", func(t *testing.T) {
		nodes := make([]string, 10)
		for i := range nodes {
			nodes[i] = string(rune('a' + i))
		}
		batches := computeBatches(nodes, 0, 30)
		wantLens := []int{3, 3, 3, 1}
		if len(batches) != len(wantLens) {
			t.Fatalf("expected %d batches, got %d", len(wantLens), len(batches))
		}
		for i, want := range wantLens {
			if len(batches[i]) != want {
				t.Errorf("batch[%d]: expected len %d, got %d", i, want, len(batches[i]))
			}
		}
	})

	t.Run("5 nodes 10 percent clamps to 1", func(t *testing.T) {
		nodes := []string{"a", "b", "c", "d", "e"}
		batches := computeBatches(nodes, 0, 10)
		// 5 * 10 / 100 = 0 → clamped to 1 → 5 batches of 1
		if len(batches) != 5 {
			t.Fatalf("expected 5 batches, got %d", len(batches))
		}
		for i, batch := range batches {
			if len(batch) != 1 {
				t.Errorf("batch[%d]: expected len 1, got %d", i, len(batch))
			}
		}
	})
}

func TestRolloutStore_SaveAndGet(t *testing.T) {
	store, _, _ := setupRolloutTest(t)
	ctx := context.Background()

	original := &RolloutState{
		ID:    "rol-test-001",
		State: RolloutCreated,
		Config: RolloutConfig{
			Version:   "v1.2.3",
			Component: "peel",
			Target:    "web-*",
			BatchSize: 2,
			SoakTime:  10 * time.Second,
		},
		Batches:     [][]string{{"node-1", "node-2"}, {"node-3"}},
		NodeResults: make(map[string]*NodeResult),
		StartedAt:   time.Now().Round(time.Millisecond),
		FailedCount: 0,
	}

	if err := store.Save(ctx, original); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	got, err := store.Get(ctx, original.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if got.ID != original.ID {
		t.Errorf("ID mismatch: got %q, want %q", got.ID, original.ID)
	}
	if got.State != original.State {
		t.Errorf("State mismatch: got %q, want %q", got.State, original.State)
	}
	if got.Config.Version != original.Config.Version {
		t.Errorf("Config.Version mismatch: got %q, want %q", got.Config.Version, original.Config.Version)
	}
	if got.Config.Component != original.Config.Component {
		t.Errorf("Config.Component mismatch: got %q, want %q", got.Config.Component, original.Config.Component)
	}
	if len(got.Batches) != len(original.Batches) {
		t.Errorf("Batches count mismatch: got %d, want %d", len(got.Batches), len(original.Batches))
	}
}

func TestRolloutStore_List(t *testing.T) {
	store, _, _ := setupRolloutTest(t)
	ctx := context.Background()

	for i, id := range []string{"rol-001", "rol-002", "rol-003"} {
		_ = i
		if err := store.Save(ctx, &RolloutState{
			ID:          id,
			State:       RolloutCreated,
			NodeResults: make(map[string]*NodeResult),
		}); err != nil {
			t.Fatalf("Save %s failed: %v", id, err)
		}
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("expected 3 rollouts, got %d", len(list))
	}
}

func TestRolloutStore_Get_NotFound(t *testing.T) {
	store, _, _ := setupRolloutTest(t)
	ctx := context.Background()

	_, err := store.Get(ctx, "nonexistent-rollout-id")
	if err == nil {
		t.Fatal("expected error for non-existent rollout, got nil")
	}
}

func TestRolloutController_DryRun(t *testing.T) {
	rolloutStore, manifestStore, _ := setupRolloutTest(t)
	ctx := context.Background()

	if err := manifestStore.Publish(ctx, &Manifest{
		Version:   "v1.0.0",
		Component: "peel",
		GOOS:      "linux",
		GOARCH:    "amd64",
		SHA256:    "abc123",
		ObjectKey: "peel/linux/amd64/v1.0.0",
	}); err != nil {
		t.Fatalf("Publish manifest failed: %v", err)
	}

	called := false
	reqFn := func(ctx context.Context, subject string, req *UpdateCommand) (*UpdateResponse, error) {
		called = true
		return &UpdateResponse{Status: "staged"}, nil
	}

	ctrl := NewRolloutController(context.Background(), reqFn, rolloutStore, manifestStore, nil, nil)

	nodes := []string{"node-1", "node-2", "node-3"}
	state, err := ctrl.StartRollout(ctx, RolloutConfig{
		Version:   "v1.0.0",
		Component: "peel",
		BatchSize: 2,
		DryRun:    true,
	}, nodes)
	if err != nil {
		t.Fatalf("StartRollout failed: %v", err)
	}

	if state.State != RolloutCreated {
		t.Errorf("expected state %q, got %q", RolloutCreated, state.State)
	}
	if len(state.Batches) == 0 {
		t.Error("expected non-empty batches")
	}
	if called {
		t.Error("RequestFunc should not be called for dry run")
	}
}

func TestRolloutController_StartRollout(t *testing.T) {
	rolloutStore, manifestStore, _ := setupRolloutTest(t)
	ctx := context.Background()

	if err := manifestStore.Publish(ctx, &Manifest{
		Version:   "v1.0.0",
		Component: "peel",
		GOOS:      "linux",
		GOARCH:    "amd64",
		SHA256:    "abc123",
		ObjectKey: "peel/linux/amd64/v1.0.0",
	}); err != nil {
		t.Fatalf("Publish manifest failed: %v", err)
	}

	reqFn := mockRequestFunc(map[string]UpdateResponse{
		CmdPrepare: {Status: "staged"},
		CmdApply:   {Status: "applying"},
		CmdConfirm: {Status: "confirmed"},
	})

	ctrl := NewRolloutController(context.Background(), reqFn, rolloutStore, manifestStore, nil, nil)

	nodes := []string{"node-1", "node-2"}
	state, err := ctrl.StartRollout(ctx, RolloutConfig{
		Version:    "v1.0.0",
		Component:  "peel",
		BatchSize:  2,
		SoakTime:   100 * time.Millisecond,
		BatchPause: 50 * time.Millisecond,
	}, nodes)
	if err != nil {
		t.Fatalf("StartRollout failed: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := rolloutStore.Get(ctx, state.ID)
		if err != nil {
			t.Fatalf("Get rollout failed: %v", err)
		}
		if got.State == RolloutCompleted || got.State == RolloutAborted {
			state = got
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if state.State != RolloutCompleted {
		t.Errorf("expected state %q, got %q", RolloutCompleted, state.State)
	}

	for _, nodeID := range nodes {
		nr, ok := state.NodeResults[nodeID]
		if !ok {
			t.Errorf("missing NodeResult for %s", nodeID)
			continue
		}
		if nr.Status != "confirmed" {
			t.Errorf("node %s: expected status %q, got %q", nodeID, "confirmed", nr.Status)
		}
	}
}

func TestRolloutController_MaxFailedAbort(t *testing.T) {
	rolloutStore, manifestStore, _ := setupRolloutTest(t)
	ctx := context.Background()

	if err := manifestStore.Publish(ctx, &Manifest{
		Version:   "v1.0.0",
		Component: "peel",
		GOOS:      "linux",
		GOARCH:    "amd64",
		SHA256:    "abc123",
		ObjectKey: "peel/linux/amd64/v1.0.0",
	}); err != nil {
		t.Fatalf("Publish manifest failed: %v", err)
	}

	reqFn := mockRequestFunc(map[string]UpdateResponse{
		CmdPrepare: {Status: "error", Error: "download failed"},
	})

	ctrl := NewRolloutController(context.Background(), reqFn, rolloutStore, manifestStore, nil, nil)

	nodes := []string{"node-1", "node-2"}
	state, err := ctrl.StartRollout(ctx, RolloutConfig{
		Version:    "v1.0.0",
		Component:  "peel",
		BatchSize:  2,
		MaxFailed:  1,
		SoakTime:   100 * time.Millisecond,
		BatchPause: 50 * time.Millisecond,
	}, nodes)
	if err != nil {
		t.Fatalf("StartRollout failed: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := rolloutStore.Get(ctx, state.ID)
		if err != nil {
			t.Fatalf("Get rollout failed: %v", err)
		}
		if got.State == RolloutAborted || got.State == RolloutCompleted {
			state = got
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if state.State != RolloutAborted {
		t.Errorf("expected state %q, got %q", RolloutAborted, state.State)
	}
	if state.FailedCount < 1 {
		t.Errorf("expected FailedCount >= 1, got %d", state.FailedCount)
	}
}

func TestRolloutController_AbortRollout(t *testing.T) {
	rolloutStore, manifestStore, _ := setupRolloutTest(t)
	ctx := context.Background()

	if err := manifestStore.Publish(ctx, &Manifest{
		Version:   "v1.0.0",
		Component: "peel",
		GOOS:      "linux",
		GOARCH:    "amd64",
		SHA256:    "abc123",
		ObjectKey: "peel/linux/amd64/v1.0.0",
	}); err != nil {
		t.Fatalf("Publish manifest failed: %v", err)
	}

	reqFn := func(ctx context.Context, subject string, req *UpdateCommand) (*UpdateResponse, error) {
		select {
		case <-ctx.Done():
			return &UpdateResponse{Status: "error", Error: "context cancelled"}, nil
		case <-time.After(2 * time.Second):
			return &UpdateResponse{Status: "staged"}, nil
		}
	}

	ctrl := NewRolloutController(context.Background(), reqFn, rolloutStore, manifestStore, nil, nil)

	nodes := []string{"node-1", "node-2", "node-3", "node-4", "node-5"}
	state, err := ctrl.StartRollout(ctx, RolloutConfig{
		Version:   "v1.0.0",
		Component: "peel",
		BatchSize: 1,
		SoakTime:  5 * time.Second,
	}, nodes)
	if err != nil {
		t.Fatalf("StartRollout failed: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	if err := ctrl.AbortRollout(ctx, state.ID); err != nil {
		t.Fatalf("AbortRollout failed: %v", err)
	}

	// AbortRollout signals the goroutine asynchronously. Poll KV until
	// the goroutine processes the signal and persists the aborted state.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := rolloutStore.Get(ctx, state.ID)
		if err != nil {
			t.Fatalf("Get rollout failed: %v", err)
		}
		if got.State == RolloutAborted {
			return // success
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("rollout did not reach aborted state within 5s")
}
