package reactor

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/event"
	"github.com/nirnx/zester/pkg/job"
)

// fakeMsg implements reactorMsg for pipeline tests.
type fakeMsg struct {
	subject string
	payload []byte

	mu         sync.Mutex
	acks       int
	naks       int
	nakDelays  []time.Duration
	inProgress int

	settled    chan struct{}
	settleOnce sync.Once
}

func newFakeMsg(subject string, payload []byte) *fakeMsg {
	return &fakeMsg{subject: subject, payload: payload, settled: make(chan struct{})}
}

func (m *fakeMsg) Subject() string { return m.subject }
func (m *fakeMsg) Data() []byte    { return m.payload }

func (m *fakeMsg) Ack() error {
	m.mu.Lock()
	m.acks++
	m.mu.Unlock()
	m.settleOnce.Do(func() { close(m.settled) })
	return nil
}

func (m *fakeMsg) NakWithDelay(d time.Duration) error {
	m.mu.Lock()
	m.naks++
	m.nakDelays = append(m.nakDelays, d)
	m.mu.Unlock()
	m.settleOnce.Do(func() { close(m.settled) })
	return nil
}

func (m *fakeMsg) InProgress() error {
	m.mu.Lock()
	m.inProgress++
	m.mu.Unlock()
	return nil
}

func (m *fakeMsg) waitSettled(t *testing.T) {
	t.Helper()
	select {
	case <-m.settled:
	case <-time.After(5 * time.Second):
		t.Fatal("message neither acked nor naked within 5s")
	}
}

func (m *fakeMsg) counts() (acks, naks, inProgress int, delays []time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.acks, m.naks, m.inProgress, append([]time.Duration(nil), m.nakDelays...)
}

// staticRules is a fixed RuleSource.
type staticRules struct{ rs *RuleSet }

func (s staticRules) RuleSet() *RuleSet { return s.rs }

// hookRecorder captures engine metric hooks.
type hookRecorder struct {
	mu        sync.Mutex
	events    []string
	drops     []string
	unmatched int
	reactions []string // "rule/result"
	breaker   []string // "rule=open|closed"
	renders   int
}

func (h *hookRecorder) bind(cfg *Config) {
	cfg.OnEvent = func(kind string) { h.mu.Lock(); h.events = append(h.events, kind); h.mu.Unlock() }
	cfg.OnDrop = func(reason string) { h.mu.Lock(); h.drops = append(h.drops, reason); h.mu.Unlock() }
	cfg.OnUnmatched = func() { h.mu.Lock(); h.unmatched++; h.mu.Unlock() }
	cfg.OnReaction = func(rule, result string) {
		h.mu.Lock()
		h.reactions = append(h.reactions, rule+"/"+result)
		h.mu.Unlock()
	}
	cfg.OnRenderDuration = func(float64) { h.mu.Lock(); h.renders++; h.mu.Unlock() }
	cfg.OnBreakerChange = func(rule string, open bool) {
		state := "closed"
		if open {
			state = "open"
		}
		h.mu.Lock()
		h.breaker = append(h.breaker, rule+"="+state)
		h.mu.Unlock()
	}
}

func (h *hookRecorder) snapshot() (events, drops, reactions, breaker []string, unmatched int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.events...), append([]string(nil), h.drops...),
		append([]string(nil), h.reactions...), append([]string(nil), h.breaker...), h.unmatched
}

// mustRuleSet builds a RuleSet from a top document and ref->source map.
func mustRuleSet(t *testing.T, top string, files map[string]string) *RuleSet {
	t.Helper()
	rules, err := ParseTopFile([]byte(top))
	if err != nil {
		t.Fatalf("ParseTopFile: %v", err)
	}
	fm := make(map[string][]byte, len(files))
	for ref, src := range files {
		fm[RefPath(ref)] = []byte(src)
	}
	return &RuleSet{Rules: rules, Files: fm}
}

func encodeEvent(t *testing.T, ev event.Event) []byte {
	t.Helper()
	data, err := bus.Encode(ev)
	if err != nil {
		t.Fatalf("encode event: %v", err)
	}
	return data
}

// testEngine builds an engine over the given rule set, seams, and hooks.
// Workers are NOT started; call start(t, e, ctx) or e.startWorkers directly.
func testEngine(t *testing.T, rs *RuleSet, seams *seamRecorder, hooks *hookRecorder, mutate func(*Config)) *Engine {
	t.Helper()
	logger, _ := testLogger()
	cfg := Config{
		Rules:      staticRules{rs: rs},
		DispatchFn: seams.dispatch,
		ResolveFn:  seams.resolve,
		EnrollFn:   seams.enroll,
		EmitFn:     seams.emit,
		Logger:     logger,
		Workers:    1,
	}
	if hooks != nil {
		hooks.bind(&cfg)
	}
	if mutate != nil {
		mutate(&cfg)
	}
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

func startWorkers(t *testing.T, e *Engine) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e.startWorkers(ctx)
	t.Cleanup(func() {
		cancel()
		e.stopWorkers()
	})
}

const engineTop = `
reactor:
  - '*/myco/deploy/*':
      - reactor.deploy
`

const engineReaction = `
restart-{{ data.get('service', 'unknown') }}:
  dispatch.module:
    - target: {{ event.peel }}
    - function: service.restart
    - args:
        name: {{ data.get('service', 'unknown') }}
`

func deployEventMsg(t *testing.T, peel, service string) (*fakeMsg, event.Event) {
	t.Helper()
	ev := testEvent("myco/deploy/finished", map[string]any{"service": service})
	subject := bus.PeelEventSendSubject(peel, "myco.deploy.finished")
	return newFakeMsg(subject, encodeEvent(t, ev)), ev
}

func TestEngineRequiresSeams(t *testing.T) {
	if _, err := NewEngine(Config{}); err == nil || !strings.Contains(err.Error(), "Rules") {
		t.Errorf("missing Rules: %v", err)
	}
	if _, err := NewEngine(Config{Rules: staticRules{}}); err == nil || !strings.Contains(err.Error(), "DispatchFn") {
		t.Errorf("missing DispatchFn: %v", err)
	}
	seams := &seamRecorder{}
	if _, err := NewEngine(Config{Rules: staticRules{}, DispatchFn: seams.dispatch}); err == nil || !strings.Contains(err.Error(), "ResolveFn") {
		t.Errorf("missing ResolveFn: %v", err)
	}
	e := testEngine(t, &RuleSet{}, seams, nil, nil)
	if err := e.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "Consumer") {
		t.Errorf("Start without Consumer: %v", err)
	}
}

func TestEngineDropsMalformedSubject(t *testing.T) {
	hooks := &hookRecorder{}
	e := testEngine(t, &RuleSet{}, &seamRecorder{}, hooks, nil)

	msg := newFakeMsg("zester.job.jid1.return.web-01", []byte("x"))
	e.handleMsg(msg)
	acks, naks, _, _ := msg.counts()
	if acks != 1 || naks != 0 {
		t.Errorf("acks/naks: %d/%d", acks, naks)
	}
	_, drops, _, _, _ := hooks.snapshot()
	if len(drops) != 1 || drops[0] != DropMalformed {
		t.Errorf("drops: %v", drops)
	}
}

func TestEngineDropsUndecodablePayload(t *testing.T) {
	hooks := &hookRecorder{}
	e := testEngine(t, &RuleSet{}, &seamRecorder{}, hooks, nil)

	msg := newFakeMsg("zester.event.web-01.send.a.b", []byte{0x01}) // fixint, not a map
	e.handleMsg(msg)
	msg.waitSettled(t)
	events, drops, _, _, _ := hooks.snapshot()
	if len(drops) != 1 || drops[0] != DropDecode {
		t.Errorf("drops: %v", drops)
	}
	// The event was counted (parseable subject) before the decode drop.
	if len(events) != 1 || events[0] != OriginKindPeel {
		t.Errorf("events: %v", events)
	}
}

func TestEngineDropsSpoofedPayloadTag(t *testing.T) {
	hooks := &hookRecorder{}
	e := testEngine(t, &RuleSet{}, &seamRecorder{}, hooks, nil)

	ev := testEvent("_master/enroll/pending/enr-1", nil) // payload claims a trusted tag
	msg := newFakeMsg("zester.event.evil.send.a.b", encodeEvent(t, ev))
	e.handleMsg(msg)
	acks, _, _, _ := msg.counts()
	if acks != 1 {
		t.Error("spoof must be acked (dropped), not redelivered")
	}
	_, drops, _, _, _ := hooks.snapshot()
	if len(drops) != 1 || drops[0] != DropSpoof {
		t.Errorf("drops: %v", drops)
	}
}

func TestEngineDropsAtDepthCap(t *testing.T) {
	hooks := &hookRecorder{}
	e := testEngine(t, &RuleSet{}, &seamRecorder{}, hooks, nil) // cap 3

	ev := testEvent("a/b", nil)
	ev.Depth = 3
	msg := newFakeMsg("zester.event.web-01.send.a.b", encodeEvent(t, ev))
	e.handleMsg(msg)
	_, drops, _, _, _ := hooks.snapshot()
	if len(drops) != 1 || drops[0] != DropDepth {
		t.Errorf("drops: %v", drops)
	}
}

func TestEngineRateLimitsPerSource(t *testing.T) {
	clk := newFakeClock()
	hooks := &hookRecorder{}
	e := testEngine(t, &RuleSet{}, &seamRecorder{}, hooks, func(cfg *Config) {
		cfg.Now = clk.Now
	})

	// Burst is 30: the 31st event from the same origin drops; another
	// origin is unaffected.
	for i := 0; i < 31; i++ {
		ev := testEvent("a/b", nil)
		ev.TS = clk.Now()
		msg := newFakeMsg("zester.event.web-01.send.a.b", encodeEvent(t, ev))
		e.handleMsg(msg)
	}
	otherEv := testEvent("a/b", nil)
	otherEv.TS = clk.Now()
	other := newFakeMsg("zester.event.web-02.send.a.b", encodeEvent(t, otherEv))
	e.handleMsg(other)

	_, drops, _, _, unmatched := hooks.snapshot()
	rl := 0
	for _, d := range drops {
		if d == DropRatelimit {
			rl++
		}
	}
	if rl != 1 {
		t.Errorf("ratelimit drops: %d (%v)", rl, drops)
	}
	if unmatched != 31 { // 30 from web-01 + 1 from web-02
		t.Errorf("unmatched: %d", unmatched)
	}
}

func TestEngineDropsStaleEventsWithWarn(t *testing.T) {
	clk := newFakeClock()
	hooks := &hookRecorder{}
	logger, buf := testLogger()
	e := testEngine(t, &RuleSet{}, &seamRecorder{}, hooks, func(cfg *Config) {
		cfg.Now = clk.Now
		cfg.Logger = logger
	})

	ev := testEvent("disk/alarm", nil)
	ev.TS = clk.Now().Add(-2 * time.Hour) // default max age 1h
	msg := newFakeMsg("zester.event.web-01.send.disk.alarm", encodeEvent(t, ev))
	e.handleMsg(msg)

	_, drops, _, _, _ := hooks.snapshot()
	if len(drops) != 1 || drops[0] != DropStale {
		t.Fatalf("drops: %v", drops)
	}
	logs := buf.String()
	for _, part := range []string{"stale", "disk/alarm", "web-01", "age="} {
		if !strings.Contains(logs, part) {
			t.Errorf("stale Warn missing %q:\n%s", part, logs)
		}
	}

	// Negative MaxEventAge disables the gate entirely.
	e2 := testEngine(t, &RuleSet{}, &seamRecorder{}, &hookRecorder{}, func(cfg *Config) {
		cfg.Now = clk.Now
		cfg.MaxEventAge = -1
	})
	msg2 := newFakeMsg("zester.event.web-01.send.disk.alarm", encodeEvent(t, ev))
	e2.handleMsg(msg2)
	acks, _, _, _ := msg2.counts()
	if acks != 1 {
		t.Error("disabled staleness gate must fall through to unmatched-ack")
	}
}

func TestEngineUnmatchedAck(t *testing.T) {
	hooks := &hookRecorder{}
	e := testEngine(t, mustRuleSet(t, engineTop, map[string]string{"reactor.deploy": engineReaction}), &seamRecorder{}, hooks, nil)

	ev := testEvent("other/tag", nil)
	msg := newFakeMsg("zester.event.web-01.send.other.tag", encodeEvent(t, ev))
	e.handleMsg(msg)
	acks, _, _, _ := msg.counts()
	if acks != 1 {
		t.Error("unmatched event must ack")
	}
	_, _, _, _, unmatched := hooks.snapshot()
	if unmatched != 1 {
		t.Errorf("unmatched: %d", unmatched)
	}
}

func TestEngineMatchedDispatchAndAck(t *testing.T) {
	rs := mustRuleSet(t, engineTop, map[string]string{"reactor.deploy": engineReaction})
	seams := &seamRecorder{resolved: []string{"web-01"}}
	hooks := &hookRecorder{}
	e := testEngine(t, rs, seams, hooks, nil)
	startWorkers(t, e)

	msg, ev := deployEventMsg(t, "web-01", "nginx")
	e.handleMsg(msg)
	msg.waitSettled(t)

	acks, naks, inProgress, _ := msg.counts()
	if acks != 1 || naks != 0 {
		t.Fatalf("acks/naks: %d/%d", acks, naks)
	}
	if inProgress < 1 {
		t.Error("worker must mark the message in-progress")
	}

	seams.mu.Lock()
	defer seams.mu.Unlock()
	if len(seams.jobs) != 1 {
		t.Fatalf("jobs: %d", len(seams.jobs))
	}
	j := seams.jobs[0]
	if want := DeriveJID("web-01", ev.ID, "reactor.deploy", "restart-nginx"); j.JID != want {
		t.Errorf("JID: got %q, want %q", j.JID, want)
	}
	if j.Function != "service.restart" || j.Args["name"] != "nginx" {
		t.Errorf("job: %+v", j)
	}

	_, _, reactions, _, _ := hooks.snapshot()
	if len(reactions) != 1 || reactions[0] != "reactor.deploy/"+ResultDispatched {
		t.Errorf("reactions: %v", reactions)
	}
}

func TestEngineTransientFailureNaks(t *testing.T) {
	rs := mustRuleSet(t, engineTop, map[string]string{"reactor.deploy": engineReaction})
	seams := &seamRecorder{resolveErr: context.DeadlineExceeded}
	e := testEngine(t, rs, seams, &hookRecorder{}, nil)
	startWorkers(t, e)

	msg, _ := deployEventMsg(t, "web-01", "nginx")
	e.handleMsg(msg)
	msg.waitSettled(t)

	acks, naks, _, delays := msg.counts()
	if acks != 0 || naks != 1 {
		t.Fatalf("acks/naks: %d/%d", acks, naks)
	}
	if len(delays) != 1 || delays[0] != transientNakDelay {
		t.Errorf("nak delay: %v, want %s", delays, transientNakDelay)
	}
}

func TestEngineBackpressureNaksWithoutAck(t *testing.T) {
	rs := mustRuleSet(t, engineTop, map[string]string{"reactor.deploy": engineReaction})
	hooks := &hookRecorder{}
	// Workers deliberately NOT started: the queue (cap 2 for 1 worker)
	// fills and the third matched event must NakWithDelay(5s).
	e := testEngine(t, rs, &seamRecorder{resolved: []string{"web-01"}}, hooks, nil)

	var msgs []*fakeMsg
	for i := 0; i < 3; i++ {
		msg, _ := deployEventMsg(t, "web-01", "nginx")
		e.handleMsg(msg)
		msgs = append(msgs, msg)
	}

	acks0, naks0, _, _ := msgs[0].counts()
	acks2, naks2, _, delays2 := msgs[2].counts()
	if acks0 != 0 || naks0 != 0 {
		t.Errorf("queued message must stay unsettled: acks=%d naks=%d", acks0, naks0)
	}
	if acks2 != 0 || naks2 != 1 {
		t.Fatalf("overflow message: acks=%d naks=%d", acks2, naks2)
	}
	if len(delays2) != 1 || delays2[0] != backpressureNakDelay {
		t.Errorf("backpressure delay: %v, want %s", delays2, backpressureNakDelay)
	}
	_, drops, _, _, _ := hooks.snapshot()
	if len(drops) != 1 || drops[0] != DropBackpressure {
		t.Errorf("drops: %v", drops)
	}
}

func TestEngineAckOnlyAfterCompletionWithKeepalive(t *testing.T) {
	rs := mustRuleSet(t, engineTop, map[string]string{"reactor.deploy": engineReaction})
	release := make(chan struct{})
	seams := &seamRecorder{resolved: []string{"web-01"}}
	e := testEngine(t, rs, seams, nil, func(cfg *Config) {
		cfg.DispatchFn = func(ctx context.Context, j *job.Job) error {
			<-release
			return nil
		}
	})
	e.keepAlive = 10 * time.Millisecond
	startWorkers(t, e)

	msg, _ := deployEventMsg(t, "web-01", "nginx")
	e.handleMsg(msg)

	// While the reaction executes: no ack, keepalives flowing.
	time.Sleep(60 * time.Millisecond)
	acks, naks, inProgress, _ := msg.counts()
	if acks != 0 || naks != 0 {
		t.Fatalf("message settled during execution: acks=%d naks=%d", acks, naks)
	}
	if inProgress < 3 {
		t.Errorf("keepalive InProgress calls: %d, want >= 3", inProgress)
	}

	close(release)
	msg.waitSettled(t)
	acks, naks, _, _ = msg.counts()
	if acks != 1 || naks != 0 {
		t.Errorf("after completion: acks=%d naks=%d", acks, naks)
	}
}

func TestEngineRenderPanicRecovered(t *testing.T) {
	rs := mustRuleSet(t, engineTop, map[string]string{"reactor.deploy": engineReaction})
	hooks := &hookRecorder{}
	e := testEngine(t, rs, &seamRecorder{resolved: []string{"web-01"}}, hooks, func(cfg *Config) {
		cfg.FactsFn = func(string) map[string]any { panic("boom") }
	})
	startWorkers(t, e)

	// Two events: both hit the panicking FactsFn; the engine must survive
	// the first and still process (and settle) the second.
	for i := 0; i < 2; i++ {
		msg, _ := deployEventMsg(t, "web-01", "nginx")
		e.handleMsg(msg)
		msg.waitSettled(t)
		acks, naks, _, _ := msg.counts()
		if acks != 1 || naks != 0 {
			t.Fatalf("event %d: acks=%d naks=%d", i, acks, naks)
		}
	}
	_, _, reactions, _, _ := hooks.snapshot()
	if len(reactions) != 2 || reactions[0] != "reactor.deploy/"+ResultRenderError {
		t.Errorf("reactions: %v", reactions)
	}
}

func TestEngineValidationFailureExecutesNothing(t *testing.T) {
	rs := mustRuleSet(t, engineTop, map[string]string{
		"reactor.deploy": "bad:\n  dispatch.module:\n    target: '{{ event.peel }}'\n    function: NOT-VALID\n",
	})
	hooks := &hookRecorder{}
	seams := &seamRecorder{resolved: []string{"web-01"}}
	e := testEngine(t, rs, seams, hooks, nil)
	startWorkers(t, e)

	msg, _ := deployEventMsg(t, "web-01", "nginx")
	e.handleMsg(msg)
	msg.waitSettled(t)

	seams.mu.Lock()
	jobs := len(seams.jobs)
	seams.mu.Unlock()
	if jobs != 0 {
		t.Error("validation failure must execute no actions")
	}
	_, _, reactions, _, _ := hooks.snapshot()
	if len(reactions) != 1 || reactions[0] != "reactor.deploy/"+ResultValidateError {
		t.Errorf("reactions: %v", reactions)
	}
}

func TestEngineThrottleSkipsRepeatFires(t *testing.T) {
	top := `
reactor:
  - '*/myco/deploy/*':
      react: [reactor.deploy]
      throttle: 10s
`
	rs := mustRuleSet(t, top, map[string]string{"reactor.deploy": engineReaction})
	clk := newFakeClock()
	hooks := &hookRecorder{}
	seams := &seamRecorder{resolved: []string{"web-01"}}
	e := testEngine(t, rs, seams, hooks, func(cfg *Config) { cfg.Now = clk.Now })
	startWorkers(t, e)

	send := func() {
		msg, _ := deployEventMsg(t, "web-01", "nginx")
		e.handleMsg(msg)
		msg.waitSettled(t)
	}

	send() // fires
	send() // throttled (same rule+source within 10s)
	clk.Advance(11 * time.Second)
	send() // fires again

	_, _, reactions, _, _ := hooks.snapshot()
	want := []string{
		"reactor.deploy/" + ResultDispatched,
		"reactor.deploy/" + ResultThrottled,
		"reactor.deploy/" + ResultDispatched,
	}
	if strings.Join(reactions, ",") != strings.Join(want, ",") {
		t.Errorf("reactions:\n  got:  %v\n  want: %v", reactions, want)
	}
}

// TestEngineThrottledRuleRetriesAfterTransientFailure pins the two-phase
// throttle: a transiently failed attempt must not consume the (rule,source)
// window, so the Nak redelivery EXECUTES instead of classifying as throttled
// (which would ack the event and permanently lose the reaction).
func TestEngineThrottledRuleRetriesAfterTransientFailure(t *testing.T) {
	top := `
reactor:
  - '*/myco/deploy/*':
      react: [reactor.deploy]
      throttle: 30s
`
	rs := mustRuleSet(t, top, map[string]string{"reactor.deploy": engineReaction})
	clk := newFakeClock()
	hooks := &hookRecorder{}
	seams := &seamRecorder{resolveErr: context.DeadlineExceeded}
	e := testEngine(t, rs, seams, hooks, func(cfg *Config) { cfg.Now = clk.Now })
	startWorkers(t, e)

	// First delivery fails transiently (resolve down) and Naks.
	msg, _ := deployEventMsg(t, "web-01", "nginx")
	e.handleMsg(msg)
	msg.waitSettled(t)
	if acks, naks, _, _ := msg.counts(); acks != 0 || naks != 1 {
		t.Fatalf("first delivery: acks=%d naks=%d", acks, naks)
	}

	// Redelivery of the SAME event ~10s later — well inside the 30s
	// throttle — with the seam healed: the reaction must execute and ack.
	seams.mu.Lock()
	seams.resolveErr = nil
	seams.resolved = []string{"web-01"}
	seams.mu.Unlock()
	clk.Advance(transientNakDelay)
	redelivery := newFakeMsg(msg.subject, msg.payload)
	e.handleMsg(redelivery)
	redelivery.waitSettled(t)
	if acks, naks, _, _ := redelivery.counts(); acks != 1 || naks != 0 {
		t.Fatalf("redelivery: acks=%d naks=%d (throttled instead of retried?)", acks, naks)
	}

	// The COMPLETED fire recorded the window: a fresh event within it is
	// legitimately throttled.
	later, _ := deployEventMsg(t, "web-01", "nginx")
	e.handleMsg(later)
	later.waitSettled(t)

	_, _, reactions, _, _ := hooks.snapshot()
	want := []string{
		"reactor.deploy/" + ResultError,
		"reactor.deploy/" + ResultDispatched,
		"reactor.deploy/" + ResultThrottled,
	}
	if strings.Join(reactions, ",") != strings.Join(want, ",") {
		t.Errorf("reactions:\n  got:  %v\n  want: %v", reactions, want)
	}
}

// TestEngineBreakerIgnoresTransientRedeliveries pins the two-phase breaker:
// redeliveries of a transiently failing event record no fires, so they can
// neither trip the breaker nor be refused by it — the first successful
// attempt executes.
func TestEngineBreakerIgnoresTransientRedeliveries(t *testing.T) {
	rs := mustRuleSet(t, engineTop, map[string]string{"reactor.deploy": engineReaction})
	clk := newFakeClock()
	hooks := &hookRecorder{}
	seams := &seamRecorder{resolved: []string{"web-01"}, dispatchErr: errors.New("nats down")}
	e := testEngine(t, rs, seams, hooks, func(cfg *Config) {
		cfg.Now = clk.Now
		cfg.StormRate = 2 // trips beyond 2 completed fires/min
	})
	startWorkers(t, e)

	// Four failed attempts of the same event: with attempt-counted fires the
	// breaker would already be open (4 > 2).
	msg, _ := deployEventMsg(t, "web-01", "nginx")
	for i := 0; i < 4; i++ {
		redelivery := newFakeMsg(msg.subject, msg.payload)
		e.handleMsg(redelivery)
		redelivery.waitSettled(t)
		if acks, naks, _, _ := redelivery.counts(); acks != 0 || naks != 1 {
			t.Fatalf("attempt %d: acks=%d naks=%d", i, acks, naks)
		}
	}

	// Heal the seam: the next redelivery must execute, not hit an open
	// breaker inflated by the failed attempts.
	seams.mu.Lock()
	seams.dispatchErr = nil
	seams.mu.Unlock()
	final := newFakeMsg(msg.subject, msg.payload)
	e.handleMsg(final)
	final.waitSettled(t)
	if acks, naks, _, _ := final.counts(); acks != 1 || naks != 0 {
		t.Fatalf("healed redelivery: acks=%d naks=%d", acks, naks)
	}

	_, _, reactions, breaker, _ := hooks.snapshot()
	want := []string{
		"reactor.deploy/" + ResultError,
		"reactor.deploy/" + ResultError,
		"reactor.deploy/" + ResultError,
		"reactor.deploy/" + ResultError,
		"reactor.deploy/" + ResultDispatched,
	}
	if strings.Join(reactions, ",") != strings.Join(want, ",") {
		t.Errorf("reactions:\n  got:  %v\n  want: %v", reactions, want)
	}
	if len(breaker) != 0 {
		t.Errorf("failed attempts must not transition the breaker: %v", breaker)
	}
}

// TestEngineChainingRoundTripThroughOwnConsumer feeds an event.send-emitted
// event back through the engine's own consume path, exactly as JetStream
// would deliver it: the derived event must pass the anti-spoof gate (payload
// Tag equal to the subject-derived "reaction/..." tag) and fire the
// _master/reaction/... rule.
func TestEngineChainingRoundTripThroughOwnConsumer(t *testing.T) {
	top := `
reactor:
  - '*/chain/start':
      - reactor.hop
  - '_master/reaction/chain/next':
      - reactor.finish
`
	rs := mustRuleSet(t, top, map[string]string{
		"reactor.hop":    "hop:\n  event.send:\n    tag: chain/next\n    data:\n      n: 2\n",
		"reactor.finish": "react:\n  dispatch.module:\n    - target: 'web-*'\n    - function: test.ping\n",
	})
	hooks := &hookRecorder{}
	seams := &seamRecorder{resolved: []string{"web-01"}}
	e := testEngine(t, rs, seams, hooks, nil)
	startWorkers(t, e)

	ev := testEvent("chain/start", nil)
	first := newFakeMsg("zester.event.web-01.send.chain.start", encodeEvent(t, ev))
	e.handleMsg(first)
	first.waitSettled(t)

	seams.mu.Lock()
	if len(seams.emits) != 1 {
		seams.mu.Unlock()
		t.Fatalf("emits: %d", len(seams.emits))
	}
	em := seams.emits[0]
	seams.mu.Unlock()

	second := newFakeMsg(em.subject, encodeEvent(t, em.ev))
	e.handleMsg(second)
	second.waitSettled(t)
	if acks, naks, _, _ := second.counts(); acks != 1 || naks != 0 {
		t.Fatalf("chained event: acks=%d naks=%d", acks, naks)
	}

	_, drops, reactions, _, _ := hooks.snapshot()
	if len(drops) != 0 {
		t.Fatalf("chained event dropped (anti-spoof/depth gate?): %v", drops)
	}
	want := []string{
		"reactor.hop/" + ResultDispatched,
		"reactor.finish/" + ResultDispatched,
	}
	if strings.Join(reactions, ",") != strings.Join(want, ",") {
		t.Fatalf("reactions:\n  got:  %v\n  want: %v", reactions, want)
	}

	// The chained dispatch is content-addressed from the DERIVED event's
	// identity: _master origin, deterministic chain ID.
	seams.mu.Lock()
	defer seams.mu.Unlock()
	if len(seams.jobs) != 1 {
		t.Fatalf("jobs: %d", len(seams.jobs))
	}
	j := seams.jobs[0]
	if want := DeriveJID(bus.OriginMaster, em.ev.ID, "reactor.finish", "react"); j.JID != want {
		t.Errorf("chained JID: got %q, want %q", j.JID, want)
	}
	if depth := j.Metadata[job.MetadataReactorDepth]; depth != "2" {
		t.Errorf("chained job depth: got %q, want 2 (derived event depth 1 + 1)", depth)
	}
}

// TestEngineOpenBreakersAndSweep covers the readiness/gauge plumbing: a
// tripped breaker shows in OpenBreakers, and after the cooldown elapses
// SweepBreakers fires the close transition without a new matching event.
func TestEngineOpenBreakersAndSweep(t *testing.T) {
	rs := mustRuleSet(t, engineTop, map[string]string{"reactor.deploy": engineReaction})
	clk := newFakeClock()
	hooks := &hookRecorder{}
	seams := &seamRecorder{resolved: []string{"web-01"}}
	e := testEngine(t, rs, seams, hooks, func(cfg *Config) {
		cfg.Now = clk.Now
		cfg.StormRate = 1
	})
	startWorkers(t, e)

	if got := e.OpenBreakers(); len(got) != 0 {
		t.Fatalf("OpenBreakers before any trip = %v", got)
	}
	for i := 0; i < 2; i++ {
		msg, _ := deployEventMsg(t, "web-01", "nginx")
		e.handleMsg(msg)
		msg.waitSettled(t)
	}
	if got := e.OpenBreakers(); len(got) != 1 || got[0] != "reactor.deploy" {
		t.Fatalf("OpenBreakers = %v, want [reactor.deploy]", got)
	}

	// Cooldown elapses with NO further events: the sweep must close the
	// breaker and fire the gauge transition.
	clk.Advance(DefaultBreakerCooldown + time.Second)
	e.SweepBreakers()
	if got := e.OpenBreakers(); len(got) != 0 {
		t.Fatalf("OpenBreakers after sweep = %v, want empty", got)
	}
	_, _, _, breaker, _ := hooks.snapshot()
	want := []string{"reactor.deploy=open", "reactor.deploy=closed"}
	if strings.Join(breaker, ",") != strings.Join(want, ",") {
		t.Errorf("breaker transitions:\n  got:  %v\n  want: %v", breaker, want)
	}

	// Disabled breaker: both methods are nil-safe no-ops.
	e2 := testEngine(t, rs, seams, nil, func(cfg *Config) { cfg.StormRate = -1 })
	if got := e2.OpenBreakers(); got != nil {
		t.Errorf("disabled breaker OpenBreakers = %v, want nil", got)
	}
	e2.SweepBreakers()
}

func TestEngineStormBreakerOpens(t *testing.T) {
	rs := mustRuleSet(t, engineTop, map[string]string{"reactor.deploy": engineReaction})
	clk := newFakeClock()
	hooks := &hookRecorder{}
	seams := &seamRecorder{resolved: []string{"web-01"}}
	e := testEngine(t, rs, seams, hooks, func(cfg *Config) {
		cfg.Now = clk.Now
		cfg.StormRate = 1 // trips beyond 1 fire/min
	})
	startWorkers(t, e)

	for i := 0; i < 3; i++ {
		msg, _ := deployEventMsg(t, "web-01", "nginx")
		e.handleMsg(msg)
		msg.waitSettled(t)
	}

	_, _, reactions, breaker, _ := hooks.snapshot()
	want := []string{
		"reactor.deploy/" + ResultDispatched,
		"reactor.deploy/" + ResultDispatched, // 2nd fire trips the breaker AFTER executing
		"reactor.deploy/" + ResultBreakerOpen,
	}
	if strings.Join(reactions, ",") != strings.Join(want, ",") {
		t.Errorf("reactions:\n  got:  %v\n  want: %v", reactions, want)
	}
	if len(breaker) != 1 || breaker[0] != "reactor.deploy=open" {
		t.Errorf("breaker transitions: %v", breaker)
	}
}

func TestEngineChainingDisabledRefusesEventSend(t *testing.T) {
	top := "reactor:\n  - '*/chain/*':\n      - reactor.chain\n"
	rs := mustRuleSet(t, top, map[string]string{
		"reactor.chain": "hop:\n  event.send:\n    tag: chain/next\n",
	})
	hooks := &hookRecorder{}
	seams := &seamRecorder{}
	e := testEngine(t, rs, seams, hooks, func(cfg *Config) { cfg.DisableChaining = true })
	startWorkers(t, e)

	ev := testEvent("chain/start", nil)
	msg := newFakeMsg("zester.event.web-01.send.chain.start", encodeEvent(t, ev))
	e.handleMsg(msg)
	msg.waitSettled(t)

	seams.mu.Lock()
	emits := len(seams.emits)
	seams.mu.Unlock()
	if emits != 0 {
		t.Error("chaining disabled must emit nothing")
	}
	_, _, reactions, _, _ := hooks.snapshot()
	if len(reactions) != 1 || reactions[0] != "reactor.chain/"+ResultValidateError {
		t.Errorf("reactions: %v", reactions)
	}
}

func TestEngineChainDepthRefusalThroughPipeline(t *testing.T) {
	top := "reactor:\n  - '*/chain/*':\n      - reactor.chain\n"
	rs := mustRuleSet(t, top, map[string]string{
		"reactor.chain": "hop:\n  event.send:\n    tag: chain/next\n",
	})
	hooks := &hookRecorder{}
	seams := &seamRecorder{}
	e := testEngine(t, rs, seams, hooks, nil) // cap 3
	startWorkers(t, e)

	// Depth 2 passes the consume gate (2 < 3) but the emission (depth 3)
	// is refused by the executor.
	ev := testEvent("chain/start", nil)
	ev.Depth = 2
	ev.Origin = "reaction:reactor.chain"
	msg := newFakeMsg("zester.event.web-01.send.chain.start", encodeEvent(t, ev))
	e.handleMsg(msg)
	msg.waitSettled(t)

	seams.mu.Lock()
	emits := len(seams.emits)
	seams.mu.Unlock()
	if emits != 0 {
		t.Error("emission at the cap must be refused")
	}
	_, _, reactions, _, _ := hooks.snapshot()
	if len(reactions) != 1 || reactions[0] != "reactor.chain/"+ResultRefused {
		t.Errorf("reactions: %v", reactions)
	}
	acks, naks, _, _ := msg.counts()
	if acks != 1 || naks != 0 {
		t.Errorf("refusal is permanent: acks=%d naks=%d", acks, naks)
	}
}

func TestEngineMultipleMatchedRulesAllFire(t *testing.T) {
	top := `
reactor:
  - '*/myco/deploy/*':
      - reactor.deploy
  - '*':
      - reactor.audit
`
	rs := mustRuleSet(t, top, map[string]string{
		"reactor.deploy": engineReaction,
		"reactor.audit":  "note:\n  log:\n    message: saw {{ tag }} from {{ event.origin }}\n",
	})
	hooks := &hookRecorder{}
	seams := &seamRecorder{resolved: []string{"web-01"}}
	logger, buf := testLogger()
	e := testEngine(t, rs, seams, hooks, func(cfg *Config) { cfg.Logger = logger })
	startWorkers(t, e)

	msg, _ := deployEventMsg(t, "web-01", "nginx")
	e.handleMsg(msg)
	msg.waitSettled(t)

	_, _, reactions, _, _ := hooks.snapshot()
	want := []string{
		"reactor.deploy/" + ResultDispatched,
		"reactor.audit/" + ResultDispatched,
	}
	if strings.Join(reactions, ",") != strings.Join(want, ",") {
		t.Errorf("reactions:\n  got:  %v\n  want: %v", reactions, want)
	}
	if !strings.Contains(buf.String(), "saw myco/deploy/finished from web-01") {
		t.Errorf("log action output missing:\n%s", buf.String())
	}
}

func TestEngineOriginKindLabels(t *testing.T) {
	hooks := &hookRecorder{}
	e := testEngine(t, &RuleSet{}, &seamRecorder{}, hooks, nil)

	subjects := []string{
		"zester.event.web-01.send.a.b",     // peel
		"zester.event.web-01.beacon.svc",   // peel
		"zester.event._master.enroll.x",    // master
		"zester.event._admin.send.note.hi", // admin
	}
	for _, s := range subjects {
		var tag string
		_, tag, _, _ = event.ParseSubject(s)
		ev := testEvent(tag, nil)
		e.handleMsg(newFakeMsg(s, encodeEvent(t, ev)))
	}
	events, _, _, _, _ := hooks.snapshot()
	want := []string{OriginKindPeel, OriginKindPeel, OriginKindMaster, OriginKindAdmin}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Errorf("origin kinds:\n  got:  %v\n  want: %v", events, want)
	}
}

// silenceDefault keeps accidental slog.Default usage from polluting test
// output when a test forgets to inject a logger.
func init() {
	slog.SetLogLoggerLevel(slog.LevelError)
}
