package update

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

// fakeNode scripts one watchdog's update state machine for controller tests:
// it answers status probes from its fields and transitions on commands the
// way pkg/update.Handler does (including the state refusals).
type fakeNode struct {
	mu         sync.Mutex
	state      string
	pending    string
	running    string
	previous   string
	soakPassed bool
	cmds       []string
	offline    bool
}

func newFakeNode(state, pending, running string) *fakeNode {
	return &fakeNode{state: state, pending: pending, running: running, previous: running}
}

func (n *fakeNode) handle(cmd *UpdateCommand) (*UpdateResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.offline {
		return nil, fmt.Errorf("nats: no responders available for request")
	}
	n.cmds = append(n.cmds, cmd.Command)
	refuse := func(what string) *UpdateResponse {
		return &UpdateResponse{Status: "error", State: n.state, Error: fmt.Sprintf("cannot %s in state %s", what, n.state)}
	}
	switch cmd.Command {
	case CmdStatus:
		return &UpdateResponse{Status: "ok", State: n.state, Version: n.pending, SoakPassed: n.soakPassed, RunningVersion: n.running}, nil
	case CmdPrepare:
		if n.state != StateIdle && n.state != StateConfirmed {
			return refuse("prepare"), nil
		}
		n.state, n.pending, n.soakPassed = StateStaged, cmd.Version, false
		return &UpdateResponse{Status: "staged", Version: cmd.Version}, nil
	case CmdApply:
		if n.state != StateStaged {
			return refuse("apply"), nil
		}
		n.state, n.previous, n.running = StateSoaking, n.running, n.pending
		return &UpdateResponse{Status: "applying", Version: n.pending}, nil
	case CmdConfirm:
		if n.state != StateSoaking {
			return refuse("confirm"), nil
		}
		n.state, n.pending, n.soakPassed = StateConfirmed, "", false
		return &UpdateResponse{Status: "confirmed", Version: n.running}, nil
	case CmdRollback:
		if n.state != StateSoaking && n.state != StateStaged && n.state != StateApplying {
			return refuse("rollback"), nil
		}
		if n.state == StateSoaking {
			n.running = n.previous
		}
		n.state, n.pending, n.soakPassed = StateIdle, "", false
		return &UpdateResponse{Status: "rolled_back"}, nil
	}
	return &UpdateResponse{Status: "error", Error: "unknown command " + cmd.Command}, nil
}

func (n *fakeNode) snapshot() (state string, cmds []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state, append([]string(nil), n.cmds...)
}

// fleetRequestFunc routes update commands to fake nodes by subject.
func fleetRequestFunc(nodes map[string]*fakeNode) RequestFunc {
	prefix := bus.UpdateCmdSubject("")
	return func(_ context.Context, subject string, req *UpdateCommand) (*UpdateResponse, error) {
		id := strings.TrimPrefix(subject, prefix)
		n, ok := nodes[id]
		if !ok {
			return nil, fmt.Errorf("no such node %q", id)
		}
		return n.handle(req)
	}
}

func fastConfig(version string, nodes ...string) RolloutConfig {
	return RolloutConfig{
		Version:   version,
		Component: "peel",
		Target:    strings.Join(nodes, ","),
		BatchSize: len(nodes),
		SoakTime:  50 * time.Millisecond,
		// BatchPause 0 → default 30s, irrelevant with one batch.
		MaxFailed: 1,
	}
}

func newReconcileController(t *testing.T, nodes map[string]*fakeNode) (*RolloutController, *RolloutStore) {
	t.Helper()
	store, ms, _ := setupRolloutTestFull(t)
	publishTestManifest(t, ms, 0)
	ctrl := NewRolloutController(context.Background(), fleetRequestFunc(nodes), store, ms, nil, nil)
	return ctrl, store
}

func waitForNodeState(t *testing.T, n *fakeNode, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, _ := n.snapshot(); st == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, cmds := n.snapshot()
	t.Fatalf("node never reached %q (state %q, cmds %v)", want, st, cmds)
}

func TestRollout_SkipsNodesAlreadyOnTargetVersion(t *testing.T) {
	current := newFakeNode(StateIdle, "", "v1.0.0")
	stale := newFakeNode(StateConfirmed, "", "0.9.0")
	ctrl, store := newReconcileController(t, map[string]*fakeNode{"a": current, "b": stale})

	st, err := ctrl.StartRollout(context.Background(), fastConfig("v1.0.0", "a", "b"), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForRolloutState(t, store, st.ID, RolloutCompleted)

	if _, cmds := current.snapshot(); len(cmds) != 1 || cmds[0] != CmdStatus {
		t.Errorf("already-current node must only be probed, got %v", cmds)
	}
	if nr := final.NodeResults["a"]; nr == nil || nr.Status != "skipped" || !strings.Contains(nr.Note, "already running v1.0.0") {
		t.Errorf("node a result = %+v, want skipped with note", nr)
	}
	if state, cmds := stale.snapshot(); state != StateConfirmed || strings.Join(cmds, ",") != "status,prepare,apply,confirm" {
		t.Errorf("stale node: state=%s cmds=%v", state, cmds)
	}
	if nr := final.NodeResults["b"]; nr == nil || nr.Status != "confirmed" {
		t.Errorf("node b result = %+v", nr)
	}
	if final.FailedCount != 0 {
		t.Errorf("FailedCount = %d", final.FailedCount)
	}
}

func TestRollout_ResumesStagedNodeFromApply(t *testing.T) {
	staged := newFakeNode(StateStaged, "v1.0.0", "0.9.0")
	ctrl, store := newReconcileController(t, map[string]*fakeNode{"a": staged})

	st, err := ctrl.StartRollout(context.Background(), fastConfig("v1.0.0", "a"), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForRolloutState(t, store, st.ID, RolloutCompleted)
	if state, cmds := staged.snapshot(); state != StateConfirmed || strings.Join(cmds, ",") != "status,apply,confirm" {
		t.Errorf("staged node must resume at apply: state=%s cmds=%v", state, cmds)
	}
	if nr := final.NodeResults["a"]; nr == nil || nr.Status != "confirmed" || !strings.Contains(nr.Note, "resumed from staged") {
		t.Errorf("result = %+v", nr)
	}
}

func TestRollout_AdoptedMidSoakConfirmsWithoutPrepare(t *testing.T) {
	// The single-master self-update shape: the driver died after apply, the
	// node soaked on its own and reports soak_passed; the adopting master
	// must confirm, not re-prepare (which the watchdog would refuse and the
	// old controller counted as a failure → MaxFailed → abort → no confirm →
	// deadline rollback).
	soaking := newFakeNode(StateSoaking, "v1.0.0", "v1.0.0")
	soaking.soakPassed = true
	ctrl, store := newReconcileController(t, map[string]*fakeNode{"a": soaking})
	ctrl.DriverID = "drv-new-master"

	orphan := &RolloutState{
		ID:              "rol-orphan",
		Config:          RolloutConfig{Version: "v1.0.0", Component: "peel", Target: "a", BatchSize: 1, SoakTime: time.Hour, BatchPause: time.Second, MaxFailed: 1},
		State:           RolloutRolling,
		Batches:         [][]string{{"a"}},
		NodeResults:     map[string]*NodeResult{"a": {ID: "a", Status: "applied"}},
		CurrentBatch:    0,
		StartedAt:       time.Now().Add(-10 * time.Minute),
		DriverID:        "drv-dead-master",
		DriverHeartbeat: time.Now().Add(-10 * time.Minute),
	}
	if err := store.Save(context.Background(), orphan); err != nil {
		t.Fatal(err)
	}

	n, err := ctrl.ResumeOrphaned(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("ResumeOrphaned: n=%d err=%v", n, err)
	}
	// SoakTime is an hour: completion within the 5s wait proves the passed
	// soak was honored and no second soak window was waited.
	final := waitForRolloutState(t, store, orphan.ID, RolloutCompleted)
	if state, cmds := soaking.snapshot(); state != StateConfirmed || strings.Join(cmds, ",") != "status,confirm" {
		t.Errorf("adopted soaking node: state=%s cmds=%v (must be status,confirm — never prepare)", state, cmds)
	}
	if nr := final.NodeResults["a"]; nr == nil || nr.Status != "confirmed" || !strings.Contains(nr.Note, "soak already passed") {
		t.Errorf("result = %+v", nr)
	}
	if final.FailedCount != 0 || final.DriverID != "drv-new-master" {
		t.Errorf("failed=%d driver=%s", final.FailedCount, final.DriverID)
	}
}

func TestRollout_AdoptedMidSoakWaitsWhenSoakNotPassed(t *testing.T) {
	soaking := newFakeNode(StateSoaking, "v1.0.0", "v1.0.0") // soakPassed false
	ctrl, store := newReconcileController(t, map[string]*fakeNode{"a": soaking})
	st, err := ctrl.StartRollout(context.Background(), fastConfig("v1.0.0", "a"), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForRolloutState(t, store, st.ID, RolloutCompleted)
	if state, cmds := soaking.snapshot(); state != StateConfirmed || strings.Join(cmds, ",") != "status,confirm" {
		t.Errorf("state=%s cmds=%v", state, cmds)
	}
	if nr := final.NodeResults["a"]; nr == nil || nr.Note != "resumed from soaking" {
		t.Errorf("result = %+v", nr)
	}
}

func TestRollout_ForeignPendingVersionIsResetFirst(t *testing.T) {
	leftover := newFakeNode(StateStaged, "v0.5.0", "0.4.0")
	ctrl, store := newReconcileController(t, map[string]*fakeNode{"a": leftover})
	st, err := ctrl.StartRollout(context.Background(), fastConfig("v1.0.0", "a"), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForRolloutState(t, store, st.ID, RolloutCompleted)
	if state, cmds := leftover.snapshot(); state != StateConfirmed || strings.Join(cmds, ",") != "status,rollback,prepare,apply,confirm" {
		t.Errorf("state=%s cmds=%v", state, cmds)
	}
	if nr := final.NodeResults["a"]; nr == nil || nr.Status != "confirmed" || !strings.Contains(nr.Note, "discarded staged v0.5.0") {
		t.Errorf("result = %+v", nr)
	}
}

func TestRollout_UnreachableNodeCountsAsFailure(t *testing.T) {
	gone := newFakeNode(StateIdle, "", "0.9.0")
	gone.offline = true
	ok := newFakeNode(StateIdle, "", "0.9.0")
	ctrl, store := newReconcileController(t, map[string]*fakeNode{"a": gone, "b": ok})
	cfg := fastConfig("v1.0.0", "a", "b")
	cfg.MaxFailed = 1
	st, err := ctrl.StartRollout(context.Background(), cfg, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForRolloutState(t, store, st.ID, RolloutAborted)
	if nr := final.NodeResults["a"]; nr == nil || nr.Status != "failed" || !strings.Contains(nr.Error, "status probe") {
		t.Errorf("unreachable node result = %+v", nr)
	}
	if final.FailedCount != 1 {
		t.Errorf("FailedCount = %d", final.FailedCount)
	}
	// MaxFailed=1 with the failure found at reconcile time: the reachable
	// node must not have been touched beyond the probe.
	if state, cmds := ok.snapshot(); state != StateIdle || strings.Contains(strings.Join(cmds, ","), "prepare") {
		t.Errorf("reachable node must not be prepared after the batch aborted: state=%s cmds=%v", state, cmds)
	}
}

func TestAbortRollout_RevertsSoakingNodes(t *testing.T) {
	node := newFakeNode(StateIdle, "", "0.9.0")
	ctrl, store := newReconcileController(t, map[string]*fakeNode{"a": node})
	cfg := fastConfig("v1.0.0", "a")
	cfg.SoakTime = time.Hour // the abort lands mid-soak
	st, err := ctrl.StartRollout(context.Background(), cfg, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	waitForNodeState(t, node, StateSoaking)

	if err := ctrl.AbortRollout(context.Background(), st.ID); err != nil {
		t.Fatal(err)
	}
	final := waitForRolloutState(t, store, st.ID, RolloutAborted)
	state, cmds := node.snapshot()
	if state != StateIdle || cmds[len(cmds)-1] != CmdRollback {
		t.Errorf("aborted rollout must roll the soaking node back: state=%s cmds=%v", state, cmds)
	}
	if node.running != "0.9.0" {
		t.Errorf("node should run the previous version again, got %s", node.running)
	}
	if nr := final.NodeResults["a"]; nr == nil || nr.Status != "rolled_back" || !strings.Contains(nr.Note, "rolled back on abort from soaking") {
		t.Errorf("result = %+v", nr)
	}
}

func TestAbortRollout_NotLocallyActive_RevertsStagedNodes(t *testing.T) {
	staged := newFakeNode(StateStaged, "v1.0.0", "0.9.0")
	idle := newFakeNode(StateIdle, "", "0.9.0")
	other := newFakeNode(StateStaged, "v2.0.0", "0.9.0") // another rollout's leftover: left alone
	ctrl, store := newReconcileController(t, map[string]*fakeNode{"a": staged, "b": idle, "c": other})

	// A rollout record driven by a master that died: no local driver.
	orphan := &RolloutState{
		ID:              "rol-dead",
		Config:          RolloutConfig{Version: "v1.0.0", Component: "peel", Target: "*", BatchSize: 3, SoakTime: time.Minute, MaxFailed: 1},
		State:           RolloutRolling,
		Batches:         [][]string{{"a", "b", "c"}},
		NodeResults:     map[string]*NodeResult{},
		DriverID:        "drv-dead",
		DriverHeartbeat: time.Now().Add(-time.Hour),
	}
	if err := store.Save(context.Background(), orphan); err != nil {
		t.Fatal(err)
	}

	if err := ctrl.AbortRollout(context.Background(), orphan.ID); err != nil {
		t.Fatal(err)
	}
	final, err := store.Get(context.Background(), orphan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != RolloutAborted {
		t.Fatalf("state = %s", final.State)
	}
	if state, cmds := staged.snapshot(); state != StateIdle || strings.Join(cmds, ",") != "status,rollback" {
		t.Errorf("staged node of the aborted rollout must be reset: state=%s cmds=%v", state, cmds)
	}
	if state, cmds := idle.snapshot(); state != StateIdle || strings.Join(cmds, ",") != "status" {
		t.Errorf("idle node must only be probed: state=%s cmds=%v", state, cmds)
	}
	if state, cmds := other.snapshot(); state != StateStaged || strings.Join(cmds, ",") != "status" {
		t.Errorf("a node staged for ANOTHER version must be left alone: state=%s cmds=%v", state, cmds)
	}
	if nr := final.NodeResults["a"]; nr == nil || nr.Status != "rolled_back" {
		t.Errorf("node a result = %+v", nr)
	}
	if nr := final.NodeResults["c"]; nr == nil || !strings.Contains(nr.Note, "not this rollout's version") {
		t.Errorf("node c should carry an explanatory note, got %+v", nr)
	}
	// A revoked driver-less record, once aborted, is never re-adopted.
	if n, _ := ctrl.ResumeOrphaned(context.Background()); n != 0 {
		t.Errorf("aborted record must not be adopted, got %d", n)
	}
}

func TestSameVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.7.0", "0.7.0", true}, {"0.7.0", "0.7.0", true}, {" v0.7.0 ", "0.7.0", true},
		{"v0.7.0", "v0.7.1", false}, {"", "", false}, {"dev", "", false},
	}
	for _, c := range cases {
		if got := sameVersion(c.a, c.b); got != c.want {
			t.Errorf("sameVersion(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
