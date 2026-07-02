package job

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/proto"
)

// cmdCounter counts ExecRequest publishes per target peel.
type cmdCounter struct {
	mu     sync.Mutex
	counts map[string]int
	last   map[string]proto.ExecRequest
}

func newCmdCounter(t *testing.T, ps bus.PubSub) *cmdCounter {
	t.Helper()
	c := &cmdCounter{counts: make(map[string]int), last: make(map[string]proto.ExecRequest)}
	sub, err := ps.Subscribe(bus.CmdSubjectAll(), func(msg *bus.Msg) {
		target := strings.TrimPrefix(msg.Subject, bus.SubjectCmd+".")
		var req proto.ExecRequest
		if err := bus.Decode(msg.Data, &req); err != nil {
			return
		}
		c.mu.Lock()
		c.counts[target]++
		c.last[target] = req
		c.mu.Unlock()
	})
	if err != nil {
		t.Fatalf("subscribe cmd wildcard: %v", err)
	}
	t.Cleanup(func() { sub.Unsubscribe() })
	return c
}

func (c *cmdCounter) count(target string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[target]
}

func (c *cmdCounter) lastReq(target string) proto.ExecRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last[target]
}

// TestManagerDispatchRedispatchesSilentTargetsOnce verifies finding 21:
// after the ack window, the dispatch watcher re-publishes the ExecRequest
// exactly once, and only to targets that neither acked nor returned.
func TestManagerDispatchRedispatchesSilentTargetsOnce(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	counter := newCmdCounter(t, ps)

	mgr := NewManager(ps, js, "master-redisp", nil)
	mgr.AckWindow = 150 * time.Millisecond

	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"peel-ack", "peel-ret", "peel-silent"}, 5*time.Second)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// Give the watcher time to subscribe, then have peel-ack acknowledge
	// and peel-ret return — both well inside the 150ms ack window.
	time.Sleep(50 * time.Millisecond)
	ack := Ack{JID: j.JID, PeelID: "peel-ack", Timestamp: time.Now().UTC()}
	ackData, _ := bus.Encode(ack)
	ps.Publish(bus.JobAckSubject(j.JID, "peel-ack"), ackData)

	ret := Return{JID: j.JID, PeelID: "peel-ret", Success: true, Timestamp: time.Now().UTC()}
	retData, _ := bus.Encode(ret)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-ret"), retData)

	// Wait well past several ack windows: the re-send must fire exactly
	// once, and only for the silent peel.
	time.Sleep(500 * time.Millisecond)

	if got := counter.count("peel-ack"); got != 1 {
		t.Errorf("peel-ack publishes = %d, want 1 (acked peels are not re-sent)", got)
	}
	if got := counter.count("peel-ret"); got != 1 {
		t.Errorf("peel-ret publishes = %d, want 1 (returned peels are not re-sent)", got)
	}
	if got := counter.count("peel-silent"); got != 2 {
		t.Errorf("peel-silent publishes = %d, want 2 (exactly one re-send)", got)
	}

	// The re-sent request carries the same identity and epoch as the job.
	req := counter.lastReq("peel-silent")
	if req.JID != j.JID {
		t.Errorf("re-sent JID = %q, want %q", req.JID, j.JID)
	}
	if req.Module != "cmd.run" {
		t.Errorf("re-sent Module = %q, want cmd.run", req.Module)
	}
	if req.Epoch != j.Epoch {
		t.Errorf("re-sent Epoch = %d, want %d", req.Epoch, j.Epoch)
	}

	// Complete the job.
	for _, peelID := range []string{"peel-ack", "peel-silent"} {
		r := Return{JID: j.JID, PeelID: peelID, Success: true, Timestamp: time.Now().UTC()}
		data, _ := bus.Encode(r)
		ps.Publish(bus.JobReturnSubject(j.JID, peelID), data)
	}
	waitForNoActiveJobs(t, mgr)

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusComplete {
		t.Errorf("final Status = %q, want %q", final.Status, StatusComplete)
	}
}

// TestManagerDispatchNoRedispatchWhenAllHeard verifies that no re-send
// happens when every target acked or returned within the window.
func TestManagerDispatchNoRedispatchWhenAllHeard(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	counter := newCmdCounter(t, ps)

	mgr := NewManager(ps, js, "master-quiet", nil)
	mgr.AckWindow = 100 * time.Millisecond

	j := NewJob("cmd.run", nil, []string{"peel-a", "peel-b"}, 5*time.Second)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	for _, peelID := range []string{"peel-a", "peel-b"} {
		ack := Ack{JID: j.JID, PeelID: peelID, Timestamp: time.Now().UTC()}
		data, _ := bus.Encode(ack)
		ps.Publish(bus.JobAckSubject(j.JID, peelID), data)
	}

	time.Sleep(300 * time.Millisecond)

	for _, peelID := range []string{"peel-a", "peel-b"} {
		if got := counter.count(peelID); got != 1 {
			t.Errorf("%s publishes = %d, want 1 (no re-send when all heard)", peelID, got)
		}
	}

	mgr.Cancel(ctx, j.JID)
	waitForNoActiveJobs(t, mgr)
}

// TestRedispatchSkipsAckedButSlowPeel pins the ack contract half of the
// double-execution fix (finding C8): a peel that ACKED the dispatch (on
// bus.JobAckSubject, before executing) but takes far longer than the ack
// window to return counts as "heard" and must NOT be re-sent the
// ExecRequest — re-sending would double-execute the job, since the peel
// epoch fence does not reject same-epoch duplicates.
func TestRedispatchSkipsAckedButSlowPeel(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	counter := newCmdCounter(t, ps)

	mgr := NewManager(ps, js, "master-slowpeel", nil)
	mgr.AckWindow = 100 * time.Millisecond

	j := NewJob("cmd.run", map[string]any{"cmd": "sleep 60"}, []string{"peel-slow"}, 5*time.Second)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// The peel acks receipt inside the window, then "executes" well past
	// several ack windows without returning.
	time.Sleep(30 * time.Millisecond)
	ack := Ack{JID: j.JID, PeelID: "peel-slow", Timestamp: time.Now().UTC()}
	ackData, _ := bus.Encode(ack)
	ps.Publish(bus.JobAckSubject(j.JID, "peel-slow"), ackData)

	time.Sleep(400 * time.Millisecond) // 4x the ack window, still no return

	if got := counter.count("peel-slow"); got != 1 {
		t.Fatalf("peel-slow publishes = %d, want 1 (acked-but-not-returned peel must not be re-dispatched)", got)
	}

	// The slow execution eventually returns; the job completes normally.
	ret := Return{JID: j.JID, PeelID: "peel-slow", Success: true, Timestamp: time.Now().UTC()}
	retData, _ := bus.Encode(ret)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-slow"), retData)
	waitForNoActiveJobs(t, mgr)

	final, err := mgr.GetJob(ctx, j.JID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if final.Status != StatusComplete {
		t.Errorf("final Status = %q, want %q", final.Status, StatusComplete)
	}
	if got := counter.count("peel-slow"); got != 1 {
		t.Errorf("peel-slow total publishes = %d, want 1 (exactly one dispatch, ever)", got)
	}
}

// TestWatcherRedispatchDisabledByFlagAndNegativeWindow verifies both off
// switches: Redispatch=false ignores the window entirely, and a negative
// AckWindow disables reconciliation even with Redispatch=true.
func TestWatcherRedispatchDisabledByFlagAndNegativeWindow(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	counter := newCmdCounter(t, ps)

	for name, configure := range map[string]func(w *Watcher){
		"flag-off":        func(w *Watcher) { w.Redispatch = false; w.AckWindow = 20 * time.Millisecond },
		"negative-window": func(w *Watcher) { w.Redispatch = true; w.AckWindow = -1 },
	} {
		j := NewJob("cmd.run", nil, []string{"peel-mute-" + name}, 300*time.Millisecond)
		j.Status = StatusRunning
		storeJob(t, js, j)

		w := NewWatcher(j, ps, js, nil)
		configure(w)
		go w.Watch(ctx)

		select {
		case <-w.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: watcher did not finish", name)
		}

		if got := counter.count("peel-mute-" + name); got != 0 {
			t.Errorf("%s: publishes = %d, want 0 (redispatch disabled)", name, got)
		}
	}
}
