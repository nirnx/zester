package masterd

import (
	"context"
	"fmt"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
)

const (
	// publisherLeaseKey names the single-publisher lease (roadmap B7)
	// gating the settings-files publish, the state-files publish, and the
	// GitFS sync loop: exactly one master (modulo the advisory-lease
	// caveat) performs these KV writes at a time.
	publisherLeaseKey = "publisher"

	// secretsLeaseKey names the facts→secrets single-owner lease (roadmap
	// B6): only the holder publishes per-peel encrypted secrets from the
	// facts watcher callback. The enrollment issued→active CAS transition
	// stays ungated on all masters (it is idempotent).
	secretsLeaseKey = "facts-secrets"

	// leaseRetryWait paces restarts of a lease loop that exited abnormally
	// (e.g. the leases bucket was temporarily unresolvable).
	leaseRetryWait = 5 * time.Second
)

// startPublisherLease starts the advisory "publisher" leader lease. The
// holder performs the publishes: on every acquisition, a per-acquisition
// sub-context (derived from ctx, cancelled on loss) is created and
// runLeaderPublish runs under it — initial settings-files publish, initial
// state-files publish, then the GitFS sync loop. Non-holders log and stand
// by; they start publishing when they acquire the lease. In a single-master
// deployment the lease is acquired immediately, so startup behavior matches
// the pre-lease code.
func (d *Daemon) startPublisherLease(ctx context.Context) error {
	lease, err := bus.NewLeaderLease(bus.LeaderLeaseConfig{
		JS:            d.js,
		Key:           publisherLeaseKey,
		HolderID:      d.masterID,
		TTL:           d.leaseTTL,
		RenewInterval: d.leaseRenewInterval,
		Logger:        d.logger,
		OnAcquired:    func() { d.startLeaderPublish(ctx) },
		OnLost:        d.stopLeaderPublish,
	})
	if err != nil {
		return fmt.Errorf("create publisher lease: %w", err)
	}
	d.logger.Info("publisher lease candidate started; standing by until acquired",
		"key", publisherLeaseKey)
	go d.runLease(ctx, lease, publisherLeaseKey)
	return nil
}

// startSecretsLease starts the advisory "facts-secrets" leader lease.
// handleFactsUpdate publishes per-peel secrets only while this master holds
// it (see secretsLeader). Called before the facts watcher starts so the
// watcher callback never observes a half-assigned pointer.
func (d *Daemon) startSecretsLease(ctx context.Context) error {
	lease, err := bus.NewLeaderLease(bus.LeaderLeaseConfig{
		JS:            d.js,
		Key:           secretsLeaseKey,
		HolderID:      d.masterID,
		TTL:           d.leaseTTL,
		RenewInterval: d.leaseRenewInterval,
		Logger:        d.logger,
	})
	if err != nil {
		return fmt.Errorf("create facts-secrets lease: %w", err)
	}
	d.secretsLease = lease
	go d.runLease(ctx, lease, secretsLeaseKey)
	return nil
}

// runLease drives a lease's acquire/renew loop until ctx is cancelled,
// restarting it after leaseRetryWait if it exits abnormally (Run only
// returns early when the leases bucket cannot be resolved).
func (d *Daemon) runLease(ctx context.Context, lease *bus.LeaderLease, key string) {
	for {
		err := lease.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		d.logger.Error("leader lease loop exited; retrying", "key", key, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(leaseRetryWait):
		}
	}
}

// startLeaderPublish is the publisher lease's OnAcquired callback: it
// creates the per-acquisition sub-context and launches the leader-publish
// goroutine. It must not block — it runs on the lease's Run goroutine,
// which also drives lease renewal.
func (d *Daemon) startLeaderPublish(ctx context.Context) {
	acqCtx, cancel := context.WithCancel(ctx)
	d.pubMu.Lock()
	if d.pubCancel != nil {
		// Defensive: never let two leader-publish runners coexist.
		d.pubCancel()
	}
	d.pubCancel = cancel
	d.pubMu.Unlock()
	go d.runLeaderPublish(acqCtx)
}

// stopLeaderPublish is the publisher lease's OnLost callback: it cancels
// the current acquisition sub-context, stopping the GitFS loop and any
// in-flight publish.
func (d *Daemon) stopLeaderPublish() {
	d.pubMu.Lock()
	if d.pubCancel != nil {
		d.pubCancel()
		d.pubCancel = nil
	}
	d.pubMu.Unlock()
}

// runLeaderPublish performs the leader-only KV writes for one lease
// acquisition: the initial settings-files publish, the initial state-files
// publish, the reactor-files publish, and then the GitFS sync loop (which
// republishes state files after every sync) until the acquisition context
// is cancelled — lease lost or shutdown.
func (d *Daemon) runLeaderPublish(ctx context.Context) {
	d.publishSettingsFiles(ctx)
	d.publishStateFiles(ctx)
	d.publishReactorFiles(ctx)

	if d.gitfs == nil {
		return
	}
	err := d.gitfs.Run(ctx)
	if ctx.Err() != nil {
		return // lease lost or shutting down: normal exit
	}
	msg := "gitfs syncer exited unexpectedly"
	if err != nil {
		msg = "gitfs syncer exited: " + err.Error()
		d.logger.Error("gitfs syncer error", "error", err)
	}
	d.gitfsExit.Store(msg)
}

// secretsLeader reports whether this master currently owns per-peel secrets
// publishing. A nil lease (unit tests exercising handleFactsUpdate
// directly) means yes.
func (d *Daemon) secretsLeader() bool {
	return d.secretsLease == nil || d.secretsLease.IsLeader()
}
