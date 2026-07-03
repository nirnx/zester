package job

import (
	"context"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
)

// TestSubjectPeelID covers the subject-token identity extraction shared by
// the watcher's ack and return handlers.
func TestSubjectPeelID(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		want    string
		ok      bool
	}{
		{name: "return subject", subject: "zester.job.jid-1.return.web-01", want: "web-01", ok: true},
		{name: "ack subject", subject: "zester.job.jid-1.ack.web-01", want: "web-01", ok: true},
		{name: "dotted trailing tokens", subject: "zester.job.jid-1.return.a.b", want: "a.b", ok: true},
		{name: "too few tokens", subject: "zester.job.jid-1.return", ok: false},
		{name: "empty trailing token", subject: "zester.job.jid-1.return.", ok: false},
		{name: "empty subject", subject: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := subjectPeelID(tt.subject)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("peelID = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestWatcherDropsForgedReturnPeelID pins the identity fix (reactor
// amendment 9): a return published on peel A's NATS-permission-scoped
// subject whose payload claims to be from peel B is a forgery and must be
// dropped — recorded for NEITHER peel, never persisted, and never allowed
// to complete the job on the victim's behalf.
func TestWatcherDropsForgedReturnPeelID(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-victim", "peel-attacker"}, 10*time.Second)
	j.Status = StatusRunning

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	bus.KVPut(ctx, jobsBucket, j.JID, j)

	w := NewWatcher(j, ps, js, nil)
	go w.Watch(ctx)
	time.Sleep(50 * time.Millisecond)

	// The attacker can only publish on its OWN subject (NATS enforces the
	// trailing token); it forges the victim's identity in the payload.
	forged := Return{JID: j.JID, PeelID: "peel-victim", Success: false, Error: "forged failure", Timestamp: time.Now().UTC()}
	data, _ := bus.Encode(forged)
	ps.Publish(bus.JobReturnSubject(j.JID, "peel-attacker"), data)

	// The forgery is dropped entirely: no returns recorded for either peel.
	if got := w.Returns(); len(got) != 0 {
		t.Fatalf("forged return was recorded: %v", got)
	}

	// And nothing was persisted per-peel.
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	for _, peelID := range []string{"peel-victim", "peel-attacker"} {
		var persisted Return
		if err := bus.KVGet(ctx, returnsBucket, j.JID+"."+peelID, &persisted); err == nil {
			t.Errorf("forged return persisted under %s", peelID)
		}
	}

	// The watcher must still be waiting: a forgery must not complete the job.
	select {
	case <-w.Done():
		t.Fatal("watcher finished on a forged return")
	default:
	}

	// Legitimate returns (payload matches subject) are still collected and
	// complete the job.
	for _, peelID := range []string{"peel-victim", "peel-attacker"} {
		ret := Return{JID: j.JID, PeelID: peelID, Success: true, Timestamp: time.Now().UTC()}
		data, _ := bus.Encode(ret)
		ps.Publish(bus.JobReturnSubject(j.JID, peelID), data)
	}

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish after legitimate returns")
	}

	if j.Status != StatusComplete {
		t.Errorf("Status = %q, want %q", j.Status, StatusComplete)
	}
	returns := w.Returns()
	if len(returns) != 2 {
		t.Fatalf("returns = %d, want 2", len(returns))
	}
	if ret, ok := returns["peel-victim"]; !ok || !ret.Success || ret.Error != "" {
		t.Errorf("victim's legitimate return was tainted: %+v", ret)
	}
}

// TestWatcherDropsForgedAckPeelID verifies the same subject-token authority
// for acks: a forged ack must not mark another peel as "heard" (which would
// suppress the silent-target re-dispatch meant for it).
func TestWatcherDropsForgedAckPeelID(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	j := NewJob("test.fn", nil, []string{"peel-victim", "peel-attacker"}, 500*time.Millisecond)
	j.Status = StatusRunning

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	bus.KVPut(ctx, jobsBucket, j.JID, j)

	w := NewWatcher(j, ps, js, nil)
	go w.Watch(ctx)
	time.Sleep(50 * time.Millisecond)

	// Forged: published on the attacker's subject, claiming the victim.
	forged := Ack{JID: j.JID, PeelID: "peel-victim", Timestamp: time.Now().UTC()}
	data, _ := bus.Encode(forged)
	ps.Publish(bus.JobAckSubject(j.JID, "peel-attacker"), data)

	if got := w.Acks(); len(got) != 0 {
		t.Fatalf("forged ack was recorded: %v", got)
	}

	// A legitimate ack is still collected, keyed by the subject identity.
	legit := Ack{JID: j.JID, PeelID: "peel-attacker", Timestamp: time.Now().UTC()}
	data, _ = bus.Encode(legit)
	ps.Publish(bus.JobAckSubject(j.JID, "peel-attacker"), data)

	acks := w.Acks()
	if len(acks) != 1 {
		t.Fatalf("acks = %d, want 1", len(acks))
	}
	if _, ok := acks["peel-attacker"]; !ok {
		t.Errorf("legitimate ack not keyed by subject identity: %v", acks)
	}

	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish")
	}
}

// TestRedispatchNotSuppressedByForgedAck closes the loop on the ack
// forgery: with the forged ack dropped, the victim still counts as silent
// and receives the one-shot re-dispatch it would otherwise have lost.
func TestRedispatchNotSuppressedByForgedAck(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	counter := newCmdCounter(t, ps)

	mgr := NewManager(ps, js, "master-forge", nil)
	mgr.AckWindow = 100 * time.Millisecond

	j := NewJob("cmd.run", nil, []string{"peel-victim", "peel-attacker"}, 5*time.Second)
	if err := mgr.Dispatch(ctx, j); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	time.Sleep(30 * time.Millisecond)

	// The attacker acks for itself AND forges an ack for the victim.
	legit := Ack{JID: j.JID, PeelID: "peel-attacker", Timestamp: time.Now().UTC()}
	data, _ := bus.Encode(legit)
	ps.Publish(bus.JobAckSubject(j.JID, "peel-attacker"), data)

	forged := Ack{JID: j.JID, PeelID: "peel-victim", Timestamp: time.Now().UTC()}
	data, _ = bus.Encode(forged)
	ps.Publish(bus.JobAckSubject(j.JID, "peel-attacker"), data)

	// After the ack window the silent victim must be re-sent the request;
	// the attacker, having genuinely acked, must not be.
	deadline := time.Now().Add(2 * time.Second)
	for counter.count("peel-victim") < 2 {
		if time.Now().After(deadline) {
			t.Fatal("victim was not re-dispatched (forged ack suppressed the re-send)")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := counter.count("peel-attacker"); got != 1 {
		t.Errorf("attacker publishes = %d, want 1 (genuine ack exempts re-send)", got)
	}

	mgr.Cancel(ctx, j.JID)
	waitForNoActiveJobs(t, mgr)
}
