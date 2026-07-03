package masterd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/ptorbus/zester/internal/config"
	"github.com/ptorbus/zester/internal/health"
	"github.com/ptorbus/zester/internal/metrics"
	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/bus/bustest"
	"github.com/ptorbus/zester/pkg/enroll"
	"github.com/ptorbus/zester/pkg/event"
	"github.com/ptorbus/zester/pkg/facts"
	"github.com/ptorbus/zester/pkg/reactor"
)

// reactorTestConfig returns a defaults-based master config with the reactor
// enabled and the rules dir pointed at dir.
func reactorTestConfig(dir string) *config.MasterDaemonConfig {
	cfg := config.MasterDaemonDefaults()
	cfg.Reactor.Dir = dir
	return &cfg
}

// checkerChecks runs the readiness handler and returns the per-check results.
func checkerChecks(t *testing.T, checker *health.Checker) map[string]health.CheckResult {
	t.Helper()
	rec := httptest.NewRecorder()
	checker.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	var resp health.Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode readiness response: %v", err)
	}
	return resp.Checks
}

// TestReactorDisabledRegistersNothing verifies that a disabled reactor
// starts no engine subsystem and registers no 'reactor' readiness check —
// while the reactor-files PUBLISHER is still constructed: rule distribution
// is a publisher-lease concern (like the settings/state-files publishers),
// so a reactor-disabled master that wins the lease must still publish the
// rules dir for the enabled masters.
func TestReactorDisabledRegistersNothing(t *testing.T) {
	ctx := context.Background()
	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	cfg := reactorTestConfig(t.TempDir())
	cfg.Reactor.Enabled = false

	d := &Daemon{
		logger:  discardLogger(),
		cfg:     cfg,
		js:      js,
		checker: health.New("test", time.Second),
	}
	d.reactorStart = func(context.Context) (func(), error) {
		t.Fatal("reactorStart must not be called when the reactor is disabled")
		return nil, nil
	}

	stop := d.startReactor(ctx)
	stop()

	if _, ok := checkerChecks(t, d.checker)["reactor"]; ok {
		t.Fatal("disabled reactor must not register the 'reactor' readiness check")
	}

	// The publisher is constructed regardless of the enabled knob.
	if err := d.startReactorPublisher(ctx); err != nil {
		t.Fatalf("startReactorPublisher: %v", err)
	}
	if d.reactorPublisher == nil {
		t.Fatal("reactor-files publisher must be constructed even when the reactor is disabled")
	}
}

// TestReactorRetryFlipsReadiness verifies the sched-consumer boot pattern:
// a boot failure leaves the 'reactor' check Down, the background retry loop
// flips it to OK once the engine starts, and shutdown stops the retried
// engine.
func TestReactorRetryFlipsReadiness(t *testing.T) {
	var calls atomic.Int32
	var stopCalls atomic.Int32

	d := &Daemon{
		logger:               discardLogger(),
		cfg:                  reactorTestConfig(t.TempDir()),
		checker:              health.New("test", time.Second),
		reactorRetryInterval: 10 * time.Millisecond,
	}
	d.reactorStart = func(context.Context) (func(), error) {
		if calls.Add(1) < 3 {
			return nil, errors.New("events stream not ready")
		}
		return func() { stopCalls.Add(1) }, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := d.startReactor(ctx)
	if res := d.reactorCheck(ctx); res.Status != health.StatusDown {
		t.Fatalf("expected Down after boot failure, got %+v", res)
	}
	if _, ok := checkerChecks(t, d.checker)["reactor"]; !ok {
		t.Fatal("enabled reactor must register the 'reactor' readiness check")
	}

	waitFor(t, 2*time.Second, "reactor readiness to flip to ok", func() bool {
		return d.reactorCheck(ctx).Status == health.StatusOK
	})
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 start attempts, got %d", got)
	}

	stop()
	if got := stopCalls.Load(); got != 1 {
		t.Fatalf("expected shutdown to stop the retried engine once, got %d stop calls", got)
	}
}

// TestReactorBootSuccess verifies the happy path: OK immediately, shutdown
// stops the boot-time engine.
func TestReactorBootSuccess(t *testing.T) {
	var stopCalls atomic.Int32
	d := &Daemon{
		logger:  discardLogger(),
		cfg:     reactorTestConfig(t.TempDir()),
		checker: health.New("test", time.Second),
	}
	d.reactorStart = func(context.Context) (func(), error) {
		return func() { stopCalls.Add(1) }, nil
	}

	ctx := context.Background()
	stop := d.startReactor(ctx)
	if res := d.reactorCheck(ctx); res.Status != health.StatusOK {
		t.Fatalf("expected OK after boot success, got %+v", res)
	}
	stop()
	if got := stopCalls.Load(); got != 1 {
		t.Fatalf("expected 1 stop call, got %d", got)
	}
}

// TestSetReactorStopAfterShutdown verifies that a retry success landing
// after shutdown stops its own engine instead of leaking it.
func TestSetReactorStopAfterShutdown(t *testing.T) {
	var stopCalls atomic.Int32
	d := &Daemon{logger: discardLogger()}

	d.shutdownReactor() // shutdown before any engine registered
	if ok := d.setReactorStop(func() { stopCalls.Add(1) }); ok {
		t.Fatal("setReactorStop must report false after shutdown")
	}
	if got := stopCalls.Load(); got != 1 {
		t.Fatalf("late engine must be stopped immediately, got %d stop calls", got)
	}
}

// TestReactorReadinessDegradedOnRuleLoadError verifies the degraded state:
// with the consumer running (state OK), a failed rule load (running on
// last-known-good rules) reports Degraded, and a subsequent successful load
// flips back to OK.
func TestReactorReadinessDegradedOnRuleLoadError(t *testing.T) {
	ctx := context.Background()
	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := bus.GetBucket(ctx, js, bus.BucketReactorFiles)
	if err != nil {
		t.Fatalf("get reactor-files bucket: %v", err)
	}

	// File content without a _manifest is a torn/tampered publish: the load
	// fails and the loader retains the last-known-good (empty) set.
	if _, err := kv.Put(ctx, reactor.TopKey, []byte("reactor: []\n")); err != nil {
		t.Fatalf("put top file: %v", err)
	}

	loader, err := reactor.NewLoader(reactor.LoaderConfig{KV: kv, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new loader: %v", err)
	}
	if err := loader.Load(ctx); err == nil {
		t.Fatal("expected load error for content without a manifest")
	}

	d := &Daemon{logger: discardLogger()}
	d.reactorState.Store(health.CheckResult{Status: health.StatusOK})
	d.reactorLoader.Store(loader)

	if res := d.reactorCheck(ctx); res.Status != health.StatusDegraded {
		t.Fatalf("expected Degraded while running on last-known-good rules, got %+v", res)
	}

	// Removing the torn content makes the bucket empty (pre-first-publish):
	// the reload succeeds with an empty rule set and readiness returns OK.
	if err := kv.Delete(ctx, reactor.TopKey); err != nil {
		t.Fatalf("delete top file: %v", err)
	}
	if err := loader.Load(ctx); err != nil {
		t.Fatalf("reload after cleanup: %v", err)
	}
	if res := d.reactorCheck(ctx); res.Status != health.StatusOK {
		t.Fatalf("expected OK after successful reload, got %+v", res)
	}
}

// TestReactorReadinessDegradedWhileBreakerOpen verifies the breaker half of
// the degraded contract: with the consumer running and rules loading fine,
// an open storm breaker reports Degraded listing the affected rules, and
// readiness returns to OK once the breakers close.
func TestReactorReadinessDegradedWhileBreakerOpen(t *testing.T) {
	ctx := context.Background()
	d := &Daemon{logger: discardLogger()}
	d.reactorState.Store(health.CheckResult{Status: health.StatusOK})

	var open []string
	d.reactorOpenBreakers.Store(func() []string { return open })

	if res := d.reactorCheck(ctx); res.Status != health.StatusOK {
		t.Fatalf("expected OK with no open breakers, got %+v", res)
	}

	open = []string{"reactor.heal_service", "reactor.scale"}
	res := d.reactorCheck(ctx)
	if res.Status != health.StatusDegraded {
		t.Fatalf("expected Degraded while a breaker is open, got %+v", res)
	}
	for _, rule := range open {
		if !strings.Contains(res.Message, rule) {
			t.Errorf("degraded message must list rule %q, got %q", rule, res.Message)
		}
	}

	open = nil
	if res := d.reactorCheck(ctx); res.Status != health.StatusOK {
		t.Fatalf("expected OK after breakers closed, got %+v", res)
	}
}

// TestPublishReactorFilesEmptyDirGuard verifies the rules-publish semantics:
// a populated dir publishes prefixed keys + manifest + revision; an empty or
// missing local dir never wipes a populated bucket; and an empty dir over an
// empty bucket is a clean empty publish.
func TestPublishReactorFilesEmptyDirGuard(t *testing.T) {
	ctx := context.Background()
	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	dir := t.TempDir()
	writeFile := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("top.zy", "reactor:\n  - 'web-*/myco/*':\n      - reactor.restart\n")
	writeFile("restart.zy", "block:\n  log: hello\n")
	writeFile("sub/extra.zy", "block:\n  log: nested\n")
	writeFile("notes.txt", "not a rule file")

	cfg := reactorTestConfig(dir)
	d := &Daemon{logger: discardLogger(), cfg: cfg, js: js}
	if err := d.startReactorPublisher(ctx); err != nil {
		t.Fatalf("startReactorPublisher: %v", err)
	}
	d.publishReactorFiles(ctx)

	for _, key := range []string{"reactor/top.zy", "reactor/restart.zy", "reactor/sub/extra.zy", reactor.KeyManifest, bus.KeyRevision} {
		if !kvHasKey(t, js, bus.BucketReactorFiles, key) {
			t.Fatalf("expected key %s in reactor-files bucket after publish", key)
		}
	}
	if kvHasKey(t, js, bus.BucketReactorFiles, "reactor/notes.txt") {
		t.Fatal("non-.zy files must not be published")
	}

	// The loader accepts the published batch (manifest-verified) and
	// compiles the rules — the publish and load sides agree on the layout.
	kv, err := bus.GetBucket(ctx, js, bus.BucketReactorFiles)
	if err != nil {
		t.Fatalf("get reactor-files bucket: %v", err)
	}
	loader, err := reactor.NewLoader(reactor.LoaderConfig{KV: kv, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new loader: %v", err)
	}
	if err := loader.Load(ctx); err != nil {
		t.Fatalf("loader rejects the published batch: %v", err)
	}
	if got := len(loader.RuleSet().Rules); got != 1 {
		t.Fatalf("expected 1 compiled rule, got %d", got)
	}

	// An empty local dir must NOT wipe the populated bucket.
	cfg.Reactor.Dir = t.TempDir()
	d.publishReactorFiles(ctx)
	if !kvHasKey(t, js, bus.BucketReactorFiles, "reactor/top.zy") {
		t.Fatal("empty local dir wiped a populated reactor-files bucket")
	}

	// A missing local dir must not wipe it either.
	cfg.Reactor.Dir = filepath.Join(t.TempDir(), "does-not-exist")
	d.publishReactorFiles(ctx)
	if !kvHasKey(t, js, bus.BucketReactorFiles, "reactor/top.zy") {
		t.Fatal("missing local dir wiped a populated reactor-files bucket")
	}

	// Empty dir over an EMPTY bucket is a legitimate empty publish.
	js2 := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js2); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	cfg2 := reactorTestConfig(t.TempDir())
	d2 := &Daemon{logger: discardLogger(), cfg: cfg2, js: js2}
	if err := d2.startReactorPublisher(ctx); err != nil {
		t.Fatalf("startReactorPublisher: %v", err)
	}
	d2.publishReactorFiles(ctx)
	if !kvHasKey(t, js2, bus.BucketReactorFiles, reactor.KeyManifest) {
		t.Fatal("empty publish over an empty bucket must still write the manifest")
	}
}

// newReactorEnrollDaemon builds a Daemon with just the enrollment store
// wired, for exercising the reactor EnrollFn directly.
func newReactorEnrollDaemon(t *testing.T) (*Daemon, *enroll.Store) {
	t.Helper()
	ctx := context.Background()
	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	store, err := enroll.NewStore(ctx, enroll.StoreConfig{JS: js, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new enroll store: %v", err)
	}
	return &Daemon{logger: discardLogger(), runCtx: ctx, enrollStore: store}, store
}

// TestReactorEnrollGate verifies the EnrollFn contract: require_peel
// mismatches and impossible transitions are permanent refusals
// (ErrEnrollRefused), already-applied transitions are duplicates
// (ErrEnrollDuplicate), and the happy path transitions with the reactor
// operator identity.
func TestReactorEnrollGate(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		state       enroll.State
		op          string
		requirePeel string
		wantErr     error // nil = success
	}{
		{"approve pending matching glob", enroll.StatePending, "approve", "web-*", nil},
		{"require_peel mismatch refused", enroll.StatePending, "approve", "db-*", reactor.ErrEnrollRefused},
		{"approve already approved is duplicate", enroll.StateApproved, "approve", "web-*", reactor.ErrEnrollDuplicate},
		{"approve progressed to active is duplicate", enroll.StateActive, "approve", "web-*", reactor.ErrEnrollDuplicate},
		{"approve rejected is refused", enroll.StateRejected, "approve", "web-*", reactor.ErrEnrollRefused},
		{"reject pending", enroll.StatePending, "reject", "web-*", nil},
		{"reject already rejected is duplicate", enroll.StateRejected, "reject", "web-*", reactor.ErrEnrollDuplicate},
		{"reject active is refused", enroll.StateActive, "reject", "web-*", reactor.ErrEnrollRefused},
		{"revoke active", enroll.StateActive, "revoke", "web-*", nil},
		{"revoke already revoked is duplicate", enroll.StateRevoked, "revoke", "web-*", reactor.ErrEnrollDuplicate},
		{"revoke pending is refused", enroll.StatePending, "revoke", "web-*", reactor.ErrEnrollRefused},
		{"empty require_peel fails closed", enroll.StatePending, "approve", "", reactor.ErrEnrollRefused},
		{"unknown op refused", enroll.StatePending, "promote", "web-*", reactor.ErrEnrollRefused},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, store := newReactorEnrollDaemon(t)
			id := "enr-gate"
			rec := createEnrollment(t, store, id, "web-01", tt.state)

			err := d.reactorEnroll(ctx, tt.op, id, tt.requirePeel, "test reason")
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("case %d: unexpected error: %v", i, err)
				}
				got, gerr := store.Get(ctx, id)
				if gerr != nil {
					t.Fatalf("get record: %v", gerr)
				}
				wantState, _, _ := enrollOpStates(tt.op)
				if got.State != wantState {
					t.Fatalf("state = %s, want %s", got.State, wantState)
				}
				if got.DecidedBy != reactorOperator {
					t.Fatalf("DecidedBy = %q, want %q", got.DecidedBy, reactorOperator)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, tt.wantErr)
			}
			// Refusals and duplicates must never mutate the record.
			got, gerr := store.Get(ctx, id)
			if gerr != nil {
				t.Fatalf("get record: %v", gerr)
			}
			if got.State != rec.State {
				t.Fatalf("state mutated to %s on classified error", got.State)
			}
		})
	}
}

// TestReactorEnrollMissingRecordRefused verifies that a nonexistent
// enrollment ID is a permanent refusal, not a transient redelivery.
func TestReactorEnrollMissingRecordRefused(t *testing.T) {
	d, _ := newReactorEnrollDaemon(t)
	err := d.reactorEnroll(context.Background(), "approve", "enr-nope", "*", "")
	if !errors.Is(err, reactor.ErrEnrollRefused) {
		t.Fatalf("error = %v, want errors.Is(ErrEnrollRefused)", err)
	}
}

// recordingJS wraps FakeJS and records JetStream publishes.
type recordingJS struct {
	*bustest.FakeJS
	mu       sync.Mutex
	subjects []string
	payloads [][]byte
}

func (r *recordingJS) Publish(_ context.Context, subject string, payload []byte, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subjects = append(r.subjects, subject)
	r.payloads = append(r.payloads, payload)
	return &jetstream.PubAck{Stream: bus.StreamEvents}, nil
}

// TestEmitEnrollPendingEvent verifies the enroll pending event: correct
// _master subject, tag matching the subject, and the {id, peel_id, ip}
// payload.
func TestEmitEnrollPendingEvent(t *testing.T) {
	rjs := &recordingJS{FakeJS: bustest.NewFakeJS()}
	d := &Daemon{logger: discardLogger(), runCtx: context.Background(), js: rjs}

	d.emitEnrollPendingEvent(enroll.Record{ID: "enr-1", PeelID: "web-01", RemoteAddr: "10.0.0.9"})

	rjs.mu.Lock()
	defer rjs.mu.Unlock()
	if len(rjs.subjects) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(rjs.subjects))
	}
	wantSubject := "zester.event._master.enroll.pending.enr-1"
	if rjs.subjects[0] != wantSubject {
		t.Fatalf("subject = %q, want %q", rjs.subjects[0], wantSubject)
	}

	var ev event.Event
	if err := bus.Decode(rjs.payloads[0], &ev); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if ev.Tag != "enroll/pending/enr-1" {
		t.Fatalf("tag = %q, want enroll/pending/enr-1", ev.Tag)
	}
	if ev.ID == "" {
		t.Fatal("event ID must be minted")
	}
	if ev.Data["id"] != "enr-1" || ev.Data["peel_id"] != "web-01" || ev.Data["ip"] != "10.0.0.9" {
		t.Fatalf("payload = %+v", ev.Data)
	}

	// The subject-derived identity must agree with the payload (the engine
	// drops mismatches as spoofs).
	origin, slashTag, kind, err := event.ParseSubject(rjs.subjects[0])
	if err != nil {
		t.Fatalf("parse subject: %v", err)
	}
	if origin != bus.OriginMaster || kind != event.KindMaster || slashTag != ev.Tag {
		t.Fatalf("subject identity mismatch: origin=%q kind=%q tag=%q", origin, kind, slashTag)
	}
}

// TestEmitEnrollPendingEventOmitsEmptyIP verifies the "ip if available"
// contract.
func TestEmitEnrollPendingEventOmitsEmptyIP(t *testing.T) {
	rjs := &recordingJS{FakeJS: bustest.NewFakeJS()}
	d := &Daemon{logger: discardLogger(), runCtx: context.Background(), js: rjs}

	d.emitEnrollPendingEvent(enroll.Record{ID: "enr-2", PeelID: "web-02"})

	rjs.mu.Lock()
	defer rjs.mu.Unlock()
	var ev event.Event
	if err := bus.Decode(rjs.payloads[0], &ev); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if _, ok := ev.Data["ip"]; ok {
		t.Fatal("ip must be omitted when the record has no remote address")
	}
}

// TestReactorResolveAndFacts verifies the in-process resolve/facts seams
// over the retained facts index, including the nil-index failure mode and
// the unseeded-index gate: an index that has not completed its initial
// replay must error (transient → the engine Naks and redelivers) rather
// than resolve to an empty peel list that would ack boot-replay reactions
// as no_targets.
func TestReactorResolveAndFacts(t *testing.T) {
	ctx := context.Background()
	d := &Daemon{logger: discardLogger()}

	if _, err := d.reactorResolve(ctx, "web-*"); err == nil {
		t.Fatal("resolve must fail before the facts index is retained")
	}
	if got := d.reactorFacts("web-01"); got == nil || len(got) != 0 {
		t.Fatalf("facts before index must be an empty map, got %v", got)
	}

	idx := facts.NewIndex()
	idx.Update("web-01", facts.Facts{"os": map[string]any{"name": "linux"}})
	idx.Update("db-01", facts.Facts{"os": map[string]any{"name": "linux"}})
	d.factsIndex.Store(idx)

	// Populated but not yet seeded (initial replay incomplete): resolving
	// would serve a partial fleet view — must error, never an empty list.
	if _, err := d.reactorResolve(ctx, "web-*"); err == nil {
		t.Fatal("resolve must fail while the facts index is unseeded")
	}

	idx.MarkSeeded()
	peels, err := d.reactorResolve(ctx, "web-*")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(peels) != 1 || peels[0] != "web-01" {
		t.Fatalf("resolve web-* = %v, want [web-01]", peels)
	}

	f := d.reactorFacts("web-01")
	if _, ok := f["os"]; !ok {
		t.Fatalf("expected os fact, got %v", f)
	}
	if got := d.reactorFacts("nope"); len(got) != 0 {
		t.Fatalf("unknown peel must yield an empty map, got %v", got)
	}
}

// TestReactorBuildEngineGateTranslation pins the config->engine knob
// translation: 0 in the config means "disabled", which pkg/reactor spells as
// a negative value (its own 0 means "package default").
func TestReactorBuildEngineGateTranslation(t *testing.T) {
	if got := gateInt(0); got != -1 {
		t.Fatalf("gateInt(0) = %d, want -1", got)
	}
	if got := gateInt(120); got != 120 {
		t.Fatalf("gateInt(120) = %d, want 120", got)
	}
	if got := gateDuration(0); got != -1 {
		t.Fatalf("gateDuration(0) = %v, want -1", got)
	}
	if got := gateDuration(time.Hour); got != time.Hour {
		t.Fatalf("gateDuration(1h) = %v, want 1h", got)
	}
}

// TestReactorBuildEngine verifies the production engine construction over a
// fake JS that satisfies the ConsumerCreator seam, with all metric hooks
// wired against a real registry.
func TestReactorBuildEngine(t *testing.T) {
	ctx := context.Background()
	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := bus.GetBucket(ctx, js, bus.BucketReactorFiles)
	if err != nil {
		t.Fatalf("get reactor-files bucket: %v", err)
	}
	loader, err := reactor.NewLoader(reactor.LoaderConfig{KV: kv, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new loader: %v", err)
	}

	d := &Daemon{
		logger: discardLogger(),
		cfg:    reactorTestConfig(t.TempDir()),
		js:     &consumerFakeJS{FakeJS: js},
		reg:    metrics.NewMasterRegistry(),
	}
	engine, err := d.buildReactorEngine(loader)
	if err != nil {
		t.Fatalf("buildReactorEngine: %v", err)
	}
	if engine == nil {
		t.Fatal("nil engine")
	}

	// A bare FakeJS does not implement the consumer seam: construction must
	// fail loudly instead of panicking at Start.
	d.js = js
	if _, err := d.buildReactorEngine(loader); err == nil {
		t.Fatal("expected error for a JetStream API without consumer support")
	}
}

// consumerFakeJS adds a stub CreateOrUpdateConsumer to FakeJS so it
// satisfies reactor.ConsumerCreator for construction-only tests.
type consumerFakeJS struct {
	*bustest.FakeJS
}

func (c *consumerFakeJS) CreateOrUpdateConsumer(context.Context, string, jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	return nil, errors.New("not implemented")
}
