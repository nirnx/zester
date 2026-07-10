package masterd

import (
	"context"
	"fmt"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/update"
)

// startUpdatePlane wires the master-side promotion-lifecycle loops: the
// binary GC (per-version expiry replaced the object-store bucket TTL) and
// the promoted-version auto-rollout trigger. Both are tick loops sharing the
// rollout controller's buckets. Called after startRolloutController.
func (d *Daemon) startUpdatePlane(ctx context.Context) error {
	manifestKV, err := bus.GetBucket(ctx, d.client.JetStream(), bus.BucketUpdateManifests)
	if err != nil {
		return fmt.Errorf("open manifest bucket: %w", err)
	}
	rolloutKV, err := bus.GetBucket(ctx, d.client.JetStream(), bus.BucketUpdateRollouts)
	if err != nil {
		return fmt.Errorf("open rollout bucket: %w", err)
	}
	objStore, err := d.client.JetStream().ObjectStore(ctx, bus.ObjectBucketUpdateBinaries)
	if err != nil {
		return fmt.Errorf("open update-binaries object store: %w", err)
	}

	d.manifestKV = manifestKV
	gc := &update.BinaryGC{
		Manifests: update.NewManifestStore(manifestKV),
		Binaries:  update.NewBinaryStore(objStore),
		Rollouts:  update.NewRolloutStore(rolloutKV),
		Logger:    d.logger,
	}
	go d.runBinaryGC(ctx, gc)
	go d.runAutoRollout(ctx, update.NewManifestStore(manifestKV), update.NewRolloutStore(rolloutKV))
	return nil
}

// runBinaryGC reaps expired published versions. Publisher-lease-gated so
// exactly one master (modulo the advisory-lease caveat — deletes are
// idempotent) walks the buckets per pass.
func (d *Daemon) runBinaryGC(ctx context.Context, gc *update.BinaryGC) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !d.publisherLeader() {
				continue
			}
			if _, err := gc.Run(ctx, time.Now()); err != nil {
				d.logger.Warn("update binary GC pass failed", "error", err)
			}
		}
	}
}

// runAutoRollout starts a rollout to the latest PROMOTED version when live
// nodes lag behind it. Effective only when both the master-local config
// knob (update_auto_rollout) and the fleet-wide NATS switch
// (`zester update auto on|off`, default on) are enabled. NOT lease-gated:
// the deterministic rollout id makes a two-master race CAS-conflict
// harmlessly, and the one-active-rollout-per-component guard holds either
// way.
func (d *Daemon) runAutoRollout(ctx context.Context, manifests *update.ManifestStore, rollouts *update.RolloutStore) {
	if d.cfg == nil || !d.cfg.UpdateAutoRollout {
		d.logger.Info("promoted-version auto-rollout disabled by master config")
		return
	}
	interval := time.Duration(d.cfg.UpdateAutoInterval)
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.autoRolloutTick(ctx, manifests, rollouts)
		}
	}
}

func (d *Daemon) autoRolloutTick(ctx context.Context, manifests *update.ManifestStore, rollouts *update.RolloutStore) {
	sw, err := update.LoadAutoSwitch(ctx, d.manifestKV)
	if err != nil {
		d.logger.Warn("auto-rollout: switch unreadable; skipping tick", "error", err)
		return
	}
	if !sw.Enabled {
		return
	}

	rolloutStates, err := rollouts.List(ctx)
	if err != nil {
		d.logger.Warn("auto-rollout: list rollouts failed; skipping tick", "error", err)
		return
	}

	for _, component := range d.cfg.UpdateAutoComponents {
		mans, err := manifests.ListByComponent(ctx, component)
		if err != nil {
			d.logger.Warn("auto-rollout: list manifests failed", "component", component, "error", err)
			continue
		}
		statuses, err := update.ListNodeStatuses(ctx, d.statusKV, component)
		if err != nil {
			d.logger.Warn("auto-rollout: list node statuses failed", "component", component, "error", err)
			continue
		}

		plan := update.PickAutoRollout(component, mans, statuses, rolloutStates)
		if plan == nil {
			continue
		}

		cfg := plan.RolloutConfig(d.cfg.UpdateAutoBatchSize, time.Duration(d.cfg.UpdateAutoSoakTime), d.cfg.UpdateAutoMaxFailed)
		d.logger.Info("auto-rollout: starting rollout to promoted version",
			"component", component, "version", plan.Version, "nodes", len(plan.NodeIDs), "id", cfg.RolloutID)
		if _, err := d.rolloutCtrl.StartRollout(ctx, cfg, plan.NodeIDs); err != nil {
			// A CAS Create conflict here means another master won the same
			// deterministic id — exactly the intended dedup, not a failure.
			d.logger.Info("auto-rollout: start did not proceed", "component", component,
				"version", plan.Version, "reason", err.Error())
		}
	}
}
