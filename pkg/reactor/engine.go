package reactor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/event"
)

// ConsumerName is the shared durable consumer on the "events" stream. All
// masters attach to the same durable, so JetStream delivers each event to
// exactly one master fleet-wide (the schedule-results pattern) and replays
// events published while every master was down.
const ConsumerName = "reactor"

// Engine defaults, applied by NewEngine when the corresponding Config knobs
// are zero.
const (
	DefaultWorkers       = 4
	DefaultMaxChainDepth = 3
	DefaultMaxEventAge   = time.Hour

	defaultRenderTimeout = 30 * time.Second
	defaultKeepAlive     = 15 * time.Second
	defaultAckWait       = 60 * time.Second
	defaultMaxDeliver    = 5
	defaultMaxAckPending = 64
	backpressureNakDelay = 5 * time.Second
	transientNakDelay    = 10 * time.Second
)

// Event drop reason labels (zester_reactor_events_dropped_total{reason}).
const (
	DropMalformed    = "malformed"
	DropDecode       = "decode"
	DropSpoof        = "spoof"
	DropDepth        = "depth"
	DropRatelimit    = "ratelimit"
	DropStale        = "stale"
	DropBackpressure = "backpressure"
)

// Origin kind labels (zester_reactor_events_total{origin_type}).
const (
	OriginKindPeel   = "peel"
	OriginKindMaster = "master"
	OriginKindAdmin  = "admin"
)

// ConsumerCreator is the narrow consumer-creation seam; bus.ConsumerAPI
// satisfies it.
type ConsumerCreator interface {
	CreateOrUpdateConsumer(ctx context.Context, stream string, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error)
}

// reactorMsg is the narrow message surface the pipeline needs;
// jetstream.Msg satisfies it directly, tests use a local fake.
type reactorMsg interface {
	Subject() string
	Data() []byte
	Ack() error
	NakWithDelay(delay time.Duration) error
	InProgress() error
}

// Config configures an Engine. Rules, DispatchFn, and ResolveFn are
// required; Consumer is required only for Start (the test service and the
// pure pipeline work without it). EnrollFn and EmitFn are optional — the
// corresponding actions are refused when absent.
type Config struct {
	// Consumer creates the shared durable consumer on the events stream.
	Consumer ConsumerCreator

	// Rules provides the current rule snapshot (the Loader).
	Rules RuleSource

	// DispatchFn dispatches reaction jobs.
	DispatchFn DispatchFn

	// ResolveFn resolves target expressions to peel IDs.
	ResolveFn ResolveFn

	// EnrollFn performs enrollment transitions (lookup + require_peel gate
	// + transition); nil refuses enroll.* actions.
	EnrollFn EnrollFn

	// EmitFn publishes derived events; nil refuses event.send actions.
	EmitFn EmitFn

	// FactsFn serves origin_facts for reaction rendering; nil renders an
	// empty map.
	FactsFn FactsFn

	// Logger defaults to slog.Default().
	Logger *slog.Logger

	// Workers is the render/execute pool size. Default: 4.
	Workers int

	// MaxChainDepth caps reaction chains: events at or beyond the cap are
	// dropped at consume and event.send refuses to emit at the cap.
	// Default: 3.
	MaxChainDepth int

	// DisableChaining turns off the event.send reaction action. The zero
	// value keeps chaining ENABLED (the design default is
	// reactor.enable_chaining=true).
	DisableChaining bool

	// DefaultThrottle is the per-(rule,source) refractory period applied to
	// rules without their own throttle option. 0 = none.
	DefaultThrottle time.Duration

	// SourceRateLimit is the per-source event rate in events/minute (burst
	// fixed at 30). 0 = default 120; negative disables the limiter.
	SourceRateLimit int

	// MaxEventAge drops events older than this at consume (loud: Warn +
	// reason "stale"). 0 = default 1h; negative disables the staleness
	// gate (full replay after outages).
	MaxEventAge time.Duration

	// StormRate is the per-rule fire rate (fires/minute) that trips the
	// circuit breaker. 0 = default 60; negative disables the breaker.
	StormRate int

	// BreakerCooldown is how long a tripped breaker stays open.
	// 0 = default 5m.
	BreakerCooldown time.Duration

	// Now is the injectable clock (nil = time.Now).
	Now func() time.Time

	// Metric hooks (all optional, nil-safe).
	OnEvent          func(originKind string)
	OnDrop           func(reason string)
	OnUnmatched      func()
	OnReaction       func(rule, result string)
	OnRenderDuration func(seconds float64)
	OnBreakerChange  func(rule string, open bool)
}

// Engine consumes events from the shared durable consumer, matches them
// against the rule set, and renders/executes reactions in a bounded worker
// pool. The consume callback does only cheap work (parse, decode, gates,
// match); rendering and execution happen in workers with hard timeouts and
// InProgress keepalives, so one slow reaction never stalls consumption.
type Engine struct {
	cfg      Config
	logger   *slog.Logger
	renderer *Renderer
	executor *Executor

	workers       int
	maxChainDepth int
	maxEventAge   time.Duration // 0 = disabled
	chaining      bool

	limiter  *SourceLimiter // nil = disabled
	throttle *Throttle
	breaker  *Breaker // nil = disabled

	queue chan work
	quit  chan struct{}
	stop  sync.Once
	wg    sync.WaitGroup

	consumer   jetstream.Consumer
	consumeCtx jetstream.ConsumeContext

	// Test seams (overridden by unit tests only).
	now           func() time.Time
	renderTimeout time.Duration
	keepAlive     time.Duration
}

// work is one matched event handed to the worker pool. The rule set pointer
// pins the snapshot the match ran against, so a concurrent reload cannot
// tear file lookups away from their rules.
type work struct {
	msg     reactorMsg
	ev      event.Event
	origin  string
	matched []Rule
	rules   *RuleSet
}

// NewEngine validates the config, applies defaults, and builds the engine.
func NewEngine(cfg Config) (*Engine, error) {
	if cfg.Rules == nil {
		return nil, fmt.Errorf("reactor: Config.Rules is required")
	}
	if cfg.DispatchFn == nil {
		return nil, fmt.Errorf("reactor: Config.DispatchFn is required")
	}
	if cfg.ResolveFn == nil {
		return nil, fmt.Errorf("reactor: Config.ResolveFn is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	renderer, err := NewRenderer(cfg.FactsFn)
	if err != nil {
		return nil, err
	}

	workers := cfg.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	maxDepth := cfg.MaxChainDepth
	if maxDepth <= 0 {
		maxDepth = DefaultMaxChainDepth
	}
	maxAge := cfg.MaxEventAge
	switch {
	case maxAge == 0:
		maxAge = DefaultMaxEventAge
	case maxAge < 0:
		maxAge = 0 // disabled
	}

	var limiter *SourceLimiter
	if cfg.SourceRateLimit >= 0 {
		limiter = NewSourceLimiter(cfg.SourceRateLimit, DefaultSourceBurst, DefaultMaxSources, now)
	}
	var breaker *Breaker
	if cfg.StormRate >= 0 {
		breaker = NewBreaker(cfg.StormRate, cfg.BreakerCooldown, now, cfg.OnBreakerChange)
	}

	e := &Engine{
		cfg:           cfg,
		logger:        logger,
		renderer:      renderer,
		workers:       workers,
		maxChainDepth: maxDepth,
		maxEventAge:   maxAge,
		chaining:      !cfg.DisableChaining,
		limiter:       limiter,
		throttle:      NewThrottle(DefaultMaxThrottleEntries, now),
		breaker:       breaker,
		queue:         make(chan work, 2*workers),
		quit:          make(chan struct{}),
		now:           now,
		renderTimeout: defaultRenderTimeout,
		keepAlive:     defaultKeepAlive,
	}
	e.executor = &Executor{
		Dispatch:      cfg.DispatchFn,
		Resolve:       cfg.ResolveFn,
		Enroll:        cfg.EnrollFn,
		Emit:          cfg.EmitFn,
		Logger:        logger,
		MaxChainDepth: maxDepth,
		Now:           now,
	}
	return e, nil
}

// ConsumerConfig returns the durable consumer configuration the engine
// creates on the events stream.
func ConsumerConfig() jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Durable:       ConsumerName,
		Description:   "Zester reactor event consumer",
		FilterSubject: bus.EventSubjectAll(),
		DeliverPolicy: jetstream.DeliverNewPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       defaultAckWait,
		MaxDeliver:    defaultMaxDeliver,
		MaxAckPending: defaultMaxAckPending,
	}
}

// Start creates (or joins) the shared durable consumer, starts the worker
// pool, and begins consuming. ctx is the daemon run context: workers use it
// for reaction execution and exit when it is cancelled.
func (e *Engine) Start(ctx context.Context) error {
	if e.cfg.Consumer == nil {
		return fmt.Errorf("reactor: Config.Consumer is required to start the engine")
	}

	cons, err := e.cfg.Consumer.CreateOrUpdateConsumer(ctx, bus.StreamEvents, ConsumerConfig())
	if err != nil {
		return fmt.Errorf("reactor: create %s consumer: %w", ConsumerName, err)
	}
	e.consumer = cons

	e.startWorkers(ctx)

	cctx, err := cons.Consume(func(msg jetstream.Msg) {
		e.handleMsg(msg)
	})
	if err != nil {
		e.stopWorkers()
		return fmt.Errorf("reactor: consume events: %w", err)
	}
	e.consumeCtx = cctx

	e.logger.Info("reactor: engine started", "durable", ConsumerName, "workers", e.workers,
		"max_chain_depth", e.maxChainDepth, "chaining", e.chaining, "max_event_age", e.maxEventAge)
	return nil
}

// Stop halts consumption and waits for in-flight reactions to finish.
func (e *Engine) Stop() {
	if e.consumeCtx != nil {
		e.consumeCtx.Stop()
	}
	e.stopWorkers()
}

// OpenBreakers returns the rules whose storm circuit breakers are currently
// open (expiry-aware, sorted; nil when the breaker is disabled). The masterd
// 'reactor' readiness check reports Degraded while any breaker is open.
func (e *Engine) OpenBreakers() []string {
	if e.breaker == nil {
		return nil
	}
	return e.breaker.OpenRules()
}

// SweepBreakers closes storm breakers whose cooldown has elapsed, firing the
// OnBreakerChange(rule, false) transition for each. The breaker otherwise
// closes only when a NEW event matches the rule, so a stopped event flow
// would leave the zester_reactor_breaker_open gauge (and the degraded
// readiness state) stuck; masterd calls this from its 15s lag-gauge loop.
func (e *Engine) SweepBreakers() {
	if e.breaker != nil {
		e.breaker.Sweep()
	}
}

// NumPending reports the durable consumer's pending event count (the
// zester_reactor_lag gauge source).
func (e *Engine) NumPending(ctx context.Context) (uint64, error) {
	if e.consumer == nil {
		return 0, errors.New("reactor: consumer not started")
	}
	info, err := e.consumer.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("reactor: consumer info: %w", err)
	}
	return info.NumPending, nil
}

func (e *Engine) startWorkers(ctx context.Context) {
	for i := 0; i < e.workers; i++ {
		e.wg.Add(1)
		go e.worker(ctx)
	}
}

func (e *Engine) stopWorkers() {
	e.stop.Do(func() { close(e.quit) })
	e.wg.Wait()
}

// handleMsg is the consume callback: cheap gates only, in the spec-mandated
// order. Permanently invalid events are Ack'd and dropped loudly (metrics);
// only queue overflow Naks (redelivery — never a silent drop).
func (e *Engine) handleMsg(msg reactorMsg) {
	origin, slashTag, kind, err := event.ParseSubject(msg.Subject())
	if err != nil {
		e.drop(msg, DropMalformed)
		e.logger.Warn("reactor: dropping event with malformed subject", "subject", msg.Subject(), "error", err)
		return
	}
	e.onEvent(kindLabel(kind))

	var ev event.Event
	if err := bus.Decode(msg.Data(), &ev); err != nil {
		e.drop(msg, DropDecode)
		e.logger.Warn("reactor: dropping undecodable event", "subject", msg.Subject(), "error", err)
		return
	}

	// The subject is the trust anchor: a payload Tag that disagrees with
	// the subject-derived tag is a spoof attempt (or a broken publisher) —
	// drop it. An empty payload Tag is filled from the subject.
	if ev.Tag != "" && ev.Tag != slashTag {
		e.drop(msg, DropSpoof)
		e.logger.Warn("reactor: dropping event with spoofed payload tag",
			"subject", msg.Subject(), "subject_tag", slashTag, "payload_tag", ev.Tag, "origin", origin)
		return
	}
	ev.Tag = slashTag

	if ev.Depth >= e.maxChainDepth {
		e.drop(msg, DropDepth)
		e.logger.Warn("reactor: dropping event at chain depth cap",
			"tag", slashTag, "origin", origin, "depth", ev.Depth, "max_chain_depth", e.maxChainDepth)
		return
	}

	if e.limiter != nil && !e.limiter.Allow(origin) {
		e.drop(msg, DropRatelimit)
		e.logger.Warn("reactor: dropping event: source rate limit exceeded", "origin", origin, "tag", slashTag)
		return
	}

	if e.maxEventAge > 0 && !ev.TS.IsZero() {
		if age := e.now().Sub(ev.TS); age > e.maxEventAge {
			e.drop(msg, DropStale)
			e.logger.Warn("reactor: dropping stale event (raise reactor.max_event_age or set it to 0 for full replay)",
				"tag", slashTag, "origin", origin, "age", age, "max_event_age", e.maxEventAge)
			return
		}
	}

	rs := e.ruleSet()
	matched := rs.Match(event.MatchKey(origin, slashTag))
	if len(matched) == 0 {
		e.onUnmatched()
		_ = msg.Ack()
		return
	}

	select {
	case e.queue <- work{msg: msg, ev: ev, origin: origin, matched: matched, rules: rs}:
	default:
		// Pipeline saturated: Nak for redelivery (NOT an ack — the event is
		// not lost) and count the backpressure loudly.
		_ = msg.NakWithDelay(backpressureNakDelay)
		e.onDrop(DropBackpressure)
		e.logger.Warn("reactor: worker queue full, delaying event", "tag", slashTag, "origin", origin, "delay", backpressureNakDelay)
	}
}

// worker executes queued reactions until the run context or the engine
// stops.
func (e *Engine) worker(ctx context.Context) {
	defer e.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.quit:
			return
		case w := <-e.queue:
			e.process(ctx, w)
		}
	}
}

// process runs all matched rules for one event, keeping the JetStream
// delivery alive with InProgress heartbeats, then acks (all rules completed
// or permanently failed) or naks (any transient failure — safe to re-run
// because every side effect dedups).
func (e *Engine) process(ctx context.Context, w work) {
	_ = w.msg.InProgress()
	stopKA := make(chan struct{})
	kaDone := make(chan struct{})
	go func() {
		defer close(kaDone)
		ticker := time.NewTicker(e.keepAlive)
		defer ticker.Stop()
		for {
			select {
			case <-stopKA:
				return
			case <-ticker.C:
				_ = w.msg.InProgress()
			}
		}
	}()

	transient := false
	for _, rule := range w.matched {
		// Both guards are two-phase: checked here, recorded only after the
		// rule COMPLETES non-transiently. A transiently failed attempt must
		// consume no guard state, or its Nak redelivery would be refused by
		// the very throttle/breaker window the failed attempt recorded and
		// the reaction permanently lost.
		if e.breaker != nil && !e.breaker.Allow(rule.Ref) {
			e.onReaction(rule.Ref, ResultBreakerOpen)
			continue
		}
		period := rule.Throttle
		if period == 0 {
			period = e.cfg.DefaultThrottle
		}
		if !e.throttle.Allow(rule.Ref, w.origin, period) {
			e.onReaction(rule.Ref, ResultThrottled)
			continue
		}
		if e.processRule(ctx, w, rule) {
			transient = true
			continue
		}
		// Completed fire (success or permanent failure): commit the guards.
		if period > 0 {
			e.throttle.Record(rule.Ref, w.origin)
		}
		if e.breaker != nil {
			e.breaker.RecordFire(rule.Ref)
		}
	}

	close(stopKA)
	<-kaDone

	if transient {
		_ = w.msg.NakWithDelay(transientNakDelay)
		return
	}
	_ = w.msg.Ack()
}

// processRule renders and executes one rule for one event. Returns true when
// any action failed transiently (the message must redeliver).
func (e *Engine) processRule(ctx context.Context, w work, rule Rule) bool {
	src, ok := w.rules.File(rule.Ref)
	if !ok {
		e.logger.Error("reactor: reaction file missing from rule snapshot", "rule", rule.Ref, "file", RefPath(rule.Ref))
		e.onReaction(rule.Ref, ResultRenderError)
		return false
	}

	rctx, cancel := context.WithTimeout(ctx, e.renderTimeout)
	defer cancel()

	start := e.now()
	rr, err := e.renderRule(rctx, rule.Ref, src, w.ev, w.origin)
	e.onRenderDuration(e.now().Sub(start).Seconds())
	if err != nil {
		e.logger.Error("reactor: reaction render failed", "rule", rule.Ref, "event_id", w.ev.ID, "error", err)
		e.onReaction(rule.Ref, ResultRenderError)
		return false
	}
	if len(rr.Errors) > 0 {
		e.logger.Error("reactor: reaction failed post-render validation; no actions executed",
			"rule", rule.Ref, "event_id", w.ev.ID, "errors", rr.Errors)
		e.onReaction(rule.Ref, ResultValidateError)
		return false
	}

	transient := false
	for _, act := range rr.Actions {
		result, err := e.executor.Execute(rctx, w.ev, w.origin, rule.Ref, act)
		e.onReaction(rule.Ref, result)
		if err != nil {
			e.logger.Warn("reactor: reaction action failed transiently, event will redeliver",
				"rule", rule.Ref, "block", act.BlockID, "event_id", w.ev.ID, "error", err)
			transient = true
		}
	}
	return transient
}

// renderRule runs render+normalize in a goroutine so a runaway template
// cannot stall the worker past the render timeout, with panic recovery
// (recovered panics classify as render errors, never crash the engine).
func (e *Engine) renderRule(ctx context.Context, ruleRef string, src []byte, ev event.Event, origin string) (RenderedRule, error) {
	type renderOut struct {
		rr  RenderedRule
		err error
	}
	out := make(chan renderOut, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				out <- renderOut{err: fmt.Errorf("reactor: render %s: panic: %v", ruleRef, r)}
			}
		}()
		rr, err := e.renderer.RenderRule(ruleRef, src, ev, origin, NormalizeOptions{EnableChaining: e.chaining})
		out <- renderOut{rr: rr, err: err}
	}()

	select {
	case o := <-out:
		return o.rr, o.err
	case <-ctx.Done():
		return RenderedRule{}, fmt.Errorf("reactor: render %s: %w", ruleRef, ctx.Err())
	}
}

func (e *Engine) ruleSet() *RuleSet {
	rs := e.cfg.Rules.RuleSet()
	if rs == nil {
		return &RuleSet{}
	}
	return rs
}

func (e *Engine) drop(msg reactorMsg, reason string) {
	_ = msg.Ack()
	e.onDrop(reason)
}

func (e *Engine) onEvent(kind string) {
	if e.cfg.OnEvent != nil {
		e.cfg.OnEvent(kind)
	}
}

func (e *Engine) onDrop(reason string) {
	if e.cfg.OnDrop != nil {
		e.cfg.OnDrop(reason)
	}
}

func (e *Engine) onUnmatched() {
	if e.cfg.OnUnmatched != nil {
		e.cfg.OnUnmatched()
	}
}

func (e *Engine) onReaction(rule, result string) {
	if e.cfg.OnReaction != nil {
		e.cfg.OnReaction(rule, result)
	}
}

func (e *Engine) onRenderDuration(seconds float64) {
	if e.cfg.OnRenderDuration != nil {
		e.cfg.OnRenderDuration(seconds)
	}
}

// kindLabel maps a subject kind to the origin_type metric label.
func kindLabel(k event.Kind) string {
	switch k {
	case event.KindMaster:
		return OriginKindMaster
	case event.KindAdmin:
		return OriginKindAdmin
	default:
		return OriginKindPeel
	}
}
