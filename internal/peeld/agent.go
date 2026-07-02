// Package peeld implements the zester-peel daemon runtime. It was extracted
// verbatim from cmd/zester-peel's former run() function (architecture review
// finding 38): the Agent struct owns the state that used to live in run()'s
// closures, and each former closure is a named method so the peel's business
// logic (module dispatch, exec handling, settings hot-reload) is unit-testable
// without Docker.
//
// Startup is offline-first (roadmap C3 / finding 5) and split in two phases:
//
//   - LOCAL phase (Run body): health server, enrollment, NATS client creation
//     (non-blocking — bus.NewClient retries in the background), local fact
//     collection, provider detection, states engine from the on-disk cache or
//     baked fallback, settings warm-start from the last-known-good snapshot,
//     scheduler start, and the core-NATS exec/cancel subscriptions (nats.go
//     registers subscriptions while reconnecting and replays them on connect,
//     so these are safe to make before the connection is up). Only genuinely
//     local problems (bad config, no creds and no master URL, unusable local
//     disk state) are fatal here.
//
//   - CONNECTED phase (runConnectedPhase goroutine): everything
//     JetStream-dependent — facts publishing, settings resolver + watchers,
//     state-file cache sync/watch, basket publisher, and the peel heartbeat —
//     starts once the NATS client reports healthy. Failures in this phase are
//     retried or degrade gracefully; they never terminate the daemon.
//
// "zester-peel ready" is logged after the local phase; when NATS is already
// reachable at boot, Run first waits (bounded) for the connected phase so the
// observable startup sequence matches the pre-split daemon.
package peeld

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nkeys"
	"github.com/ptorbus/zester/internal/config"
	"github.com/ptorbus/zester/internal/health"
	"github.com/ptorbus/zester/internal/metrics"
	"github.com/ptorbus/zester/internal/version"
	"github.com/ptorbus/zester/pkg/auth"
	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/enroll"
	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/execmod"
	"github.com/ptorbus/zester/pkg/facts"
	"github.com/ptorbus/zester/pkg/facts/collectors"
	"github.com/ptorbus/zester/pkg/proto"
	"github.com/ptorbus/zester/pkg/schedule"
	"github.com/ptorbus/zester/pkg/settings"
	"github.com/ptorbus/zester/pkg/starmod"
	"github.com/ptorbus/zester/pkg/state"
	"github.com/ptorbus/zester/pkg/template"
)

const authDir = "/data/auth"

// connectedReadyWait bounds how long Run waits for the connected phase before
// logging "zester-peel ready" when NATS is reachable at boot. It preserves
// the pre-offline-first startup sequence in the common case without letting a
// slow control plane (e.g. buckets not initialized yet) delay local readiness
// forever.
const connectedReadyWait = 60 * time.Second

// Agent is the zester-peel daemon runtime. New constructs it from a fully
// resolved config and logger; Run starts every subsystem (see the package
// comment for the two-phase startup order) and blocks until the passed
// context is cancelled.
//
// Mutable fields are assigned during Run's local phase in startup order and
// are stable afterwards unless a comment below says otherwise; the comments
// document which lock (if any) guards each group — do not change the locking
// without reading the execMu comment first.
type Agent struct {
	cfg    *config.PeelConfig
	logger *slog.Logger
	peelID string

	// runCtx is Run's internal lifecycle context, handed to every subsystem
	// (facts manager, watchers, scheduler, connected phase) and captured by
	// handleExecRequest as the base context for executions — formerly run()'s
	// ctx closure capture. It is cancelled by Run's first-registered defer,
	// i.e. only AFTER all other shutdown defers have completed, preserving
	// the pre-extraction ordering (subsystems are stopped via their
	// Stop/cancel functions first; the shared context dies last).
	runCtx context.Context

	// busClient is read by the "nats" readiness check, which is registered
	// up-front against this atomic pointer; it stays nil until the NATS
	// client is created, so /readyz reports 503 from process start until
	// NATS is actually connected (instead of "no checks registered = OK").
	busClient atomic.Pointer[bus.Client]

	checker *health.Checker
	metrics *metrics.Registry

	client    *bus.Client // set once after NATS client creation; stable afterwards
	ps        bus.PubSub  // NATS pub/sub wrapper (exec handler, scheduler, job returns, resolve service)
	credsPath string

	mgr      *facts.Manager
	mctx     *exec.ModuleContext
	registry *state.Registry

	// mctxTemplate is an immutable snapshot of the module context taken right
	// after construction (with RenderTemplate already set) and BEFORE any
	// per-request mutation of mctx. Read-only executions derive per-request
	// contexts from it via WithFactsSettings, so they never observe or race
	// the worker's per-execution writes to mctx.Facts/Settings.
	mctxTemplate *exec.ModuleContext

	// guardRunner evaluates onlyif/unless shell guards on states, using the
	// peel's command provider. Exit code 0 = success.
	guardRunner state.GuardRunner

	// execReg holds imperative remote-execution functions (pkg.version,
	// service.restart, disk.usage, ...) dispatched ad-hoc, distinct from the
	// idempotent state modules in `registry`. State modules take precedence
	// where a name exists in both, so ad-hoc state execution is unchanged.
	// The precedence check queries the registry live (not a startup
	// snapshot) so Starlark state modules loaded later — globally or at
	// compile time — are never shadowed by a same-named execmod function.
	execReg *execmod.Registry

	runner *state.Runner

	// resolver is nil when peel-side settings rendering is disabled (a
	// permanent configuration state, not a transient failure).
	resolver *settings.Resolver

	// Shared basket_scope: basketScopeMu protects the scope value read by
	// makeBasketFunc and updated whenever settings are (re-)resolved.
	// cachedSettings stores the last successfully resolved settings map
	// (seeded from the on-disk snapshot at boot), used by
	// settings.get/items/keys queries for consistent reads.
	basketScopeMu  sync.RWMutex
	basketScopeVal string
	cachedSettings map[string]any

	// Settings snapshot persistence (offline-first warm start): snapMu guards
	// the last-persisted content hash; settingsSnapshotPath is overridable in
	// tests ("" disables persistence).
	settingsSnapshotPath string
	snapMu               sync.Mutex
	snapHash             [sha256.Size]byte

	// The scheduler is initialized after the exec function is ready; the
	// atomic pointer lets the settings-watcher goroutine (reResolve) safely
	// observe it for hot-reload before/after assignment.
	schedPtr atomic.Pointer[schedule.Runner]

	// execMu serializes all mutating module executions. The shared
	// ModuleContext is mutated per execution (Facts/Settings), and the
	// scheduler, the exec worker, and the settings re-resolver (template
	// rendering can invoke execution modules via salt['mod.func']) all run
	// concurrently — without serialization those would race and
	// cross-contaminate settings between runs. Read-only modules (facts.*
	// except the mutating facts.set, settings.*/pillar.*, test.ping,
	// grains.*, sys.list_functions) deliberately run OUTSIDE this mutex on
	// contexts derived from mctxTemplate (finding 32).
	execMu sync.Mutex

	// execQueue is the bounded queue feeding the single exec worker
	// goroutine; see handler.go. Sized execQueueSize; when full, requests are
	// rejected immediately with execQueueFullMsg instead of piling up in the
	// NATS client's pending buffer.
	execQueue chan execTask

	// States-engine state, mutated only under execMu: execModule switches to
	// the KV cache dir (rebuilding statesEng and starLoader) once the cache
	// has content, because the peel may have booted before state files
	// reached KV. statesEng may be nil (with effectiveStatesDir "") when no
	// states directory existed at boot — a KV-only deployment before its
	// first cache sync (C4); execModule then builds it lazily per execution.
	// bakedStatesDir is the baked-in fallback dir (bakedStatesDirDefault in
	// production, overridable in tests).
	bakedStatesDir     string
	effectiveStatesDir string
	statesEng          *template.Engine
	starLoader         *starmod.Loader

	// dedup tracks the highest epoch seen per JID (persisted, capped) to
	// reject stale and duplicate dispatches — see dedup.go.
	dedup *dedupTracker

	// Job cancel registry: single wildcard subscription for all job
	// cancels, dispatching to per-job cancel functions via map lookup.
	// Avoids per-job subscription churn under high concurrency. Guarded by
	// cancelMu. Entries are registered at enqueue time, so queued (not yet
	// running) jobs are cancelable too.
	cancelMu    sync.Mutex
	cancelFuncs map[string]context.CancelFunc

	// Connected-phase teardown functions, run in reverse order by Run's
	// shutdown defers (mirroring the pre-split defer semantics). Guarded by
	// cleanupMu; cleanupsClosed makes late registrations run immediately.
	cleanupMu      sync.Mutex
	cleanups       []func()
	cleanupsClosed bool
}

// New creates a peel Agent from a fully resolved config (flag/YAML precedence
// already applied by the caller) and a logger already scoped with peel_id.
func New(cfg *config.PeelConfig, logger *slog.Logger) *Agent {
	return &Agent{
		cfg:                  cfg,
		logger:               logger,
		peelID:               cfg.ID,
		cancelFuncs:          make(map[string]context.CancelFunc),
		execQueue:            make(chan execTask, execQueueSize),
		dedup:                newDedupTracker(defaultDedupPath, dedupCapacity, dedupSaveDelay, logger),
		settingsSnapshotPath: defaultSettingsSnapshotPath,
		bakedStatesDir:       bakedStatesDirDefault,
	}
}

// Run starts the peel daemon and blocks until ctx is cancelled (main cancels
// it on SIGINT/SIGTERM). See the package comment for the local/connected
// phase split.
func (a *Agent) Run(ctx context.Context) error {
	// runCtx deliberately derives from Background, not from ctx: the deferred
	// cancel below is registered first, so runCtx is cancelled only after all
	// other shutdown defers have run — preserving the pre-extraction run()
	// ordering where subsystems are stopped via their Stop/cancel functions
	// first and the shared context dies last.
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.runCtx = runCtx

	logger := a.logger
	cfg := a.cfg
	peelID := a.peelID

	// ---------------------------------------------------------------- LOCAL

	// Readiness checker backing GET /readyz. The nats check is registered
	// up-front against an atomic pointer that stays nil until the NATS
	// client is created, so /readyz reports 503 from process start until
	// NATS is actually connected (instead of "no checks registered = OK").
	// Later checks are registered as subsystems come up — Checker.Register
	// is mutex-guarded and safe after the HTTP server has started.
	a.checker = health.New(version.Version, 2*time.Second)
	a.checker.Register("nats", a.natsHealthCheck)

	a.metrics = metrics.NewPeelRegistry()

	stopHealth, err := startLocalHealthServer(logger, "peel", cfg.HealthAddr, a.checker, a.metrics.Handler())
	if err != nil {
		return fmt.Errorf("start local health server: %w", err)
	}
	defer stopHealth()

	// Keep the uptime gauge fresh on a coarse ticker.
	go a.runUptimeGauge(runCtx)

	a.credsPath = filepath.Join(authDir, peelID+".creds")

	// If no credentials exist, run enrollment flow. Enrollment polls on the
	// caller's ctx (not runCtx): a SIGINT/SIGTERM while waiting for approval
	// must still terminate the process promptly. "No creds and no master
	// URL" remains fatal — it is a genuinely local configuration problem.
	if err := a.ensureEnrolled(ctx); err != nil {
		return err
	}

	// Create the NATS client. With RetryConnect an unreachable server is
	// non-fatal: the client keeps retrying in the background and the daemon
	// continues booting offline-first from local state.
	logger.Info("connecting to NATS", "peel", peelID, "url", cfg.NatsURL)
	natsURLs := bus.NormalizeNATSURLs([]string{cfg.NatsURL})
	if err := bus.ValidateTLSNATSURLs(natsURLs); err != nil {
		return fmt.Errorf("rejecting NATS configuration: %w", err)
	}

	natsTLS, err := bus.NATSClientTLS(natsURLs, cfg.NatsCA)
	if err != nil {
		return fmt.Errorf("configure NATS TLS: %w", err)
	}
	client, err := bus.NewClient(bus.ClientConfig{
		URLs:         natsURLs,
		Name:         "zester-peel-" + peelID,
		CredsFile:    a.credsPath,
		TLS:          natsTLS,
		Logger:       logger,
		RetryConnect: true,
		OnReconnect: func() {
			a.metrics.NATSReconnects.Inc()
			a.metrics.PeelConnected.Set(1)
		},
		OnDisconnect: func(error) {
			a.metrics.PeelConnected.Set(0)
		},
		OnSlowConsumer: func() {
			a.metrics.NATSSlowConsumers.Inc()
		},
	})
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
	defer client.Shutdown(runCtx)
	a.client = client
	a.busClient.Store(client)
	// With RetryOnFailedConnect the client may still be connecting in the
	// background here — report the actual state, not an assumed one. The
	// OnReconnect/OnDisconnect hooks keep the gauge current afterwards.
	if client.IsHealthy() {
		a.metrics.PeelConnected.Set(1)
		logger.Info("NATS connected", "peel", peelID, "url", cfg.NatsURL)
	} else {
		a.metrics.PeelConnected.Set(0)
		logger.Warn("NATS not reachable yet, connecting in background", "peel", peelID, "url", cfg.NatsURL)
	}

	// kv readiness check: a cheap JetStream round-trip (bucket-info lookup
	// on the facts bucket) proving the JetStream API answers, not just that
	// the TCP connection is up.
	a.checker.Register("kv", a.kvHealthCheck)

	// Pub/sub wrapper (exec handler, scheduler, job returns, and the basket
	// target-resolution service client). Created before the template engines
	// so makeBasketFunc can route through the master-side resolve service.
	a.ps = bus.NewNATSPubSub(client.Conn())

	// Facts: collect locally now (providers, templates, and the scheduler
	// need real facts even while NATS is down); publishing to KV and the
	// interval collectors start in the connected phase.
	mgr, err := facts.NewManager(facts.ManagerConfig{
		PeelID: peelID,
		JS:     client.JetStream(),
		Collectors: []facts.Collector{
			collectors.OS{},
			collectors.CPU{},
			collectors.Memory{},
			collectors.Disk{},
			collectors.Network{},
			collectors.Custom{},
			collectors.DefaultIP{},
		},
		Logger: logger,
	})
	if err != nil {
		return fmt.Errorf("create facts manager: %w", err)
	}
	a.mgr = mgr

	// Derive the peel's curve public key from its creds seed and publish
	// it as a fact so the master can encrypt settings for this peel.
	creds, err := auth.LoadCredsFile(a.credsPath)
	if err != nil {
		return fmt.Errorf("load creds for curve key: %w", err)
	}
	curveKey, err := auth.CurvePublicKeyFromSeed(creds.Seed)
	if err != nil {
		return fmt.Errorf("derive curve public key: %w", err)
	}
	mgr.SetFact("_curve_public_key", curveKey)
	logger.Info("curve public key derived", "peel", peelID)

	// Static identity facts for fleet visibility and mixed-version rollouts.
	mgr.SetFact("zester_version", version.Version)
	mgr.SetFact("protocol_version", proto.ProtocolVersion)

	if err := mgr.Collect(runCtx); err != nil {
		return fmt.Errorf("collect facts: %w", err)
	}
	defer mgr.Stop()
	logger.Info("facts collection started", "peel", peelID)

	// Detect execution providers based on collected facts.
	providers := exec.DetectProviders(mgr.GetFacts(), logger)
	a.mctx = exec.NewModuleContext(providers, mgr.GetFacts(), nil, logger)

	// Register state modules with injected execution providers.
	a.registry = state.NewRegistry()
	registerStateModules(a.registry, a.mctx)

	logger.Info("registered state modules", "modules", a.registry.Modules())

	// guardRunner evaluates onlyif/unless shell guards on states, using the
	// peel's command provider. Exit code 0 = success.
	a.guardRunner = state.GuardRunnerFunc(a.runGuard)

	a.execReg = execmod.DefaultRegistry()

	// Set up peel-side settings resolver. The engine and resolver only need
	// local inputs; the master curve public key (KV) is loaded in the
	// connected phase via resolver.SetSenderPub.
	eng, err := template.NewEngine(template.EngineConfig{
		BasePath: "/data",
		ModuleFn: a.moduleDispatch,
		BasketFn: makeBasketFunc(client.JetStream(), a.ps, logger, a.basketScope),
	})
	if err != nil {
		return fmt.Errorf("create template engine: %w", err)
	}

	// Derive peel's own curve key pair for decrypting settings secrets.
	var peelEnc *auth.Encryptor
	peelCurveKP, curveErr := deriveCurveKeyPair(creds.Seed)
	if curveErr != nil {
		logger.Warn("failed to derive curve key pair for settings decryption", "error", curveErr)
	} else {
		peelEnc, err = auth.NewEncryptorFromCurveKey(peelCurveKP)
		if err != nil {
			logger.Warn("failed to create peel encryptor", "error", err)
		}
	}

	resolver, err := settings.NewResolver(settings.ResolverConfig{
		PeelID:    peelID,
		JS:        client.JetStream(),
		Engine:    eng,
		Encryptor: peelEnc,
		Logger:    logger,
	})
	if err != nil {
		logger.Warn("failed to create settings resolver, peel-side rendering disabled", "error", err)
	}
	a.resolver = resolver

	// Warm-start settings from the on-disk last-known-good snapshot, so the
	// scheduler, basket_scope, and settings.* queries work before (or
	// without) a live resolve. The connected-phase initial resolve replaces
	// it as soon as NATS is up. No lock needed: the watcher/scheduler
	// goroutines that contend on basketScopeMu have not started yet.
	if resolver != nil {
		snap, snapErr := loadSettingsSnapshot(a.settingsSnapshotPath)
		switch {
		case snapErr != nil:
			logger.Warn("failed to load settings snapshot", "path", a.settingsSnapshotPath, "error", snapErr)
		case snap != nil:
			a.cachedSettings = snap
			a.snapHash = hashSettings(snap)
			if scope, ok := snap["basket_scope"].(string); ok && scope != "" {
				a.basketScopeVal = scope
				logger.Info("basket_scope configured", "scope", scope)
			}
			logger.Info("settings loaded from last-known-good snapshot",
				"path", a.settingsSnapshotPath, "keys", len(snap))
		}
	}

	// States engine from the KV cache dir when it already has content (from a
	// previous boot), otherwise the baked-in fallback. The connected phase
	// syncs fresh files; execModule re-evaluates the directory before every
	// execution. A KV-only deployment may have NO states directory at all on
	// first boot — that is non-fatal (C4, offline-first): setupStatesEngine
	// leaves statesEng nil and execModule builds it lazily once state files
	// appear.
	a.setupStatesEngine()

	// Enable template rendering for state modules (file.managed source templates).
	a.mctx.RenderTemplate = a.renderStateTemplate

	// Freeze the immutable module-context template NOW — after RenderTemplate
	// is set, before anything can mutate mctx per-request. Read-only
	// executions derive their contexts from this snapshot.
	a.mctxTemplate = a.mctx.WithFactsSettings(nil, nil)

	// Create Starlark module loader for custom .star modules.
	a.starLoader = starmod.NewLoader(starmod.LoaderConfig{
		StatesDir:     a.effectiveStatesDir,
		ModuleContext: a.mctx,
		Logger:        logger,
	})

	// Phase 1: load global _modules/ at startup.
	if count, err := a.starLoader.LoadGlobal(a.registry); err != nil {
		logger.Warn("starlark global module loading had errors", "error", err)
	} else if count > 0 {
		logger.Info("loaded global Starlark modules", "count", count)
	}

	a.runner = state.NewRunner(logger)

	// Load the persisted JID dedup state so re-delivered dispatches are
	// rejected across restarts.
	if err := a.dedup.Load(); err != nil {
		logger.Warn("failed to load dedup state, starting empty", "error", err)
	}
	defer func() {
		if err := a.dedup.Flush(); err != nil {
			logger.Debug("dedup state flush on shutdown failed", "error", err)
		}
	}()

	// Connected-phase teardown (watchers, basket publisher, state-file
	// watcher) runs right after sched.Stop — the same relative position the
	// pre-split defers had. Registered before sched's defer so the reverse
	// defer order is: sched.Stop, then cleanups, then mgr.Stop, ...
	defer a.runCleanups()

	// Set up peel-side scheduler.
	sched := schedule.NewRunner(schedule.RunnerConfig{
		PeelID:   peelID,
		ExecFn:   a.schedExec,
		ReturnFn: a.schedReturn,
		Logger:   logger,
	})
	a.schedPtr.Store(sched)

	// Load schedule from peel.yaml.
	if len(cfg.Schedule) > 0 {
		entries, errs := schedule.Parse(scheduleRawEntries(cfg.Schedule), schedule.SourceConfig)
		for _, e := range errs {
			logger.Warn("schedule config parse error", "error", e)
		}
		if len(entries) > 0 {
			sched.Load(entries)
			logger.Info("schedule entries loaded from config", "count", len(entries))
		}
	}

	// Load schedule from cached settings (the snapshot when offline; the
	// connected-phase initial resolve hot-reloads them from live settings).
	if a.cachedSettings != nil {
		entries, errs := schedule.ParseSettings(a.cachedSettings, schedule.SourceSettings)
		for _, e := range errs {
			logger.Warn("schedule settings parse error", "error", e)
		}
		if len(entries) > 0 {
			sched.Load(entries)
			logger.Info("schedule entries loaded from settings", "count", len(entries))
		}
	}

	// Single worker consuming the bounded exec queue (see handler.go).
	go a.runExecWorker(runCtx)

	sched.Start(runCtx)
	defer sched.Stop()

	// Subscribe to module execution requests.
	// Messages arrive via request/reply (CLI direct) or fire-and-forget (job
	// manager). nats.go registers subscriptions while the connection is still
	// (re)connecting and sends them on (re)connect, so these are safe to make
	// in the local phase — commands start flowing the moment NATS is up.

	// Single wildcard subscription for all job cancels: zester.job.*.cancel
	cancelSubject := bus.SubjectJob + ".*." + bus.SubjectJobCancel
	_, err = a.ps.Subscribe(cancelSubject, a.handleJobCancel)
	if err != nil {
		logger.Error("subscribe job cancel wildcard", "error", err)
	}

	_, err = a.ps.Subscribe(bus.CmdSubject(peelID), a.handleExecRequest)
	if err != nil {
		return fmt.Errorf("subscribe exec handler: %w", err)
	}

	// ------------------------------------------------------------ CONNECTED

	connectedDone := make(chan struct{})
	go func() {
		defer close(connectedDone)
		a.runConnectedPhase(runCtx)
	}()

	// When NATS is reachable at boot, wait (bounded) for the connected phase
	// so "zester-peel ready" keeps its pre-split position after the
	// NATS-dependent subsystems have started. Offline, the peel is ready
	// immediately: it enforces from local state and attaches to the control
	// plane whenever the connection arrives.
	if client.IsHealthy() {
		select {
		case <-connectedDone:
		case <-time.After(connectedReadyWait):
			logger.Warn("NATS-dependent subsystems still starting in background", "peel", peelID)
		case <-ctx.Done():
			return nil
		}
	} else {
		logger.Warn("NATS unreachable, starting in offline mode; scheduler and cached state remain active", "peel", peelID)
	}

	logger.Info("zester-peel ready", "peel", peelID)

	// Wait for shutdown (main logs the received signal and cancels ctx).
	<-ctx.Done()

	return nil
}

// natsHealthCheck backs the "nats" readiness check registered before the NATS
// client exists (see the busClient field comment).
func (a *Agent) natsHealthCheck(_ context.Context) health.CheckResult {
	c := a.busClient.Load()
	if c == nil {
		return health.CheckResult{Status: health.StatusDown, Message: "nats client not initialized"}
	}
	if !c.IsHealthy() {
		return health.CheckResult{Status: health.StatusDown, Message: "nats disconnected"}
	}
	return health.CheckResult{Status: health.StatusOK}
}

// kvHealthCheck backs the "kv" readiness check: a cheap JetStream round-trip
// (bucket-info lookup on the facts bucket) proving the JetStream API answers,
// not just that the TCP connection is up.
func (a *Agent) kvHealthCheck(kctx context.Context) health.CheckResult {
	if _, err := bus.GetBucket(kctx, a.client.JetStream(), bus.BucketFacts); err != nil {
		return health.CheckResult{Status: health.StatusDown, Message: err.Error()}
	}
	return health.CheckResult{Status: health.StatusOK}
}

// runUptimeGauge keeps the uptime gauge fresh on a coarse ticker.
func (a *Agent) runUptimeGauge(ctx context.Context) {
	start := time.Now()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.metrics.PeelUptime.Set(time.Since(start).Seconds())
		}
	}
}

// ensureEnrolled runs the enrollment flow when no credentials exist. All
// configured master URLs are tried in order (multi-master failover); the
// single master_url field is kept for back-compat.
func (a *Agent) ensureEnrolled(ctx context.Context) error {
	if enroll.HasCredentials(authDir, a.peelID) {
		return nil
	}
	masterURLs := a.cfg.MasterURLs
	if len(masterURLs) == 0 && a.cfg.MasterURL != "" {
		masterURLs = []string{a.cfg.MasterURL}
	}
	if len(masterURLs) == 0 {
		return fmt.Errorf("no credentials found and no master URL configured (--master-url / --master-urls); cannot enroll")
	}
	a.logger.Info("no credentials found, starting enrollment", "peel", a.peelID, "master_urls", masterURLs)

	kb, err := enroll.LoadOrGenerateKey(authDir, a.peelID)
	if err != nil {
		return fmt.Errorf("load or generate enrollment key: %w", err)
	}

	enrollClient, err := enroll.NewClient(enroll.ClientConfig{
		MasterURLs: masterURLs,
		PeelID:     a.peelID,
		CAFile:     a.cfg.EnrollCA,
		Logger:     a.logger,
	})
	if err != nil {
		return fmt.Errorf("create enrollment client: %w", err)
	}

	result, err := enrollClient.Enroll(ctx, kb)
	if err != nil {
		return fmt.Errorf("enrollment failed: %w", err)
	}

	if _, err := enroll.SaveCredentials(authDir, a.peelID, result.JWT, kb.Seed); err != nil {
		return fmt.Errorf("save enrollment credentials: %w", err)
	}

	a.logger.Info("enrollment complete, credentials saved", "peel", a.peelID, "expires_at", result.ExpiresAt)
	return nil
}

// basketScope returns the current basket_scope setting; it is the scopeFn
// callback handed to makeBasketFunc.
func (a *Agent) basketScope() string {
	a.basketScopeMu.RLock()
	defer a.basketScopeMu.RUnlock()
	return a.basketScopeVal
}

// applyResolvedSettings installs a successfully resolved settings map:
// updates the cached settings and basket scope, persists the last-known-good
// snapshot, and hot-reloads settings-sourced schedule entries. initial marks
// the first live resolution after startup, which carries the startup-parity
// log lines.
func (a *Agent) applyResolvedSettings(newSettings map[string]any, initial bool) {
	a.basketScopeMu.Lock()
	a.cachedSettings = newSettings
	a.basketScopeVal, _ = newSettings["basket_scope"].(string)
	scope := a.basketScopeVal
	a.basketScopeMu.Unlock()

	if initial && scope != "" {
		a.logger.Info("basket_scope configured", "scope", scope)
	}

	a.persistSettingsSnapshot(newSettings)

	// Hot-reload settings-sourced schedule entries.
	if sched := a.schedPtr.Load(); sched != nil {
		dynEntries, errs := schedule.ParseSettings(newSettings, schedule.SourceSettings)
		for _, e := range errs {
			a.logger.Warn("schedule settings reload error", "error", e)
		}
		sched.Reload(dynEntries, schedule.SourceSettings)
		if initial && len(dynEntries) > 0 {
			a.logger.Info("schedule entries loaded from settings", "count", len(dynEntries))
		}
	}
}

// reResolve re-renders peel-side settings after a settings-files/secrets KV
// change (invoked via the debounced watcher) and hot-reloads settings-sourced
// schedule entries.
func (a *Agent) reResolve() {
	// Resolving renders templates, and rendering can invoke execution
	// modules via salt['mod.func'] (moduleDispatch). Those share the
	// mutable ModuleContext with job/scheduler executions, so this
	// watcher-goroutine path must hold execMu like every other module
	// execution. (The in-exec resolve path in execModule already runs
	// under execMu — do NOT lock inside moduleDispatch itself.)
	a.execMu.Lock()
	defer a.execMu.Unlock()

	currentFacts := a.mgr.GetFacts()
	newSettings, err := a.resolver.Resolve(a.runCtx, currentFacts)
	if err != nil {
		a.logger.Warn("peel-side settings re-resolution failed", "error", err)
		return
	}
	a.applyResolvedSettings(newSettings, false)
}

// addCleanup registers a connected-phase teardown function. If shutdown has
// already executed the registered cleanups, fn runs immediately — the
// connected phase may still be starting subsystems while Run unwinds.
func (a *Agent) addCleanup(fn func()) {
	a.cleanupMu.Lock()
	closed := a.cleanupsClosed
	if !closed {
		a.cleanups = append(a.cleanups, fn)
	}
	a.cleanupMu.Unlock()
	if closed {
		fn()
	}
}

// runCleanups executes connected-phase cleanups in reverse registration
// order, mirroring the defer semantics of the pre-split Run body.
func (a *Agent) runCleanups() {
	a.cleanupMu.Lock()
	fns := a.cleanups
	a.cleanups = nil
	a.cleanupsClosed = true
	a.cleanupMu.Unlock()
	for i := len(fns) - 1; i >= 0; i-- {
		fns[i]()
	}
}

// runGuard backs guardRunner: it runs an onlyif/unless shell guard via the
// peel's command provider and returns the exit code.
func (a *Agent) runGuard(gctx context.Context, cmd string) (int, error) {
	res, err := a.mctx.Command.Run(gctx, exec.CommandOpts{Command: cmd, Shell: true})
	if err != nil {
		return 1, err
	}
	return res.ExitCode, nil
}

// deriveCurveKeyPair derives an X25519 curve key pair from an Ed25519 nkey seed.
func deriveCurveKeyPair(seed []byte) (nkeys.KeyPair, error) {
	_, rawSeed, err := nkeys.DecodeSeed(seed)
	if err != nil {
		return nil, fmt.Errorf("decode seed: %w", err)
	}
	curveSeed, err := nkeys.EncodeSeed(nkeys.PrefixByteCurve, rawSeed)
	if err != nil {
		return nil, fmt.Errorf("encode curve seed: %w", err)
	}
	return nkeys.FromCurveSeed(curveSeed)
}
