package facts

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

// watchFactsEntries runs a self-reconnecting watcher over the facts KV bucket
// and invokes handle for every non-nil entry, including delete and purge
// markers. It is the shared machinery behind Watch and WatchIntoIndex.
// The watcher automatically reconnects with exponential backoff if the
// JetStream consumer is lost (e.g., NATS cluster failure). onReplayDone
// (optional) is invoked exactly once, when the INITIAL replay's nil
// end-of-replay sentinel arrives — reconnect replays do not re-fire it.
func watchFactsEntries(ctx context.Context, js bus.JetStreamAPI, handle func(entry bus.KVEntry), onReplayDone func(), logger *slog.Logger) (context.CancelFunc, error) {
	kv, err := bus.GetBucket(ctx, js, bus.BucketFacts)
	if err != nil {
		return nil, fmt.Errorf("facts: get facts bucket for watch: %w", err)
	}

	watcher, err := kv.WatchAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("facts: create watcher: %w", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)

	go func() {
		defer watcher.Stop()
		replayDone := false
		for {
			select {
			case <-watchCtx.Done():
				return
			case entry, ok := <-watcher.Updates():
				if !ok {
					watcher.Stop()
					backoff := time.Second
					for {
						logger.Warn("watcher lost, reconnecting", "bucket", bus.BucketFacts, "backoff", backoff)
						select {
						case <-watchCtx.Done():
							return
						case <-time.After(backoff):
						}
						backoff = min(backoff*2, 30*time.Second)

						newKV, err := bus.GetBucket(watchCtx, js, bus.BucketFacts)
						if err != nil {
							logger.Warn("watcher reconnect failed", "bucket", bus.BucketFacts, "error", err)
							continue
						}
						newWatcher, err := newKV.WatchAll(watchCtx)
						if err != nil {
							logger.Warn("watcher reconnect failed", "bucket", bus.BucketFacts, "error", err)
							continue
						}
						watcher = newWatcher
						backoff = time.Second
						logger.Info("watcher reconnected", "bucket", bus.BucketFacts)
						break
					}
					continue
				}
				if entry == nil {
					// End-of-replay sentinel: the bucket's current contents
					// have all been handled.
					if !replayDone {
						replayDone = true
						if onReplayDone != nil {
							onReplayDone()
						}
					}
					continue
				}
				handle(entry)
			}
		}
	}()

	return cancel, nil
}

// WatchIntoIndex keeps idx synchronized with the facts KV bucket: puts call
// idx.Update, explicit deletes and purges call idx.Remove. It reuses the same
// self-reconnecting watch machinery as Watch, so a WatchAll replay on start
// (or reconnect) seeds the index with the full bucket contents. When the
// initial replay completes (the watcher's nil end-of-replay sentinel) the
// index is marked seeded (idx.Seeded) — consumers that must not resolve
// against a partially-populated index gate on it.
//
// Staleness bounds: explicit kv.Delete/Purge operations ARE surfaced by
// JetStream watchers and are applied to the index. Two cases are not:
//   - TTL-based expiry (MaxAge) is not delivered to watchers; the facts
//     bucket currently has no TTL, so this does not occur today.
//   - A delete that happens while the watcher is reconnecting is missed;
//     the replay after reconnect only re-Updates surviving keys, so the
//     deleted peel lingers in the index until the process restarts. This
//     matches the KV-scan behavior for dead peels (whose facts also remain
//     in the bucket until explicitly deleted).
//
// Call the returned cancel function to stop watching.
func WatchIntoIndex(ctx context.Context, js bus.JetStreamAPI, idx *Index, logger *slog.Logger) (context.CancelFunc, error) {
	if idx == nil {
		return nil, fmt.Errorf("facts: watch into index: index is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	return watchFactsEntries(ctx, js, func(entry bus.KVEntry) {
		switch entry.Operation() {
		case bus.KVOpDelete, bus.KVOpPurge:
			idx.Remove(entry.Key())
		default:
			var f Facts
			if err := bus.Decode(entry.Value(), &f); err != nil {
				logger.Warn("facts: index watch: decode entry", "peel_id", entry.Key(), "error", err)
				return
			}
			idx.Update(entry.Key(), f)
		}
	}, idx.MarkSeeded, logger)
}
