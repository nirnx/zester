package masterd

import (
	"context"
	"fmt"
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
// In a JetStream cluster, a request sent during RAFT meta-leader election
// (~300ms window after cluster formation) can be silently dropped — the
// server accepts the message but no leader exists to process it, so the
// client hangs until context timeout.  Use short per-attempt timeouts so
// retries happen quickly rather than waiting minutes on a lost request.
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
	for attempt := 1; ; attempt++ {
		initCtx, initCancel := context.WithTimeout(ctx, 5*time.Second)
		err := bus.InitializeStorageOpts(initCtx, d.client.JetStream(), opts)
		initCancel()
		if err == nil {
			d.logger.Info("JetStream storage initialized", "attempts", attempt)
			break
		}
		if attempt >= 20 {
			if !d.client.IsHealthy() {
				return fmt.Errorf("initialize storage after %d attempts (NATS never became healthy — check nats_url, nats_ca, and the NATS server): %w", attempt, err)
			}
			return fmt.Errorf("initialize storage after %d attempts: %w", attempt, err)
		}
		wait := min(time.Duration(attempt)*time.Second, 5*time.Second)
		d.logger.Warn("JetStream storage init failed, retrying", "attempt", attempt, "wait", wait, "error", err)
		time.Sleep(wait)
	}

	// Initialize Object Store for binary distribution (update system).
	for attempt := 1; ; attempt++ {
		initCtx, initCancel := context.WithTimeout(ctx, 5*time.Second)
		err := bus.InitializeObjectStoresOpts(initCtx, d.client.JetStream(), opts)
		initCancel()
		if err == nil {
			d.logger.Info("Object Store initialized", "attempts", attempt)
			break
		}
		if attempt >= 20 {
			return fmt.Errorf("initialize object store after %d attempts: %w", attempt, err)
		}
		wait := min(time.Duration(attempt)*time.Second, 5*time.Second)
		d.logger.Warn("Object Store init failed, retrying", "attempt", attempt, "wait", wait, "error", err)
		time.Sleep(wait)
	}
	return nil
}
