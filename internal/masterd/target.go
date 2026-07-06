package masterd

import (
	"context"
	"errors"
	"time"

	"github.com/nirnx/zester/internal/health"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/facts"
	"github.com/nirnx/zester/pkg/target"
)

// connectedPeelsInterval is how often the connected-peels gauge recounts
// the peel-heartbeat bucket.
const connectedPeelsInterval = 15 * time.Second

// startTargetService starts the master-side target-resolution service
// (roadmap C2): a facts.Index kept fresh by a KV watcher, served over
// request/reply on bus.SubjectTargetResolve via the shared resolver queue
// group. Every master joins the group (deliberately NOT lease-gated — the
// queue group load-balances requests across masters). Failures are
// non-fatal: CLI/API targeting falls back to facts-KV scans, and the
// 'target-service' readiness check reports Down. The returned stop function
// stops the service and the index watcher.
func (d *Daemon) startTargetService(ctx context.Context) func() {
	d.targetState.Store(health.CheckResult{Status: health.StatusDown, Message: "starting"})
	d.checker.Register("target-service", d.targetServiceCheck)

	idx := facts.NewIndex()
	// Retain the index on the daemon: the reactor resolves targets and
	// serves origin_facts from it in-process (zero NATS hops). The reactor
	// gates on idx.Seeded() (set by WatchIntoIndex at end-of-replay), so
	// storing the empty index up front is safe — including on the failure
	// path below, where it stays unseeded and reactor dispatches keep
	// redelivering instead of resolving against a permanently empty index.
	d.factsIndex.Store(idx)
	cancelIdx, err := facts.WatchIntoIndex(ctx, d.js, idx, d.logger)
	if err != nil {
		d.logger.Warn("target resolution service failed to start; targeting falls back to facts KV scans", "error", err)
		d.targetState.Store(health.CheckResult{Status: health.StatusDown, Message: err.Error()})
		return func() {}
	}

	cancelSvc, err := target.StartResolveService(ctx, bus.NewNATSPubSub(d.nc), target.DefaultResolveQueue, target.IndexResolveFunc(idx), d.logger)
	if err != nil {
		cancelIdx()
		d.logger.Warn("target resolution service failed to start; targeting falls back to facts KV scans", "error", err)
		d.targetState.Store(health.CheckResult{Status: health.StatusDown, Message: err.Error()})
		return func() {}
	}

	d.targetState.Store(health.CheckResult{Status: health.StatusOK})
	d.logger.Info("target resolution service started", "queue", target.DefaultResolveQueue)
	return func() {
		cancelSvc()
		cancelIdx()
	}
}

// targetServiceCheck is the 'target-service' readiness check; it returns
// the result recorded by startTargetService.
func (d *Daemon) targetServiceCheck(context.Context) health.CheckResult {
	if v, ok := d.targetState.Load().(health.CheckResult); ok {
		return v
	}
	return health.CheckResult{Status: health.StatusDown, Message: "not started"}
}

// startConnectedPeelsGauge feeds the zester_connected_peels gauge
// from the peel-heartbeat bucket (30s-TTL'd liveness keys written by
// peels): a 15s ticker counts the live keys. An empty bucket reads as 0; a
// read error keeps the last value (a KV blip must not report a fleet of
// zero) with a Debug log.
func (d *Daemon) startConnectedPeelsGauge(ctx context.Context) {
	kv, err := bus.GetBucket(ctx, d.js, bus.BucketPeelHeartbeat)
	if err != nil {
		d.logger.Warn("connected-peels gauge disabled; peel-heartbeat bucket unavailable", "error", err)
		return
	}
	go d.runConnectedPeelsGauge(ctx, kv)
}

// runConnectedPeelsGauge updates the gauge immediately and then every
// connectedPeelsInterval until ctx is cancelled.
func (d *Daemon) runConnectedPeelsGauge(ctx context.Context, kv bus.KV) {
	ticker := time.NewTicker(connectedPeelsInterval)
	defer ticker.Stop()
	for {
		d.updateConnectedPeels(ctx, kv)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Daemon) updateConnectedPeels(ctx context.Context, kv bus.KV) {
	n, err := countPeelHeartbeats(ctx, kv)
	if err != nil {
		d.logger.Debug("connected-peels count failed; keeping last gauge value", "error", err)
		return
	}
	d.reg.ConnectedPeels.Set(float64(n))
}

// countPeelHeartbeats counts the live keys in the peel-heartbeat bucket.
// An empty bucket (bus.ErrNoKeysFound) counts as zero.
func countPeelHeartbeats(ctx context.Context, kv bus.KV) (int, error) {
	lister, err := kv.ListKeys(ctx)
	if err != nil {
		if errors.Is(err, bus.ErrNoKeysFound) {
			return 0, nil
		}
		return 0, err
	}
	defer lister.Stop()

	n := 0
	for range lister.Keys() {
		n++
	}
	return n, nil
}
