package masterd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

// connectNATS validates the configured NATS URLs, builds the TLS config, and
// connects to the external NATS server with a bounded retry loop (20
// attempts, backoff capped at 5s). On success d.client and d.nc are set; the
// caller (Run) registers the client.Shutdown defer and flips the readiness
// pointer, exactly where the pre-extraction code did.
func (d *Daemon) connectNATS() error {
	d.logger.Info("connecting to NATS", "url", d.cfg.NatsURL)
	natsURLs := bus.NormalizeNATSURLs([]string{d.cfg.NatsURL})
	if err := bus.ValidateTLSNATSURLs(natsURLs); err != nil {
		return fmt.Errorf("rejecting NATS configuration: %w", err)
	}

	natsTLS, natsCA, caOptional := bus.NATSClientTLS(natsURLs, d.cfg.NatsCA, d.cfg.AuthDir)
	var client *bus.Client
	for attempt := 1; ; attempt++ {
		var err error
		client, err = bus.NewClient(bus.ClientConfig{
			URLs:           natsURLs,
			Name:           "zester-master",
			CredsFile:      d.cfg.AuthDir + "/master.creds",
			TLS:            natsTLS,
			CAFile:         natsCA,
			CAFileOptional: caOptional,
			Logger:         d.logger,
			RetryConnect:   true,
			// Transport metrics for the Prometheus registry.
			OnReconnect:    d.reg.NATSReconnects.Inc,
			OnDisconnect:   func(error) { d.reg.NATSDisconnects.Inc() },
			OnSlowConsumer: d.reg.NATSSlowConsumers.Inc,
		})
		if err == nil {
			break
		}
		if attempt >= 20 {
			return fmt.Errorf("connect to NATS after %d attempts: %w", attempt, err)
		}
		wait := min(time.Duration(attempt)*time.Second, 5*time.Second)
		d.logger.Warn("NATS connect failed, retrying", "attempt", attempt, "wait", wait, "error", err)
		time.Sleep(wait)
	}
	d.client = client
	d.nc = client.Conn()
	return nil
}

// initStorage initializes the JetStream KV buckets and streams, then the
// Object Store for binary distribution (update system), each with a bounded
// retry loop.
//
// storageInitAttemptTimeout bounds one storage-initialization attempt. In a
// JetStream cluster a request sent during RAFT meta-leader election can be
// silently dropped — the server accepts the message but no leader exists to
// process it — so the client would hang until context timeout; a bounded
// attempt turns that into a retry. On a fresh cluster every new replicated
// stream also waits for its own leader election (~4-9s); since
// bus.InitializeStorageOpts creates all assets concurrently, one attempt
// needs to cover one election window plus margin, not one per asset.
const storageInitAttemptTimeout = 15 * time.Second

// initStorage creates the KV buckets, streams and the object store with a
// bounded retry loop per asset family. The two families are independent
// stream groups, so they are initialized concurrently: a fresh cluster pays
// one leader-election window, not two.
func (d *Daemon) initStorage(ctx context.Context) error {
	// Cluster size drives the replica auto-tiering (finding 1 / Tier B3):
	// with --jetstream-replicas 0, critical assets default to
	// min(3, clusterSize) instead of the old always-1. Servers() includes
	// configured plus gossip-discovered cluster members.
	clusterSize := len(d.client.Conn().Servers())
	opts := bus.StorageOptions{
		Replicas:    d.cfg.JetStreamReplicas,
		ClusterSize: clusterSize,
		Logger:      d.logger,
	}
	d.logger.Info("initializing JetStream storage",
		"replicas", d.cfg.JetStreamReplicas, "cluster_size", clusterSize)

	results := make(chan error, 2)
	go func() {
		results <- d.retryStorageInit(ctx, "JetStream storage", func(c context.Context) error {
			return bus.InitializeStorageOpts(c, d.client.JetStream(), opts)
		})
	}()
	go func() {
		results <- d.retryStorageInit(ctx, "Object Store", func(c context.Context) error {
			return bus.InitializeObjectStoresOpts(c, d.client.JetStream(), opts)
		})
	}()
	var errs []error
	for range 2 {
		if err := <-results; err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// retryStorageInit runs init with a fresh storageInitAttemptTimeout budget per
// attempt, up to 20 attempts with a backoff capped at 5s. The log messages
// ("<what> initialized" / "<what> init failed, retrying") are stable.
func (d *Daemon) retryStorageInit(ctx context.Context, what string, init func(context.Context) error) error {
	for attempt := 1; ; attempt++ {
		initCtx, initCancel := context.WithTimeout(ctx, storageInitAttemptTimeout)
		err := init(initCtx)
		initCancel()
		if err == nil {
			d.logger.Info(what+" initialized", "attempts", attempt)
			return nil
		}
		if attempt >= 20 {
			if !d.client.IsHealthy() {
				return fmt.Errorf("initialize %s after %d attempts (NATS never became healthy — check nats_url, nats_ca, and the NATS server): %w", strings.ToLower(what), attempt, err)
			}
			return fmt.Errorf("initialize %s after %d attempts: %w", strings.ToLower(what), attempt, err)
		}
		wait := min(time.Duration(attempt)*time.Second, 5*time.Second)
		d.logger.Warn(what+" init failed, retrying", "attempt", attempt, "wait", wait, "error", err)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return fmt.Errorf("initialize %s: %w", strings.ToLower(what), ctx.Err())
		}
	}
}
