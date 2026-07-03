package peeld

import (
	"context"
	"time"

	"github.com/ptorbus/zester/internal/version"
	"github.com/ptorbus/zester/pkg/basket"
	"github.com/ptorbus/zester/pkg/beacon"
	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/facts"
	"github.com/ptorbus/zester/pkg/proto"
	"github.com/ptorbus/zester/pkg/settings"
	"github.com/ptorbus/zester/pkg/statefiles"
)

// peelHeartbeatInterval is how often the peel writes its liveness record into
// bus.BucketPeelHeartbeat. The bucket TTL is 30s (pkg/bus/kv.go), so a peel
// is considered offline after missing ~3 beats.
const peelHeartbeatInterval = 10 * time.Second

// runConnectedPhase starts every JetStream-dependent subsystem once the NATS
// connection is actually up: facts publishing, master curve key + initial
// settings resolution, basket publisher, settings watchers, state-file cache,
// and the peel heartbeat. It runs on its own goroutine so a peel that boots
// during a control-plane outage still enforces from local state (scheduler,
// cached state files, settings snapshot) — roadmap C3 / finding 5. Every
// failure here is retried or degrades gracefully; nothing in this phase may
// terminate the daemon.
func (a *Agent) runConnectedPhase(ctx context.Context) {
	if !a.waitForNATS(ctx) {
		return
	}
	logger := a.logger

	// Facts: the local phase already collected; publish to KV and start the
	// interval collectors. Retried with backoff — the bucket may not exist
	// yet while the master is still initializing storage.
	if !a.retryUntil(ctx, "start facts publishing", func() error {
		return a.mgr.StartPublishing(ctx)
	}) {
		return
	}
	logger.Info("facts publishing started", "peel", a.peelID)

	if a.resolver != nil {
		a.loadMasterCurvePub(ctx)
		a.initialSettingsResolve(ctx)
	}

	// Start basket publisher if basket_functions are configured in settings.
	a.startBasketPublisher(ctx)

	// Watch for settings-files and secrets changes to re-resolve settings.
	if a.resolver != nil {
		a.startSettingsWatchers(ctx)
	}

	// Initialize state file cache from KV and watch for changes.
	a.startStateFileCache(ctx)

	// Beacon engine (v1: the service beacon, reactor amendment 22).
	a.startBeacons(ctx)

	// Peel presence heartbeat (finding 24 / roadmap B11).
	go a.runHeartbeat(ctx)

	logger.Info("connected-phase subsystems started", "peel", a.peelID)
}

// waitForNATS blocks until the NATS client reports healthy, polling once per
// second (there is no client-side notification for the initial
// RetryOnFailedConnect attempt succeeding, so polling is the reliable
// signal). Returns false when ctx is cancelled first.
func (a *Agent) waitForNATS(ctx context.Context) bool {
	if a.client.IsHealthy() {
		return true
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if a.client.IsHealthy() {
				return true
			}
		}
	}
}

// retryUntil runs fn until it succeeds, with capped exponential backoff
// (1s..30s). NATS-dependent startup steps are never boot-fatal (finding 5):
// what used to kill the daemon now retries until the control plane recovers.
// Returns false when ctx is cancelled before fn succeeds.
func (a *Agent) retryUntil(ctx context.Context, op string, fn func() error) bool {
	backoff := time.Second
	for {
		err := fn()
		if err == nil {
			return true
		}
		a.logger.Warn(op+" failed, retrying", "error", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

// loadMasterCurvePub reads the master's curve public key from the secrets
// bucket and hands it to the resolver for secret decryption. Failures only
// disable secret decryption until the WatchMasterCurvePub watcher delivers
// the key.
func (a *Agent) loadMasterCurvePub(ctx context.Context) {
	secretsKV, err := bus.GetBucket(ctx, a.client.JetStream(), bus.BucketSecrets)
	if err != nil {
		a.logger.Warn("failed to get secrets bucket for master curve key", "error", err)
		return
	}
	entry, err := secretsKV.Get(ctx, settings.MasterCurvePubKey)
	if err != nil {
		a.logger.Warn("master curve public key not found in KV, secret decryption disabled", "error", err)
		return
	}
	a.resolver.SetSenderPub(string(entry.Value()))
	a.logger.Info("loaded master curve public key for decryption")
}

// initialSettingsResolve performs the first live settings resolution after
// connect. It holds execMu because rendering may invoke execution modules via
// salt['mod.func'] on the shared mutable ModuleContext, and the scheduler and
// exec worker are already running by the time this fires. On failure the peel
// keeps whatever the snapshot warm-start seeded (or master-compiled settings
// per request).
func (a *Agent) initialSettingsResolve(ctx context.Context) {
	a.execMu.Lock()
	defer a.execMu.Unlock()

	compiled, err := a.resolver.Resolve(ctx, a.mgr.GetFacts())
	if err != nil {
		a.logger.Warn("initial peel-side settings resolution failed, falling back to master-compiled", "error", err)
		return
	}
	if len(compiled) > 0 {
		a.logger.Info("peel-side settings resolved on startup", "keys", len(compiled))
	}
	a.applyResolvedSettings(compiled, true)
}

// startBasketPublisher starts the basket publisher when basket_functions are
// configured in the currently cached settings (live-resolved when NATS was up
// in time, otherwise the on-disk snapshot).
func (a *Agent) startBasketPublisher(ctx context.Context) {
	a.basketScopeMu.RLock()
	cached := a.cachedSettings
	a.basketScopeMu.RUnlock()

	basketFunctions := basket.ParseFunctions(cached)
	if len(basketFunctions) == 0 {
		return
	}
	basketPub, err := basket.NewPublisher(basket.PublisherConfig{
		PeelID:    a.peelID,
		JS:        a.client.JetStream(),
		Functions: basketFunctions,
		Logger:    a.logger,
	})
	if err != nil {
		a.logger.Warn("failed to create basket publisher", "error", err)
		return
	}
	if err := basketPub.Start(ctx, func() map[string]any { return a.mgr.GetFacts() }); err != nil {
		a.logger.Warn("failed to start basket publisher", "error", err)
		return
	}
	a.addCleanup(basketPub.Stop)
	a.logger.Info("basket publisher started", "functions", len(basketFunctions))
}

// startSettingsWatchers wires the settings-files/secrets/master-curve-key KV
// watchers into the debounced re-resolver.
//
// Debounce + jitter prevents thundering-herd bursts when many peels react to
// the same KV change simultaneously.
//
// Revision gating: the peel only re-resolves when _revision in the
// settings-files bucket changes OR when a forced flag is set (curve key
// rotation). This prevents spurious re-resolves caused by periodic facts
// re-publication → master secrets re-publish → secrets watcher chain, which
// would read partially-written settings files.
func (a *Agent) startSettingsWatchers(ctx context.Context) {
	resolver := a.resolver
	logger := a.logger

	debounced := settings.NewDebouncedFunc(settings.DebouncedFuncConfig{
		Fn:       a.reResolve,
		Debounce: 2 * time.Second,
		Jitter:   5 * time.Second,
	})
	a.addCleanup(debounced.Stop)

	cancelFilesWatch, err := settings.WatchRawFiles(ctx, a.client.JetStream(), debounced.Trigger, logger)
	if err != nil {
		logger.Warn("failed to watch settings-files KV", "error", err)
	} else {
		a.addCleanup(cancelFilesWatch)
	}

	cancelSecretsWatch, err := settings.WatchSecrets(ctx, a.client.JetStream(), a.peelID, func() {
		// Secrets live in a separate bucket from settings files, so a
		// secrets-only change (rotation, late-arriving startup publish)
		// never bumps the settings-files _revision that gates the
		// resolver cache. Invalidate explicitly — mirroring the curve-key
		// path below, which invalidates via SetSenderPub — so the
		// debounced re-resolve re-reads instead of returning the stale
		// cached result.
		resolver.InvalidateCache()
		debounced.Trigger()
	}, logger)
	if err != nil {
		logger.Warn("failed to watch secrets KV", "error", err)
	} else {
		a.addCleanup(cancelSecretsWatch)
	}

	// SetSenderPub only invalidates the cache and triggers re-resolve
	// when the key actually changes, ignoring watcher replays.
	cancelMasterCurveWatch, err := settings.WatchMasterCurvePub(ctx, a.client.JetStream(), func(pub string) {
		if pub == "" || pub == resolver.SenderPub() {
			return
		}
		resolver.SetSenderPub(pub)
		logger.Info("updated master curve public key for decryption")
		debounced.Trigger()
	}, logger)
	if err != nil {
		logger.Warn("failed to watch master curve public key", "error", err)
	} else {
		a.addCleanup(cancelMasterCurveWatch)
	}
}

// startStateFileCache syncs the state-file cache from KV and starts the
// change watcher. Failures fall back to whatever the local disk already has
// (execModule re-evaluates the effective states dir before every execution).
func (a *Agent) startStateFileCache(ctx context.Context) {
	stateCache := statefiles.NewCache(statefiles.CacheConfig{
		CacheDir: a.cfg.StatesCache,
		JS:       a.client.JetStream(),
		Logger:   a.logger,
	})
	if count, err := stateCache.Sync(ctx); err != nil {
		a.logger.Warn("state file cache sync failed, falling back to local states", "error", err)
	} else {
		a.logger.Info("state files cached from KV", "count", count, "cache_dir", a.cfg.StatesCache)
	}

	cancelStateWatch, err := stateCache.Watch(ctx)
	if err != nil {
		a.logger.Warn("failed to start state file watcher", "error", err)
	} else {
		a.addCleanup(cancelStateWatch)
	}
	a.logger.Info("state file distribution initialized", "peel", a.peelID, "cache_dir", a.cfg.StatesCache)
}

// startBeacons constructs the beacon manager (v1: the service beacon) from
// the currently cached settings — live-resolved when NATS was up in time,
// otherwise the snapshot warm-start — and runs its poll loop on the
// connected phase's context (publishing needs NATS; the manager's bounded
// buffer covers transient disconnects afterwards). Settings changes hot-swap
// the config via applyResolvedSettings, the same pattern as the basket_scope
// and schedule reloads. Beacon events ride the same core-NATS PubSub as
// scheduled results; the events JetStream stream captures them server-side.
func (a *Agent) startBeacons(ctx context.Context) {
	a.basketScopeMu.RLock()
	cached := a.cachedSettings
	a.basketScopeMu.RUnlock()

	cfg, err := beacon.ParseConfig(cached)
	if err != nil {
		a.logger.Warn("beacon settings parse error", "error", err)
	}

	mgr := beacon.NewManager(beacon.ManagerConfig{
		PeelID:  a.peelID,
		Publish: a.publishEvent,
		Service: a.mctx.Service,
		BusyFn:  a.execBusy.Load,
		OnEvent: func(name string) {
			a.metrics.PeelBeaconEventsTotal.WithLabelValues(name).Inc()
		},
		Logger: a.logger,
	})
	mgr.UpdateConfig(cfg)
	a.beaconPtr.Store(mgr)
	go mgr.Run(ctx)

	if cfg.Service != nil {
		a.logger.Info("beacon manager started",
			"services", len(cfg.Service.Services), "interval", cfg.Service.Interval)
	}
}

// runHeartbeat writes a facts.Heartbeat under the peel's own ID every 10s
// (bucket TTL 30s). Started in the connected phase; failures are logged at
// Debug and retried on the next tick — heartbeats are best-effort presence,
// never fatal. Peels enrolled before the peel-heartbeat JWT grants existed
// will fail every put (Debug) until their credentials are re-issued; the
// fleet just looks offline in presence views, nothing else degrades.
func (a *Agent) runHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(peelHeartbeatInterval)
	defer ticker.Stop()

	var kv bus.KV
	beat := func() {
		if kv == nil {
			k, err := bus.GetBucket(ctx, a.client.JetStream(), bus.BucketPeelHeartbeat)
			if err != nil {
				a.logger.Debug("peel heartbeat: get bucket", "error", err)
				return
			}
			kv = k
		}
		hb := facts.Heartbeat{
			TS:       time.Now().UTC(),
			Version:  version.Version,
			Protocol: proto.ProtocolVersion,
		}
		if _, err := bus.KVPut(ctx, kv, a.peelID, hb); err != nil {
			a.logger.Debug("peel heartbeat: put", "error", err)
		}
	}

	beat()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			beat()
		}
	}
}
