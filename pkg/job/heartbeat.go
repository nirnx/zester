package job

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

const (
	// HeartbeatInterval is how often a master publishes its heartbeat.
	// The KV bucket TTL should be 3x this value (15s) per HA-R7.
	HeartbeatInterval = 5 * time.Second
)

// Heartbeater periodically writes a master heartbeat to the KV bucket.
// The heartbeat expires via bucket TTL if the master dies.
type Heartbeater struct {
	masterID string
	js       bus.JetStreamAPI
	logger   *slog.Logger
	activeFn func() []string // returns list of active JIDs
}

// NewHeartbeater creates a heartbeater for the given master instance.
// activeFn should return the list of currently active job IDs.
func NewHeartbeater(masterID string, js bus.JetStreamAPI, logger *slog.Logger, activeFn func() []string) *Heartbeater {
	if logger == nil {
		logger = slog.Default()
	}
	return &Heartbeater{
		masterID: masterID,
		js:       js,
		logger:   logger,
		activeFn: activeFn,
	}
}

// Run starts the heartbeat loop. It blocks until ctx is cancelled.
func (h *Heartbeater) Run(ctx context.Context) {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	// Publish an initial heartbeat immediately.
	h.beat(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.beat(ctx)
		}
	}
}

func (h *Heartbeater) beat(ctx context.Context) {
	kv, err := bus.GetBucket(ctx, h.js, bus.BucketMasterHeartbeat)
	if err != nil {
		h.logger.Error("heartbeat: get bucket", "error", err)
		return
	}

	hb := MasterHeartbeat{
		MasterID:   h.masterID,
		ActiveJobs: h.activeFn(),
		Timestamp:  time.Now().UTC(),
	}

	if _, err := bus.KVPut(ctx, kv, h.masterID, hb); err != nil {
		h.logger.Error("heartbeat: put", "master", h.masterID, "error", err)
	}
}

// ListLiveMasters returns the master IDs that currently have a heartbeat
// entry in the KV bucket (i.e., their TTL has not expired).
//
// A genuinely empty bucket (no live masters) yields an empty map and a nil
// error. Any KV/infrastructure failure yields a nil map and a non-nil
// error so callers (the orphan scanner) can distinguish "all masters are
// dead" from "liveness is unknown" and abort instead of mass-reclaiming.
func ListLiveMasters(ctx context.Context, js bus.JetStreamAPI) (map[string]MasterHeartbeat, error) {
	kv, err := bus.GetBucket(ctx, js, bus.BucketMasterHeartbeat)
	if err != nil {
		return nil, fmt.Errorf("job: get heartbeat bucket: %w", err)
	}

	lister, err := kv.ListKeys(ctx)
	if err != nil {
		// The real jetstream client reports an empty bucket as an empty
		// lister, but the bustest fakes (and some nats.go library
		// versions) surface ErrNoKeysFound: that means "no live
		// masters", not an infra failure.
		if errors.Is(err, bus.ErrNoKeysFound) {
			return map[string]MasterHeartbeat{}, nil
		}
		return nil, fmt.Errorf("job: list heartbeat keys: %w", err)
	}

	live := make(map[string]MasterHeartbeat)
	for key := range lister.Keys() {
		var hb MasterHeartbeat
		if err := bus.KVGet(ctx, kv, key, &hb); err != nil {
			// The entry can legitimately expire between the listing and the
			// read — that master is genuinely gone. Any other failure means
			// liveness is UNKNOWN for a master whose key was just listed
			// (i.e. it is alive); report the error so the scanner aborts
			// rather than counting a live master as missing.
			if errors.Is(err, bus.ErrKeyNotFound) {
				continue
			}
			return nil, fmt.Errorf("job: read heartbeat %s: %w", key, err)
		}
		live[key] = hb
	}
	return live, nil
}
