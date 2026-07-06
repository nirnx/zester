package beacon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/event"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/proto"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recorder captures published (subject, event) pairs and supports error
// injection for offline-buffer tests. Single-goroutine use (tick is driven
// directly by the test) except where noted.
type recorder struct {
	published []outbound
	err       error
}

func (r *recorder) publish(subject string, ev event.Event) error {
	if r.err != nil {
		return r.err
	}
	r.published = append(r.published, outbound{subject: subject, ev: ev})
	return nil
}

// serviceCfg is a shorthand for a hand-built service beacon config.
func serviceCfg(onChangeOnly bool, services ...string) Config {
	return Config{Service: &ServiceConfig{
		Services:     services,
		Interval:     DefaultInterval,
		OnChangeOnly: onChangeOnly,
	}}
}

func newTestManager(t *testing.T, rec *recorder, svc *exectest.FakeServiceExec, busy *bool) *Manager {
	t.Helper()
	fakeNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	cfg := ManagerConfig{
		PeelID:  "web-01",
		Publish: rec.publish,
		BusyFn:  func() bool { return busy != nil && *busy },
		Logger:  discardLogger(),
		Now:     func() time.Time { return fakeNow },
	}
	if svc != nil {
		cfg.Service = svc
	}
	return NewManager(cfg)
}

// TestServiceSubjectTagRoundTrip pins the amendment-22 invariant: publishing
// on bus.BeaconSubject(peelID, "service") yields exactly the slash tag
// "beacon/<peelID>/service" through event.ParseSubject, and the emitted
// event's Tag matches it (anti-spoof cross-check on the consumer side).
func TestServiceSubjectTagRoundTrip(t *testing.T) {
	svc := exectest.NewFakeServiceExec("systemd")
	svc.PreAdd("nginx", true, true)
	rec := &recorder{}
	m := newTestManager(t, rec, svc, nil)
	m.UpdateConfig(serviceCfg(false, "nginx"))

	m.tick(context.Background())

	if len(rec.published) != 1 {
		t.Fatalf("published = %d events, want 1", len(rec.published))
	}
	got := rec.published[0]

	wantSubject := bus.BeaconSubject("web-01", ServiceBeaconName)
	if got.subject != wantSubject {
		t.Errorf("subject = %q, want %q", got.subject, wantSubject)
	}

	origin, slashTag, kind, err := event.ParseSubject(got.subject)
	if err != nil {
		t.Fatalf("ParseSubject(%q): %v", got.subject, err)
	}
	if origin != "web-01" {
		t.Errorf("origin = %q, want web-01", origin)
	}
	if slashTag != "beacon/web-01/service" {
		t.Errorf("slash tag = %q, want beacon/web-01/service", slashTag)
	}
	if kind != event.KindBeacon {
		t.Errorf("kind = %q, want %q", kind, event.KindBeacon)
	}
	if got.ev.Tag != slashTag {
		t.Errorf("event Tag = %q, subject-derived tag = %q — consumer would drop as spoofed", got.ev.Tag, slashTag)
	}
	if got.ev.ID == "" {
		t.Error("event ID empty")
	}
	if got.ev.V != proto.ProtocolVersion {
		t.Errorf("event V = %d, want %d", got.ev.V, proto.ProtocolVersion)
	}
	if got.ev.Depth != 0 || got.ev.Origin != "" {
		t.Errorf("organic beacon event has Depth=%d Origin=%q, want 0/empty", got.ev.Depth, got.ev.Origin)
	}
	if got.ev.TS.IsZero() {
		t.Error("event TS is zero")
	}
}

// TestServiceTransitions covers the onchangeonly=true default: first poll
// establishes the baseline silently, steady state emits nothing, and each
// transition emits exactly one event with {service, running, previous}.
func TestServiceTransitions(t *testing.T) {
	ctx := context.Background()
	svc := exectest.NewFakeServiceExec("systemd")
	svc.PreAdd("nginx", true, true)
	rec := &recorder{}
	m := newTestManager(t, rec, svc, nil)
	m.UpdateConfig(serviceCfg(true, "nginx"))

	// Baseline poll: no emission.
	m.tick(ctx)
	if len(rec.published) != 0 {
		t.Fatalf("baseline poll emitted %d events, want 0", len(rec.published))
	}

	// Steady state: still nothing.
	m.tick(ctx)
	if len(rec.published) != 0 {
		t.Fatalf("steady-state poll emitted %d events, want 0", len(rec.published))
	}

	// Transition running -> stopped.
	svc.PreAdd("nginx", false, true)
	m.tick(ctx)
	if len(rec.published) != 1 {
		t.Fatalf("transition emitted %d events, want 1", len(rec.published))
	}
	data := rec.published[0].ev.Data
	if data["service"] != "nginx" || data["running"] != false || data["previous"] != true {
		t.Errorf("event data = %v, want {service: nginx, running: false, previous: true}", data)
	}

	// Transition back: stopped -> running.
	svc.PreAdd("nginx", true, true)
	m.tick(ctx)
	if len(rec.published) != 2 {
		t.Fatalf("second transition: published = %d, want 2", len(rec.published))
	}
	data = rec.published[1].ev.Data
	if data["running"] != true || data["previous"] != false {
		t.Errorf("event data = %v, want {running: true, previous: false}", data)
	}
}

// TestOnChangeOnlyFalseEmitsEveryPoll: with onchangeonly disabled every poll
// emits every configured service's state; the very first observation reports
// previous == running (no earlier state exists).
func TestOnChangeOnlyFalseEmitsEveryPoll(t *testing.T) {
	ctx := context.Background()
	svc := exectest.NewFakeServiceExec("systemd")
	svc.PreAdd("nginx", true, true)
	rec := &recorder{}
	m := newTestManager(t, rec, svc, nil)
	m.UpdateConfig(serviceCfg(false, "nginx"))

	m.tick(ctx)
	m.tick(ctx)
	if len(rec.published) != 2 {
		t.Fatalf("published = %d events after 2 polls, want 2", len(rec.published))
	}
	first := rec.published[0].ev.Data
	if first["previous"] != first["running"] {
		t.Errorf("first observation previous = %v, want == running (%v)", first["previous"], first["running"])
	}
	if rec.published[0].ev.ID == rec.published[1].ev.ID {
		t.Error("events share an ID; each emission must mint a fresh KSUID")
	}
}

// TestBusySkip: polls are skipped while BusyFn is true
// (disable_during_state_run); the missed transition is picked up on the next
// non-busy tick.
func TestBusySkip(t *testing.T) {
	ctx := context.Background()
	svc := exectest.NewFakeServiceExec("systemd")
	svc.PreAdd("nginx", true, true)
	rec := &recorder{}
	busy := false
	m := newTestManager(t, rec, svc, &busy)
	m.UpdateConfig(serviceCfg(true, "nginx"))

	m.tick(ctx) // baseline

	svc.PreAdd("nginx", false, true)
	busy = true
	m.tick(ctx)
	if len(rec.published) != 0 {
		t.Fatalf("busy tick emitted %d events, want 0", len(rec.published))
	}

	busy = false
	m.tick(ctx)
	if len(rec.published) != 1 {
		t.Fatalf("post-busy tick emitted %d events, want 1 (missed transition)", len(rec.published))
	}
}

// TestOfflineBufferAndDrain: publish failures buffer events; once publishing
// recovers, the next tick drains the buffer in FIFO order — buffered events
// first, then the current tick's. Busy ticks still drain.
func TestOfflineBufferAndDrain(t *testing.T) {
	ctx := context.Background()
	svc := exectest.NewFakeServiceExec("systemd")
	svc.PreAdd("nginx", true, true)
	rec := &recorder{err: errors.New("nats: connection closed")}
	busy := false
	m := newTestManager(t, rec, svc, &busy)
	m.UpdateConfig(serviceCfg(false, "nginx"))

	m.tick(ctx)
	m.tick(ctx)
	if len(m.buf) != 2 {
		t.Fatalf("buffered = %d, want 2 while offline", len(m.buf))
	}
	firstID, secondID := m.buf[0].ev.ID, m.buf[1].ev.ID

	// Recovery on a BUSY tick: no new poll, but the buffer drains.
	rec.err = nil
	busy = true
	m.tick(ctx)
	if len(rec.published) != 2 || len(m.buf) != 0 {
		t.Fatalf("published=%d buffered=%d after recovery drain, want 2/0", len(rec.published), len(m.buf))
	}
	if rec.published[0].ev.ID != firstID || rec.published[1].ev.ID != secondID {
		t.Error("drain order mismatch: buffered events must publish FIFO")
	}

	// Next non-busy tick polls and publishes directly.
	busy = false
	m.tick(ctx)
	if len(rec.published) != 3 {
		t.Fatalf("published = %d after live tick, want 3", len(rec.published))
	}
}

// TestOfflineBufferOverflowDropsOldest: the bounded buffer drops its oldest
// entry once full.
func TestOfflineBufferOverflowDropsOldest(t *testing.T) {
	ctx := context.Background()
	svc := exectest.NewFakeServiceExec("systemd")
	svc.PreAdd("nginx", true, true)
	rec := &recorder{err: errors.New("offline")}
	m := newTestManager(t, rec, svc, nil)
	m.bufCap = 2
	m.UpdateConfig(serviceCfg(false, "nginx"))

	m.tick(ctx)
	id1 := m.buf[0].ev.ID
	m.tick(ctx)
	id2 := m.buf[1].ev.ID
	m.tick(ctx) // overflows: id1 dropped

	if len(m.buf) != 2 {
		t.Fatalf("buffered = %d, want cap 2", len(m.buf))
	}
	if m.buf[0].ev.ID != id2 {
		t.Errorf("oldest surviving event = %q, want %q (id1 %q must be dropped)", m.buf[0].ev.ID, id2, id1)
	}
}

// TestNilProviderDisabledWithOneWarn: a configured service beacon without a
// ServiceExec provider polls nothing and Warn-logs exactly once.
func TestNilProviderDisabledWithOneWarn(t *testing.T) {
	ctx := context.Background()
	var logBuf bytes.Buffer
	rec := &recorder{}
	m := NewManager(ManagerConfig{
		PeelID:  "web-01",
		Publish: rec.publish,
		Service: nil,
		Logger:  slog.New(slog.NewTextHandler(&logBuf, nil)),
	})
	m.UpdateConfig(serviceCfg(true, "nginx"))

	m.tick(ctx)
	m.tick(ctx)
	m.tick(ctx)

	if len(rec.published) != 0 {
		t.Fatalf("published = %d events with nil provider, want 0", len(rec.published))
	}
	if got := strings.Count(logBuf.String(), "service beacon disabled"); got != 1 {
		t.Errorf("disabled warning logged %d times, want exactly 1\nlogs: %s", got, logBuf.String())
	}
}

// TestConfigHotSwap: UpdateConfig is picked up on the next tick — the new
// service set is polled, stale baselines are pruned (so a re-added service
// re-baselines), and the interval follows the config.
func TestConfigHotSwap(t *testing.T) {
	ctx := context.Background()
	svc := exectest.NewFakeServiceExec("systemd")
	svc.PreAdd("alpha", true, true)
	svc.PreAdd("beta", true, true)
	rec := &recorder{}
	m := newTestManager(t, rec, svc, nil)

	if got := m.interval(); got != DefaultInterval {
		t.Errorf("unconfigured interval = %v, want %v", got, DefaultInterval)
	}

	m.UpdateConfig(serviceCfg(true, "alpha"))
	m.tick(ctx) // baseline for alpha

	// Swap to beta with a new interval.
	m.UpdateConfig(Config{Service: &ServiceConfig{
		Services: []string{"beta"}, Interval: 30 * time.Second, OnChangeOnly: true,
	}})
	if got := m.interval(); got != 30*time.Second {
		t.Errorf("interval = %v, want 30s after hot-swap", got)
	}

	m.tick(ctx) // baseline for beta, prunes alpha
	if len(rec.published) != 0 {
		t.Fatalf("published = %d during baselines, want 0", len(rec.published))
	}
	if _, ok := m.svc.prev["alpha"]; ok {
		t.Error("alpha baseline not pruned after hot-swap")
	}
	if _, ok := m.svc.prev["beta"]; !ok {
		t.Error("beta baseline missing after hot-swap")
	}

	// beta transition now emits.
	svc.PreAdd("beta", false, true)
	m.tick(ctx)
	if len(rec.published) != 1 || rec.published[0].ev.Data["service"] != "beta" {
		t.Fatalf("published = %+v, want one beta transition", rec.published)
	}

	// CurrentConfig returns a defensive copy.
	cc := m.CurrentConfig()
	if cc.Service == nil || cc.Service.Interval != 30*time.Second {
		t.Fatalf("CurrentConfig = %+v, want the swapped config", cc)
	}
	cc.Service.Services[0] = "mutated"
	if m.CurrentConfig().Service.Services[0] != "beta" {
		t.Error("CurrentConfig aliases internal state")
	}

	// Disabling via empty config stops polling.
	m.UpdateConfig(Config{})
	svc.PreAdd("beta", true, true)
	m.tick(ctx)
	if len(rec.published) != 1 {
		t.Errorf("published = %d after disable, want still 1", len(rec.published))
	}
}

// TestOnEventHookCountsGeneratedEvents: the metric hook fires once per
// generated event, including ones that only reach the offline buffer.
func TestOnEventHookCountsGeneratedEvents(t *testing.T) {
	ctx := context.Background()
	svc := exectest.NewFakeServiceExec("systemd")
	svc.PreAdd("nginx", true, true)
	rec := &recorder{err: errors.New("offline")}
	counts := map[string]int{}
	m := NewManager(ManagerConfig{
		PeelID:  "web-01",
		Publish: rec.publish,
		Service: svc,
		OnEvent: func(name string) { counts[name]++ },
		Logger:  discardLogger(),
	})
	m.UpdateConfig(serviceCfg(false, "nginx"))

	m.tick(ctx)
	m.tick(ctx)

	if counts[ServiceBeaconName] != 2 {
		t.Errorf("OnEvent(%q) fired %d times, want 2 (buffered events count)", ServiceBeaconName, counts[ServiceBeaconName])
	}
}

// TestPollErrorKeepsBaseline: an IsRunning error emits nothing and keeps the
// previous baseline, so the eventual successful poll reports the real
// transition.
func TestPollErrorKeepsBaseline(t *testing.T) {
	ctx := context.Background()
	svc := exectest.NewFakeServiceExec("systemd")
	svc.PreAdd("nginx", true, true)
	rec := &recorder{}
	m := newTestManager(t, rec, svc, nil)
	m.UpdateConfig(serviceCfg(true, "nginx"))

	m.tick(ctx) // baseline: running

	// FakeServiceExec has no IsRunning error injection; emulate a provider
	// error with a wrapper.
	failing := &failingServiceExec{FakeServiceExec: svc, err: errors.New("systemctl: dbus timeout")}
	m.svc.svc = failing
	svc.PreAdd("nginx", false, true)
	m.tick(ctx)
	if len(rec.published) != 0 {
		t.Fatalf("poll error emitted %d events, want 0", len(rec.published))
	}

	m.svc.svc = svc // provider recovers
	m.tick(ctx)
	if len(rec.published) != 1 {
		t.Fatalf("post-recovery poll emitted %d events, want 1", len(rec.published))
	}
	if data := rec.published[0].ev.Data; data["previous"] != true || data["running"] != false {
		t.Errorf("event data = %v, want the real running->stopped transition", data)
	}
}

// failingServiceExec wraps FakeServiceExec, failing IsRunning.
type failingServiceExec struct {
	*exectest.FakeServiceExec
	err error
}

func (f *failingServiceExec) IsRunning(context.Context, string) (bool, error) {
	return false, f.err
}

// TestRunLoop drives the real Run loop: events flow at the configured
// interval and cancellation stops the loop.
func TestRunLoop(t *testing.T) {
	svc := exectest.NewFakeServiceExec("systemd")
	svc.PreAdd("nginx", true, true)

	pubCh := make(chan outbound, 16)
	m := NewManager(ManagerConfig{
		PeelID: "web-01",
		Publish: func(subject string, ev event.Event) error {
			select {
			case pubCh <- outbound{subject: subject, ev: ev}:
			default:
			}
			return nil
		},
		Service: svc,
		Logger:  discardLogger(),
	})
	m.UpdateConfig(Config{Service: &ServiceConfig{
		Services: []string{"nginx"}, Interval: 5 * time.Millisecond, OnChangeOnly: false,
	}})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Run(ctx)
	}()

	select {
	case ob := <-pubCh:
		if ob.subject != bus.BeaconSubject("web-01", ServiceBeaconName) {
			t.Errorf("subject = %q", ob.subject)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event published within 5s")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}
