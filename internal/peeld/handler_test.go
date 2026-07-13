package peeld

import (
	"context"
	"testing"

	"github.com/nirnx/zester/internal/config"
	"github.com/nirnx/zester/internal/metrics"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/execmod"
	"github.com/nirnx/zester/pkg/facts"
	"github.com/nirnx/zester/pkg/job"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/proto"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules"
)

// newTestAgent builds an Agent with just enough wiring for handler-level
// tests: no NATS, no disk persistence, an empty facts manager backed by the
// in-memory fake JetStream.
func newTestAgent(t *testing.T) *Agent {
	t.Helper()

	a := New(&config.PeelConfig{ID: "p1"}, discardLogger())
	a.settingsSnapshotPath = "" // no disk writes from tests
	a.dedup = newDedupTracker("", dedupCapacity, noSave, discardLogger())
	a.runCtx = context.Background()
	a.metrics = metrics.NewPeelRegistry()
	a.ps = bustest.NewFakePubSub()
	a.execReg = execmod.DefaultRegistry()
	a.registry = state.NewRegistry()
	a.registry.Register("test.ping", modules.NewTestPingBuilder(modschema.DecodeOptions{}))
	a.wireDocSource() // registers sys.doc + merged sys.list_functions on execReg
	a.runner = state.NewRunner(discardLogger())

	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(context.Background(), js); err != nil {
		t.Fatal(err)
	}
	mgr, err := facts.NewManager(facts.ManagerConfig{PeelID: "p1", JS: js, Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetFact("role", "db")
	a.mgr = mgr

	return a
}

// TestReadOnlyModuleClassification pins the exact read-only set: facts.*
// EXCEPT the mutating facts.set (C7), settings.*/pillar.*, test.ping,
// sys.list_functions, sys.doc, grains.* — and nothing else. The
// facts./settings./pillar. classification is now derived from the shared
// modules.DispatchSpecials table (IsReadOnlyDispatch).
func TestReadOnlyModuleClassification(t *testing.T) {
	a := newTestAgent(t)

	readOnly := []string{
		"facts.get", "facts.items", "facts.keys",
		"facts.bogus_query", // unknown facts subfunction still routes read-only (family catch-all)
		"settings.get", "settings.items", "settings.keys",
		"pillar.get", "pillar.items",
		"test.ping",
		"sys.list_functions",
		"sys.doc",
		"grains.item", "grains.items",
	}
	for _, m := range readOnly {
		if !a.readOnlyModule(m) {
			t.Errorf("readOnlyModule(%q) = false, want true", m)
		}
	}

	mutating := []string{
		"state.apply", "state.highstate",
		"cmd.run", "pkg.installed", "pkg.version",
		"service.restart", "disk.usage",
		"test.nop", "test.echo",
		"file.managed", "module.run",
		"facts.set",             // mutates the custom-facts file — execMu-serialized (C7)
		"grains.bogus_function", // not in the exec registry → serialized path errors
	}
	for _, m := range mutating {
		if a.readOnlyModule(m) {
			t.Errorf("readOnlyModule(%q) = true, want false", m)
		}
	}

	// A state module shadowing an exec-registry name must keep precedence:
	// grains.items overridden by a (Starlark) state module leaves the
	// read-only fast path.
	a.registry.Register("grains.items", modules.NewTestPingBuilder(modschema.DecodeOptions{}))
	if a.readOnlyModule("grains.items") {
		t.Error("readOnlyModule(grains.items) = true after state module shadowed it")
	}
}

// TestHandleExecRequestQueueFull verifies the bounded-queue rejection: when
// the queue is full (no worker draining it), a request/reply exec gets an
// immediate "peel busy" error response instead of blocking.
func TestHandleExecRequestQueueFull(t *testing.T) {
	a := newTestAgent(t)
	a.execQueue = make(chan execTask, 1)
	a.execQueue <- execTask{} // fill the queue; no worker is running

	req := proto.ExecRequest{ID: "echo hi", Module: "cmd.run"}
	data, err := bus.Encode(req)
	if err != nil {
		t.Fatal(err)
	}

	var resp proto.ExecResponse
	responded := false
	msg := bus.NewMsg("zester.cmd.p1", data, "_INBOX.test", func(b []byte) error {
		responded = true
		return bus.Decode(b, &resp)
	})

	a.handleExecRequest(msg)

	if !responded {
		t.Fatal("no response sent for queue-full rejection")
	}
	if resp.Error != execQueueFullMsg {
		t.Errorf("error = %q, want %q", resp.Error, execQueueFullMsg)
	}
	if resp.Success {
		t.Error("queue-full response marked success")
	}
}

// TestHandleExecRequestQueueFullJobReturn verifies the fire-and-forget (job
// dispatch) variant: rejection is published as a failed job return, and the
// job's cancel registration is released.
func TestHandleExecRequestQueueFullJobReturn(t *testing.T) {
	a := newTestAgent(t)
	a.execQueue = make(chan execTask, 1)
	a.execQueue <- execTask{}

	ps := a.ps.(*bustest.FakePubSub)
	var ret struct {
		Success bool   `msgpack:"success"`
		Error   string `msgpack:"error"`
	}
	got := false
	if _, err := ps.Subscribe("zester.job.j1.return.p1", func(msg *bus.Msg) {
		got = true
		if err := bus.Decode(msg.Data, &ret); err != nil {
			t.Errorf("decode return: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}

	req := proto.ExecRequest{JID: "j1", Epoch: 1, Module: "cmd.run", Args: map[string]any{"name": "echo hi"}}
	data, err := bus.Encode(req)
	if err != nil {
		t.Fatal(err)
	}

	a.handleExecRequest(bus.NewMsg("zester.cmd.p1", data, "", nil))

	if !got {
		t.Fatal("no job return published for queue-full rejection")
	}
	if ret.Success || ret.Error != execQueueFullMsg {
		t.Errorf("return = success=%v error=%q, want failed %q", ret.Success, ret.Error, execQueueFullMsg)
	}

	a.cancelMu.Lock()
	_, registered := a.cancelFuncs["j1"]
	a.cancelMu.Unlock()
	if registered {
		t.Error("cancel registration leaked after queue-full rejection")
	}
}

// TestHandleExecRequestReadOnlyBypassesQueue verifies that read-only modules
// answer inline even with a completely full queue and no worker.
func TestHandleExecRequestReadOnlyBypassesQueue(t *testing.T) {
	a := newTestAgent(t)
	a.execQueue = make(chan execTask, 1)
	a.execQueue <- execTask{} // full, no worker

	req := proto.ExecRequest{Module: "facts.get", Args: map[string]any{"key": "role"}}
	data, err := bus.Encode(req)
	if err != nil {
		t.Fatal(err)
	}

	var resp proto.ExecResponse
	msg := bus.NewMsg("zester.cmd.p1", data, "_INBOX.test", func(b []byte) error {
		return bus.Decode(b, &resp)
	})

	a.handleExecRequest(msg)

	if !resp.Success {
		t.Fatalf("read-only exec failed: %q", resp.Error)
	}
	if len(resp.Results) != 1 || resp.Results[0].Details["result"] != "db" {
		t.Errorf("results = %+v, want facts.get role = db", resp.Results)
	}
}

// TestHandleExecRequestJobAck verifies the peel half of the ack-window
// protocol (C8): an ACCEPTED job dispatch publishes exactly one job.Ack on
// zester.job.<jid>.ack.<peelID> before execution; rejected (duplicate/stale)
// re-deliveries and direct request/reply executions publish none.
func TestHandleExecRequestJobAck(t *testing.T) {
	a := newTestAgent(t)
	ps := a.ps.(*bustest.FakePubSub)

	var acks []struct {
		subject string
		ack     job.Ack
	}
	if _, err := ps.Subscribe("zester.job.*.ack.*", func(msg *bus.Msg) {
		var ack job.Ack
		if err := bus.Decode(msg.Data, &ack); err != nil {
			t.Errorf("decode ack: %v", err)
			return
		}
		acks = append(acks, struct {
			subject string
			ack     job.Ack
		}{msg.Subject, ack})
	}); err != nil {
		t.Fatal(err)
	}

	// Accepted job dispatch (test.ping runs on the inline read-only path, so
	// no worker goroutine is needed) → exactly one ack, correct shape.
	req := proto.ExecRequest{JID: "j9", Epoch: 2, ID: "ping", Module: "test.ping"}
	data, err := bus.Encode(req)
	if err != nil {
		t.Fatal(err)
	}
	a.handleExecRequest(bus.NewMsg("zester.cmd.p1", data, "", nil))

	if len(acks) != 1 {
		t.Fatalf("acks = %d, want exactly 1 after accepted dispatch", len(acks))
	}
	if got, want := acks[0].subject, bus.JobAckSubject("j9", "p1"); got != want {
		t.Errorf("ack subject = %q, want %q", got, want)
	}
	if acks[0].ack.JID != "j9" || acks[0].ack.PeelID != "p1" {
		t.Errorf("ack payload = %+v, want jid=j9 peel=p1", acks[0].ack)
	}
	if acks[0].ack.Timestamp.IsZero() {
		t.Error("ack timestamp is zero")
	}

	// Duplicate re-delivery (same jid, same epoch) is rejected → no ack.
	a.handleExecRequest(bus.NewMsg("zester.cmd.p1", data, "", nil))
	if len(acks) != 1 {
		t.Fatalf("acks = %d after duplicate delivery, want still 1", len(acks))
	}

	// Stale epoch is rejected → no ack.
	req.Epoch = 1
	if data, err = bus.Encode(req); err != nil {
		t.Fatal(err)
	}
	a.handleExecRequest(bus.NewMsg("zester.cmd.p1", data, "", nil))
	if len(acks) != 1 {
		t.Fatalf("acks = %d after stale delivery, want still 1", len(acks))
	}

	// Direct request/reply (not a job dispatch) → no ack.
	direct := proto.ExecRequest{ID: "ping", Module: "test.ping"}
	if data, err = bus.Encode(direct); err != nil {
		t.Fatal(err)
	}
	a.handleExecRequest(bus.NewMsg("zester.cmd.p1", data, "_INBOX.test", func([]byte) error { return nil }))
	if len(acks) != 1 {
		t.Fatalf("acks = %d after direct request/reply, want still 1", len(acks))
	}
}

// TestHandleExecRequestDuplicateJobRejected pins the persisted-dedup handler
// behavior: the same (jid, epoch) job dispatch executes once; the re-delivery
// is dropped without executing or responding.
func TestHandleExecRequestDuplicateJobRejected(t *testing.T) {
	a := newTestAgent(t)

	ps := a.ps.(*bustest.FakePubSub)
	returns := 0
	if _, err := ps.Subscribe("zester.job.j7.return.p1", func(msg *bus.Msg) {
		returns++
	}); err != nil {
		t.Fatal(err)
	}

	// Worker drains the queue synchronously for the test. t.Context() is
	// canceled automatically at test cleanup, standing in for the explicit
	// WithCancel/defer pair (stopWorker was never called early, only deferred).
	workerCtx := t.Context()
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		a.runExecWorker(workerCtx)
	}()

	req := proto.ExecRequest{JID: "j7", Epoch: 3, ID: "ping", Module: "test.ping"}
	data, err := bus.Encode(req)
	if err != nil {
		t.Fatal(err)
	}

	// First delivery executes (read-only fast path publishes the return
	// inline); duplicate delivery must be rejected by the dedup fence.
	a.handleExecRequest(bus.NewMsg("zester.cmd.p1", data, "", nil))
	a.handleExecRequest(bus.NewMsg("zester.cmd.p1", data, "", nil))

	if returns != 1 {
		t.Fatalf("returns = %d, want exactly 1 (duplicate must be dropped)", returns)
	}

	// A newer epoch (reclaim by a newer master) is accepted again.
	req.Epoch = 4
	data, err = bus.Encode(req)
	if err != nil {
		t.Fatal(err)
	}
	a.handleExecRequest(bus.NewMsg("zester.cmd.p1", data, "", nil))
	if returns != 2 {
		t.Fatalf("returns = %d, want 2 after higher-epoch redispatch", returns)
	}
}
