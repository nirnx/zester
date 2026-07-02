package settings

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
)

// DefaultWatchReconnectJitter is the default spread for the deterministic
// per-peel jitter added to watcher reconnect backoff. A NATS restart drops
// every peel's KV watchers at once; without jitter the whole fleet would
// re-create its consumers inside the same 1–30s backoff window, spiking the
// JetStream meta layer exactly when the cluster is weakest.
const DefaultWatchReconnectJitter = 60 * time.Second

var (
	watchJitterMu     sync.RWMutex
	watchJitterID     string
	watchJitterSpread = DefaultWatchReconnectJitter
)

// SetWatchJitter configures the deterministic reconnect jitter applied by
// every KV watcher in this package. id seeds the deterministic offset —
// set it to the peel ID at startup so each peel gets a stable, unique slot
// in the spread window (watchers that already know the peel ID, such as
// WatchSecrets and WatchSecretsAndCurve, use it directly and only take the
// spread from here). spread bounds the offset; 0 disables jitter (useful in
// tests). When no id is configured, the hostname is used as a fallback seed.
func SetWatchJitter(id string, spread time.Duration) {
	watchJitterMu.Lock()
	defer watchJitterMu.Unlock()
	watchJitterID = id
	if spread < 0 {
		spread = 0
	}
	watchJitterSpread = spread
}

// watchReconnectJitter returns the deterministic jitter offset for the given
// identity: hash(id) mod spread. An empty id falls back to the package-level
// jitter ID (SetWatchJitter), then the hostname. Returns 0 when jitter is
// disabled or no identity is available.
func watchReconnectJitter(id string) time.Duration {
	watchJitterMu.RLock()
	spread := watchJitterSpread
	if id == "" {
		id = watchJitterID
	}
	watchJitterMu.RUnlock()

	if id == "" {
		if hn, err := os.Hostname(); err == nil {
			id = hn
		}
	}
	if spread <= 0 || id == "" {
		return 0
	}

	h := fnv.New64a()
	h.Write([]byte(id)) //nolint:errcheck // fnv never errors
	return time.Duration(h.Sum64() % uint64(spread))
}

// watcherFactory creates (or re-creates) a KV watcher after a consumer loss.
type watcherFactory func(ctx context.Context) (bus.KeyWatcher, error)

// runWatchLoop consumes entries from a KV watcher, dispatching every non-nil
// entry to onEntry (nil sentinel entries — end of initial replay — are
// skipped). When the updates channel closes (JetStream consumer lost), the
// watcher is re-created via remake with exponential backoff (1s..30s). The
// first reconnect attempt additionally waits the deterministic per-identity
// jitter offset (see SetWatchJitter) so a fleet-wide consumer loss does not
// stampede the cluster with simultaneous consumer-create requests.
func runWatchLoop(
	ctx context.Context,
	watcher bus.KeyWatcher,
	remake watcherFactory,
	jitterID string,
	onEntry func(bus.KVEntry),
	logger *slog.Logger,
	bucket, keys string,
) {
	defer func() {
		watcher.Stop() //nolint:errcheck // best-effort cleanup
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-watcher.Updates():
			if !ok {
				watcher.Stop() //nolint:errcheck // channel already closed
				backoff := time.Second
				// Deterministic per-peel spread, applied once per loss:
				// later attempts keep their relative offset via the
				// shared exponential backoff schedule.
				jitter := watchReconnectJitter(jitterID)
				for {
					wait := backoff + jitter
					jitter = 0
					logger.Warn("watcher lost, reconnecting",
						"bucket", bucket, "keys", keys, "backoff", wait)
					select {
					case <-ctx.Done():
						return
					case <-time.After(wait):
					}
					backoff = min(backoff*2, 30*time.Second)

					newWatcher, err := remake(ctx)
					if err != nil {
						logger.Warn("watcher reconnect failed",
							"bucket", bucket, "keys", keys, "error", err)
						continue
					}
					watcher = newWatcher
					logger.Info("watcher reconnected", "bucket", bucket, "keys", keys)
					break
				}
				continue
			}
			if entry == nil {
				continue
			}
			onEntry(entry)
		}
	}
}

// WatchRawFiles watches the settings-files KV bucket for revision changes
// and calls the provided function when the revision bumps. This ensures peels
// only re-resolve after the master has finished writing all files atomically.
// Falls back to WatchAll if the revision key watch fails (backward compat).
// Returns a cancel function to stop watching. The watcher automatically
// reconnects with exponential backoff plus deterministic per-peel jitter
// (SetWatchJitter) if the JetStream consumer is lost.
func WatchRawFiles(ctx context.Context, js bus.JetStreamAPI, fn func(), logger *slog.Logger) (context.CancelFunc, error) {
	kv, err := bus.GetBucket(ctx, js, bus.BucketSettingsFiles)
	if err != nil {
		return nil, fmt.Errorf("settings: get settings-files bucket for watch: %w", err)
	}

	watcher, err := kv.Watch(ctx, bus.KeyRevision)
	if err != nil {
		logger.Warn("revision watch failed, falling back to WatchAll", "bucket", bus.BucketSettingsFiles, "error", err)
		return watchRawFilesAll(ctx, js, kv, fn, logger)
	}

	watchCtx, cancel := context.WithCancel(ctx)

	remake := func(ctx context.Context) (bus.KeyWatcher, error) {
		newKV, err := bus.GetBucket(ctx, js, bus.BucketSettingsFiles)
		if err != nil {
			return nil, err
		}
		return newKV.Watch(ctx, bus.KeyRevision)
	}

	go runWatchLoop(watchCtx, watcher, remake, "",
		func(bus.KVEntry) { fn() },
		logger, bus.BucketSettingsFiles, bus.KeyRevision)

	return cancel, nil
}

// watchRawFilesAll is the backward-compatible fallback that watches all keys.
func watchRawFilesAll(ctx context.Context, js bus.JetStreamAPI, kv bus.KV, fn func(), logger *slog.Logger) (context.CancelFunc, error) {
	watcher, err := kv.WatchAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("settings: create settings-files watcher (fallback): %w", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)

	remake := func(ctx context.Context) (bus.KeyWatcher, error) {
		newKV, err := bus.GetBucket(ctx, js, bus.BucketSettingsFiles)
		if err != nil {
			return nil, err
		}
		return newKV.WatchAll(ctx)
	}

	go runWatchLoop(watchCtx, watcher, remake, "",
		func(bus.KVEntry) { fn() },
		logger, bus.BucketSettingsFiles, "(all)")

	return cancel, nil
}

// WatchSecrets watches the secrets KV bucket for changes to this peel's key
// and calls the provided function when they change.
// Returns a cancel function to stop watching. The watcher automatically
// reconnects with exponential backoff plus deterministic per-peel jitter
// if the JetStream consumer is lost.
//
// Peels that also watch the master curve public key should prefer
// WatchSecretsAndCurve, which covers both keys with a single JetStream
// consumer.
func WatchSecrets(ctx context.Context, js bus.JetStreamAPI, peelID string, fn func(), logger *slog.Logger) (context.CancelFunc, error) {
	kv, err := bus.GetBucket(ctx, js, bus.BucketSecrets)
	if err != nil {
		return nil, fmt.Errorf("settings: get secrets bucket for watch: %w", err)
	}

	watcher, err := kv.Watch(ctx, peelID)
	if err != nil {
		return nil, fmt.Errorf("settings: create secrets watcher for %s: %w", peelID, err)
	}

	watchCtx, cancel := context.WithCancel(ctx)

	remake := func(ctx context.Context) (bus.KeyWatcher, error) {
		newKV, err := bus.GetBucket(ctx, js, bus.BucketSecrets)
		if err != nil {
			return nil, err
		}
		return newKV.Watch(ctx, peelID)
	}

	go runWatchLoop(watchCtx, watcher, remake, peelID,
		func(bus.KVEntry) { fn() },
		logger, bus.BucketSecrets, peelID)

	return cancel, nil
}

// WatchMasterCurvePub watches the well-known master curve public key entry
// in the secrets KV bucket and calls fn with the latest key when it changes.
// Returns a cancel function to stop watching. The watcher automatically
// reconnects with exponential backoff plus deterministic per-peel jitter
// if the JetStream consumer is lost.
//
// Peels that also watch their per-peel secrets key should prefer
// WatchSecretsAndCurve, which covers both keys with a single JetStream
// consumer.
func WatchMasterCurvePub(ctx context.Context, js bus.JetStreamAPI, fn func(pub string), logger *slog.Logger) (context.CancelFunc, error) {
	kv, err := bus.GetBucket(ctx, js, bus.BucketSecrets)
	if err != nil {
		return nil, fmt.Errorf("settings: get secrets bucket for master curve pub watch: %w", err)
	}

	watcher, err := kv.Watch(ctx, MasterCurvePubKey)
	if err != nil {
		return nil, fmt.Errorf("settings: create master curve pub watcher: %w", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)

	remake := func(ctx context.Context) (bus.KeyWatcher, error) {
		newKV, err := bus.GetBucket(ctx, js, bus.BucketSecrets)
		if err != nil {
			return nil, err
		}
		return newKV.Watch(ctx, MasterCurvePubKey)
	}

	go runWatchLoop(watchCtx, watcher, remake, "",
		func(entry bus.KVEntry) { fn(string(entry.Value())) },
		logger, bus.BucketSecrets, MasterCurvePubKey)

	return cancel, nil
}

// WatchSecretsAndCurve watches both this peel's secrets key and the master
// curve public key with a SINGLE filtered JetStream consumer (both keys live
// in the secrets bucket), halving the per-peel consumer count relative to
// running WatchSecrets and WatchMasterCurvePub separately. onSecrets fires
// when the per-peel secrets entry changes; onCurve fires with the latest key
// whenever the master curve public key entry changes (including the initial
// replay of its current value, matching WatchMasterCurvePub semantics).
//
// Returns a cancel function to stop watching. The watcher automatically
// reconnects with exponential backoff plus deterministic per-peel jitter
// (hash(peelID) mod the SetWatchJitter spread) if the consumer is lost.
func WatchSecretsAndCurve(ctx context.Context, js bus.JetStreamAPI, peelID string, onSecrets func(), onCurve func(pub string), logger *slog.Logger) (context.CancelFunc, error) {
	if peelID == "" {
		return nil, fmt.Errorf("settings: peel ID is required for combined secrets watch")
	}

	kv, err := bus.GetBucket(ctx, js, bus.BucketSecrets)
	if err != nil {
		return nil, fmt.Errorf("settings: get secrets bucket for combined watch: %w", err)
	}

	filters := []string{peelID, MasterCurvePubKey}
	keysDesc := peelID + "," + MasterCurvePubKey

	watcher, err := kv.WatchFiltered(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("settings: create combined secrets watcher for %s: %w", peelID, err)
	}

	watchCtx, cancel := context.WithCancel(ctx)

	remake := func(ctx context.Context) (bus.KeyWatcher, error) {
		newKV, err := bus.GetBucket(ctx, js, bus.BucketSecrets)
		if err != nil {
			return nil, err
		}
		return newKV.WatchFiltered(ctx, filters)
	}

	go runWatchLoop(watchCtx, watcher, remake, peelID,
		func(entry bus.KVEntry) {
			switch entry.Key() {
			case MasterCurvePubKey:
				if onCurve != nil {
					onCurve(string(entry.Value()))
				}
			case peelID:
				if onSecrets != nil {
					onSecrets()
				}
			}
		},
		logger, bus.BucketSecrets, keysDesc)

	return cancel, nil
}
