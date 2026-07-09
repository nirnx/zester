package masterd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/nirnx/zester/internal/health"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/enroll"
	"github.com/nirnx/zester/pkg/event"
	"github.com/nirnx/zester/pkg/fileserver"
	"github.com/nirnx/zester/pkg/job"
	"github.com/nirnx/zester/pkg/reactor"
	"github.com/nirnx/zester/pkg/statefiles"
	"github.com/nirnx/zester/pkg/target"
)

const (
	// reactorBootRetryInterval paces the background retry loop that keeps
	// trying to start the reactor after a boot failure (the sched-consumer
	// pattern).
	reactorBootRetryInterval = 60 * time.Second

	// reactorLagInterval is how often the zester_reactor_lag gauge polls the
	// shared durable consumer's pending count (same cadence as the
	// connected-peels gauge).
	reactorLagInterval = 15 * time.Second

	// reactorOperator is the operator identity recorded (DecidedBy) on
	// enrollment transitions performed by reactor rules. The executor's audit
	// log additionally carries the rule-qualified "reactor:<rule>" identity.
	reactorOperator = "reactor"
)

// startReactorPublisher creates the reactor-files publisher on
// d.reactorPublisher — REGARDLESS of reactor.enabled, mirroring the
// settings/state-files publishers: rule distribution is a publisher-lease
// concern, and a reactor-disabled master that wins the lease must still
// distribute the rules dir (the enabled knob gates only the engine, loader,
// consumer, test service, and lag gauge). The actual publish of the local
// reactor dir to the reactor-files bucket is leader-only
// (publishReactorFiles, called from runLeaderPublish), mirroring the
// state-files publish exactly.
func (d *Daemon) startReactorPublisher(ctx context.Context) error {
	reactorKV, err := bus.GetBucket(ctx, d.js, bus.BucketReactorFiles)
	if err != nil {
		return fmt.Errorf("get reactor-files bucket: %w", err)
	}
	d.reactorPublisher = statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: d.cfg.Reactor.Dir,
		KV:        reactorKV,
		Logger:    d.logger,
	})
	return nil
}

// publishReactorFiles publishes the local reactor rules dir to the
// reactor-files KV bucket (files -> _manifest -> _revision, statefiles batch
// protocol). Called only by the publisher-lease holder (runLeaderPublish),
// whether or not the local engine is enabled. An absent or empty reactor dir
// publishes an empty set ONLY when the bucket is already empty — the
// publisher refuses to wipe a populated bucket from a missing local dir
// (statefiles AllowEmpty semantics).
func (d *Daemon) publishReactorFiles(ctx context.Context, force bool) fileserver.SetResult {
	if d.reactorPublisher == nil {
		return fileserver.SetResult{Name: "reactor"}
	}
	files, err := loadReactorFiles(d.cfg.Reactor.Dir)
	if err != nil {
		d.logger.Warn("failed to load reactor files", "dir", d.cfg.Reactor.Dir, "error", err)
		return fileserver.SetResult{Name: "reactor", Err: err.Error()}
	}
	publish := d.reactorPublisher.PublishFiles
	if force {
		publish = d.reactorPublisher.PublishFilesForce
	}
	res, err := publish(ctx, files)
	if err != nil {
		d.logger.Warn("failed to publish reactor files", "error", err)
		return fileserver.SetResult{Name: "reactor", Err: err.Error()}
	}
	if res.Changed {
		d.logger.Info("published reactor files", "count", res.Files)
	} else {
		d.logger.Debug("reactor files unchanged, publish skipped", "count", res.Files)
	}
	return fileserver.SetResult{Name: "reactor", Files: res.Files, Changed: res.Changed}
}

// loadReactorFiles walks the local reactor dir and maps every .zy file to
// its reactor-files bucket key: "<dir>/top.zy" -> "reactor/top.zy",
// "<dir>/restart_service.zy" -> "reactor/restart_service.zy" (the key layout
// pkg/reactor's RefPath/TopKey expect). A missing dir yields an empty set —
// PublishFiles then refuses unless the bucket is already empty.
func loadReactorFiles(dir string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	if dir == "" {
		return files, nil
	}
	err := filepath.WalkDir(dir, func(path string, de os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := de.Name()
		if de.IsDir() {
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || filepath.Ext(name) != ".zy" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return fmt.Errorf("rel path for %s: %w", path, err)
		}
		files[reactor.FilePrefix+filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string][]byte), nil
		}
		return nil, fmt.Errorf("walk reactor dir %s: %w", dir, err)
	}
	return files, nil
}

// startReactor starts the reactor engine: the rule loader over the
// reactor-files bucket, the shared durable consumer on the events stream,
// the reactor test service, and the lag gauge. A disabled reactor registers
// nothing. Boot failure is non-fatal: Warn + Down 'reactor' readiness check
// + a background retry loop (retryReactor) that flips the check to OK once
// the engine finally starts — the sched-consumer pattern, including the
// stop/retry race guard. The returned stop function stops whichever engine
// instance is running at shutdown time (no-op when none ever started).
func (d *Daemon) startReactor(ctx context.Context) func() {
	if !d.cfg.Reactor.Enabled {
		d.logger.Info("reactor disabled by config")
		return func() {}
	}
	if d.reactorStart == nil {
		d.reactorStart = d.reactorBoot
	}
	if d.reactorRetryInterval <= 0 {
		d.reactorRetryInterval = reactorBootRetryInterval
	}

	stop, err := d.reactorStart(ctx)
	if err != nil {
		d.logger.Warn("reactor failed to start; events will not trigger reactions until a retry succeeds", "error", err)
		d.reactorState.Store(health.CheckResult{Status: health.StatusDown, Message: err.Error()})
		go d.retryReactor(ctx)
	} else {
		d.reactorState.Store(health.CheckResult{Status: health.StatusOK})
		d.setReactorStop(stop)
	}
	d.checker.Register("reactor", d.reactorCheck)
	return d.shutdownReactor
}

// retryReactor retries starting the reactor until it succeeds or ctx is
// cancelled, flipping the 'reactor' readiness check to OK on success.
func (d *Daemon) retryReactor(ctx context.Context) {
	ticker := time.NewTicker(d.reactorRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		stop, err := d.reactorStart(ctx)
		if err != nil {
			d.logger.Warn("reactor retry failed", "error", err)
			d.reactorState.Store(health.CheckResult{Status: health.StatusDown, Message: err.Error()})
			continue
		}
		if !d.setReactorStop(stop) {
			return // shut down while retrying; the fresh engine was stopped
		}
		d.reactorState.Store(health.CheckResult{Status: health.StatusOK})
		d.logger.Info("reactor started after retry")
		return
	}
}

// setReactorStop records the stop function of the currently running engine.
// It returns false — after stopping the engine — when shutdown has already
// run, so a retry that wins a race against shutdown does not leak a running
// consumer.
func (d *Daemon) setReactorStop(stop func()) bool {
	d.reactorStopMu.Lock()
	if d.reactorStopped {
		d.reactorStopMu.Unlock()
		stop()
		return false
	}
	d.reactorStop = stop
	d.reactorStopMu.Unlock()
	return true
}

// shutdownReactor stops the currently running engine (if any) and marks the
// reactor stopped for good.
func (d *Daemon) shutdownReactor() {
	d.reactorStopMu.Lock()
	stop := d.reactorStop
	d.reactorStop = nil
	d.reactorStopped = true
	d.reactorStopMu.Unlock()
	if stop != nil {
		stop()
	}
}

// reactorCheck is the 'reactor' readiness check: Down until the shared
// durable consumer is running (boot failure is retried every
// reactorBootRetryInterval), Degraded while the rule loader is running on
// last-known-good rules (the last load failed) or while any rule's storm
// circuit breaker is open (that rule's reactions are being shed wholesale),
// OK otherwise. Registered only when the reactor is enabled.
func (d *Daemon) reactorCheck(context.Context) health.CheckResult {
	res, ok := d.reactorState.Load().(health.CheckResult)
	if !ok {
		return health.CheckResult{Status: health.StatusDown, Message: "not started"}
	}
	if res.Status != health.StatusOK {
		return res
	}
	if loader := d.reactorLoader.Load(); loader != nil {
		if err := loader.LastError(); err != nil {
			return health.CheckResult{
				Status:  health.StatusDegraded,
				Message: "running on last-known-good rules: " + err.Error(),
			}
		}
	}
	if fn, ok := d.reactorOpenBreakers.Load().(func() []string); ok && fn != nil {
		if open := fn(); len(open) > 0 {
			return health.CheckResult{
				Status:  health.StatusDegraded,
				Message: "storm breaker open for rules: " + strings.Join(open, ", "),
			}
		}
	}
	return health.CheckResult{Status: health.StatusOK}
}

// reactorBoot is the production reactorStart seam: it builds the rule
// loader once (initial load + _revision watch — the loader always starts
// before the consumer attaches), builds a fresh engine, starts the shared
// durable consumer, the test service, and the lag gauge. Returns the stop
// function for this engine instance.
func (d *Daemon) reactorBoot(ctx context.Context) (func(), error) {
	loader := d.reactorLoader.Load()
	if loader == nil {
		reactorKV, err := bus.GetBucket(ctx, d.js, bus.BucketReactorFiles)
		if err != nil {
			return nil, fmt.Errorf("get reactor-files bucket: %w", err)
		}
		loader, err = reactor.NewLoader(reactor.LoaderConfig{
			KV:            reactorKV,
			Logger:        d.logger,
			OnRulesLoaded: func(n int) { d.reg.ReactorRulesLoaded.Set(float64(n)) },
			OnRuleError:   d.reg.ReactorRuleErrors.Inc,
		})
		if err != nil {
			return nil, fmt.Errorf("create reactor rule loader: %w", err)
		}
		// Synchronous initial load + revision-watch goroutine. Load failures
		// are non-fatal by design (empty/last-known-good rules + degraded
		// readiness), so Start never blocks the boot.
		loader.Start(ctx)
		d.reactorLoader.Store(loader)
	}

	engine, err := d.buildReactorEngine(loader)
	if err != nil {
		return nil, err
	}
	if err := engine.Start(ctx); err != nil {
		return nil, err
	}
	stopTest, err := engine.StartTestService(bus.NewNATSPubSub(d.nc))
	if err != nil {
		engine.Stop()
		return nil, err
	}
	// The readiness check reads the running engine's open-breaker set for
	// the breaker-open degraded state (stored as a func seam so tests can
	// exercise the check without tripping a real breaker).
	d.reactorOpenBreakers.Store((func() []string)(engine.OpenBreakers))
	go d.runReactorLagGauge(ctx, engine)
	return func() {
		stopTest()
		engine.Stop()
	}, nil
}

// buildReactorEngine constructs the reactor engine over the daemon's seams:
// job dispatch bound to the run context, in-process target resolution over
// the retained facts index, enrollment transitions against the enrollment
// store, derived-event emission via deduplicating JetStream publishes, and
// all Prometheus metric hooks.
func (d *Daemon) buildReactorEngine(loader *reactor.Loader) (*reactor.Engine, error) {
	consumer, ok := d.js.(reactor.ConsumerCreator)
	if !ok {
		return nil, fmt.Errorf("reactor: JetStream API %T does not support consumer creation", d.js)
	}
	rc := d.cfg.Reactor
	engine, err := reactor.NewEngine(reactor.Config{
		Consumer:   consumer,
		Rules:      loader,
		DispatchFn: d.reactorDispatch,
		ResolveFn:  d.reactorResolve,
		EnrollFn:   d.reactorEnroll,
		EmitFn:     d.reactorEmit,
		FactsFn:    d.reactorFacts,
		Logger:     d.logger,

		Workers:         rc.Workers,
		MaxChainDepth:   rc.MaxChainDepth,
		DisableChaining: !rc.EnableChaining,
		DefaultThrottle: time.Duration(rc.DefaultThrottle),
		SourceRateLimit: gateInt(rc.SourceRateLimit),
		MaxEventAge:     gateDuration(time.Duration(rc.MaxEventAge)),
		StormRate:       gateInt(rc.StormRate),
		BreakerCooldown: time.Duration(rc.BreakerCooldown),

		OnEvent:          func(originKind string) { d.reg.ReactorEventsTotal.WithLabelValues(originKind).Inc() },
		OnDrop:           func(reason string) { d.reg.ReactorEventsDropped.WithLabelValues(reason).Inc() },
		OnUnmatched:      d.reg.ReactorEventsUnmatched.Inc,
		OnReaction:       func(rule, result string) { d.reg.ReactorReactionsTotal.WithLabelValues(rule, result).Inc() },
		OnRenderDuration: d.reg.ReactorRenderDuration.Observe,
		OnBreakerChange:  d.onReactorBreakerChange,
	})
	if err != nil {
		return nil, fmt.Errorf("create reactor engine: %w", err)
	}
	return engine, nil
}

// gateInt translates the config convention "0 = disabled" into the
// pkg/reactor knob convention "negative = disabled, 0 = package default".
func gateInt(v int) int {
	if v == 0 {
		return -1
	}
	return v
}

// gateDuration is gateInt for duration knobs (reactor.max_event_age: 0 in
// the config means no staleness gate / full replay).
func gateDuration(v time.Duration) time.Duration {
	if v == 0 {
		return -1
	}
	return v
}

// onReactorBreakerChange feeds the zester_reactor_breaker_open gauge.
func (d *Daemon) onReactorBreakerChange(rule string, open bool) {
	v := 0.0
	if open {
		v = 1
	}
	d.reg.ReactorBreakerOpen.WithLabelValues(rule).Set(v)
}

// reactorDispatch is the reactor's DispatchFn: it hands reaction jobs to the
// job manager bound to the daemon lifecycle context — never the per-message
// context — so dispatch watchers survive message acks (exactly how
// handleDispatch uses d.runCtx). Manager.Dispatch wraps JID conflicts with
// job.ErrJIDConflict, which the reactor executor classifies as
// duplicate-suppressed on its exclusive rxn- keyspace.
func (d *Daemon) reactorDispatch(_ context.Context, j *job.Job) error {
	return d.jobMgr.Dispatch(d.runCtx, j)
}

// reactorResolve is the reactor's ResolveFn: in-process target resolution
// (type auto-detected) over the retained facts index — zero NATS hops, the
// exact backend the resolve service serves. It refuses to resolve until the
// index completed its initial replay (facts.Index.Seeded): an unseeded (or
// never-seeding, when WatchIntoIndex failed) index would return an empty
// peel list with NO error, the engine would classify boot-replay reactions
// as no_targets, and ack them — silently lost. The error is transient by
// construction: the engine Naks and the redelivery retries until the index
// is ready.
func (d *Daemon) reactorResolve(ctx context.Context, expr string) ([]string, error) {
	idx := d.factsIndex.Load()
	if idx == nil {
		return nil, fmt.Errorf("reactor: facts index unavailable")
	}
	if !idx.Seeded() {
		return nil, fmt.Errorf("reactor: facts index not yet seeded")
	}
	return target.Resolve(ctx, expr, target.DetectType(expr), target.NewIndexLister(idx))
}

// reactorFacts is the reactor's FactsFn: the emitting peel's facts from the
// retained index for the origin_facts render variable. Never errors —
// unknown peels (and a not-yet-started index) yield an empty map.
func (d *Daemon) reactorFacts(peelID string) map[string]any {
	if idx := d.factsIndex.Load(); idx != nil {
		if f := idx.RawFacts(peelID); f != nil {
			return f
		}
	}
	return map[string]any{}
}

// reactorEmit is the reactor's EmitFn: a JetStream publish carrying the
// deterministic derived-event ID as the message ID, so redelivered emissions
// collapse in-stream within the events stream's duplicate window.
func (d *Daemon) reactorEmit(ctx context.Context, subject string, ev event.Event, msgID string) error {
	data, err := bus.Encode(&ev)
	if err != nil {
		return fmt.Errorf("encode derived event: %w", err)
	}
	if _, err := d.js.Publish(ctx, subject, data, jetstream.WithMsgID(msgID)); err != nil {
		return fmt.Errorf("publish derived event: %w", err)
	}
	return nil
}

// reactorEnroll is the reactor's EnrollFn: lookup, require_peel gate, then
// the state transition against the enrollment store (the same store logic
// the NATS admin service uses). Permanent refusals (missing record,
// require_peel mismatch, invalid transition) wrap reactor.ErrEnrollRefused;
// already-applied transitions wrap reactor.ErrEnrollDuplicate; anything else
// (KV connectivity, CAS races) returns unwrapped so the engine redelivers
// and the next attempt reclassifies.
func (d *Daemon) reactorEnroll(ctx context.Context, op, enrollmentID, requirePeelGlob, reason string) error {
	rec, err := d.enrollStore.Get(ctx, enrollmentID)
	if err != nil {
		if errors.Is(err, bus.ErrKeyNotFound) {
			return fmt.Errorf("enrollment %s not found: %w", enrollmentID, reactor.ErrEnrollRefused)
		}
		return fmt.Errorf("lookup enrollment %s: %w", enrollmentID, err)
	}

	// The require_peel gate runs BEFORE any transition or duplicate
	// classification: an enrollment outside the rule's peel-ID scope is
	// always refused, never acted on.
	if !reactor.MatchRequirePeel(requirePeelGlob, rec.PeelID) {
		return fmt.Errorf("enrollment %s peel %q does not match require_peel %q: %w",
			enrollmentID, rec.PeelID, requirePeelGlob, reactor.ErrEnrollRefused)
	}

	targetState, appliedStates, err := enrollOpStates(op)
	if err != nil {
		return fmt.Errorf("%v: %w", err, reactor.ErrEnrollRefused)
	}
	for _, s := range appliedStates {
		if rec.State == s {
			return fmt.Errorf("enrollment %s already actioned (state %s): %w",
				enrollmentID, rec.State, reactor.ErrEnrollDuplicate)
		}
	}
	if !rec.CanTransitionTo(targetState) {
		return fmt.Errorf("cannot %s enrollment %s in state %s: %w",
			op, enrollmentID, rec.State, reactor.ErrEnrollRefused)
	}

	// Reactor auto-approval must never paper over a first-contact MITM
	// artifact: a trust-mismatched record is refused unconditionally (there
	// is no force in reaction rendering — a human must decide).
	if op == "approve" && rec.TrustMismatch {
		return fmt.Errorf("enrollment %s is trust-mismatched; reactor auto-approval refused: %w",
			enrollmentID, reactor.ErrEnrollRefused)
	}

	switch op {
	case "approve":
		_, err = d.enrollStore.Approve(ctx, enrollmentID, reactorOperator)
	case "reject":
		_, err = d.enrollStore.Reject(ctx, enrollmentID, reactorOperator, reason)
	case "revoke":
		_, err = d.enrollStore.Revoke(ctx, enrollmentID, reactorOperator, reason)
	}
	if err != nil {
		// CAS races and transient KV failures redeliver; the pre-checks
		// above reclassify duplicates/refusals on the next attempt.
		return fmt.Errorf("enroll %s %s: %w", op, enrollmentID, err)
	}
	return nil
}

// enrollOpStates maps an enroll op to its transition target plus the states
// that mean the op was already applied (idempotent replay): an approval that
// progressed to issued/active is still a duplicate approval, not a refusal.
func enrollOpStates(op string) (target enroll.State, applied []enroll.State, err error) {
	switch op {
	case "approve":
		return enroll.StateApproved, []enroll.State{enroll.StateApproved, enroll.StateIssued, enroll.StateActive}, nil
	case "reject":
		return enroll.StateRejected, []enroll.State{enroll.StateRejected}, nil
	case "revoke":
		return enroll.StateRevoked, []enroll.State{enroll.StateRevoked}, nil
	default:
		return "", nil, fmt.Errorf("unknown enroll op %q", op)
	}
}

// runReactorLagGauge feeds the zester_reactor_lag gauge from the shared
// durable consumer's pending count, immediately and then every
// reactorLagInterval until ctx is cancelled (mirroring the connected-peels
// gauge loop). Poll errors keep the last value with a Debug log — a
// consumer-info blip must not zero the reported lag. The same cadence
// piggybacks the storm-breaker expiry sweep: without it, a breaker whose
// rule stops matching events would report open (gauge stuck at 1, readiness
// degraded) until the next matching event instead of closing on cooldown.
func (d *Daemon) runReactorLagGauge(ctx context.Context, engine *reactor.Engine) {
	ticker := time.NewTicker(reactorLagInterval)
	defer ticker.Stop()
	for {
		d.updateReactorLag(ctx, engine)
		engine.SweepBreakers()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Daemon) updateReactorLag(ctx context.Context, engine *reactor.Engine) {
	n, err := engine.NumPending(ctx)
	if err != nil {
		d.logger.Debug("reactor lag poll failed; keeping last gauge value", "error", err)
		return
	}
	d.reg.ReactorLag.Set(float64(n))
}

// emitEnrollPendingEvent publishes zester.event._master.enroll.pending.<id>
// when a NEW enrollment record is created (wired as the enroll handler's
// OnPending hook). Best-effort: failures are Debug-logged — enrollment must
// never fail or slow down because the events stream is unavailable.
func (d *Daemon) emitEnrollPendingEvent(rec enroll.Record) {
	payload := map[string]any{
		"id":      rec.ID,
		"peel_id": rec.PeelID,
	}
	if rec.RemoteAddr != "" {
		payload["ip"] = rec.RemoteAddr
	}
	ev := event.NewEvent("enroll/pending/"+rec.ID, payload)

	data, err := bus.Encode(&ev)
	if err != nil {
		d.logger.Debug("encode enroll pending event failed", "enrollment_id", rec.ID, "error", err)
		return
	}
	subject := bus.MasterEventSubject(event.DottedTag(ev.Tag))
	if _, err := d.js.Publish(d.runCtx, subject, data); err != nil {
		d.logger.Debug("publish enroll pending event failed",
			"enrollment_id", rec.ID, "subject", subject, "error", err)
		return
	}
	d.logger.Debug("enroll pending event published",
		"enrollment_id", rec.ID, "peel_id", rec.PeelID, "subject", subject)
}
