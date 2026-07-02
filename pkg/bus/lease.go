package bus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

// Default LeaderLease timings. The TTL must match the leases bucket's
// per-entry TTL (see BucketLeases in DefaultBuckets) — the bucket expires the
// entry, the lease config only paces renewals and re-acquisition attempts.
const (
	defaultLeaseTTL           = 15 * time.Second
	defaultLeaseRenewInterval = 5 * time.Second
)

// LeaderLeaseConfig configures a LeaderLease.
type LeaderLeaseConfig struct {
	// JS is the JetStream API used to access the lease bucket. Required.
	JS JetStreamAPI

	// Bucket is the KV bucket holding leases. Defaults to BucketLeases.
	Bucket string

	// Key names the contended role, e.g. "settings-publisher". Required.
	Key string

	// HolderID identifies this candidate (typically the master ID). Required.
	HolderID string

	// TTL is how long an unrenewed lease survives before another candidate
	// can take it. Must match the bucket's per-entry TTL. Defaults to 15s.
	TTL time.Duration

	// RenewInterval is how often the current leader refreshes the lease.
	// Must be well below TTL. Defaults to 5s.
	RenewInterval time.Duration

	// Logger is the structured logger. Defaults to slog.Default().
	Logger *slog.Logger

	// OnAcquired is called (from the Run goroutine) when leadership is won.
	OnAcquired func()

	// OnLost is called (from the Run goroutine) when leadership is lost,
	// including on shutdown while leader.
	OnLost func()
}

// LeaderLease is an advisory leader election over a TTL'd KV entry: the
// candidate that Creates the key holds the lease and renews it via CAS
// Update; everyone else retries acquisition after roughly TTL/3.
//
// This is a load-shedding/dedup mechanism, NOT a fencing token: there is no
// guarantee of a single leader at all times. A partitioned or paused leader
// can believe it holds the lease for up to TTL after another candidate has
// taken it, so brief split-brain (< TTL) is possible and consumers must
// tolerate duplicate work. Suitable for cutting redundant publishes
// (settings/state-file/GitFS sync); unsuitable for anything that requires
// mutual exclusion for correctness.
type LeaderLease struct {
	cfg LeaderLeaseConfig

	mu     sync.Mutex
	leader bool
}

// NewLeaderLease validates the config and applies defaults.
func NewLeaderLease(cfg LeaderLeaseConfig) (*LeaderLease, error) {
	if cfg.JS == nil {
		return nil, errors.New("bus: leader lease: JS is required")
	}
	if cfg.Key == "" {
		return nil, errors.New("bus: leader lease: Key is required")
	}
	if cfg.HolderID == "" {
		return nil, errors.New("bus: leader lease: HolderID is required")
	}
	if cfg.Bucket == "" {
		cfg.Bucket = BucketLeases
	}
	if cfg.TTL == 0 {
		cfg.TTL = defaultLeaseTTL
	}
	if cfg.RenewInterval == 0 {
		cfg.RenewInterval = defaultLeaseRenewInterval
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &LeaderLease{cfg: cfg}, nil
}

// IsLeader reports whether this candidate currently believes it holds the
// lease. Subject to the advisory caveat on LeaderLease: a stale true is
// possible for up to TTL.
func (l *LeaderLease) IsLeader() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.leader
}

// Run drives the acquire/renew loop until ctx is cancelled, then releases a
// held lease (best-effort) and returns ctx.Err(). It only returns early if
// the lease bucket cannot be resolved.
func (l *LeaderLease) Run(ctx context.Context) error {
	kv, err := GetBucket(ctx, l.cfg.JS, l.cfg.Bucket)
	if err != nil {
		return fmt.Errorf("bus: leader lease %q: %w", l.cfg.Key, err)
	}

	for {
		if rev, err := kv.Create(ctx, l.cfg.Key, []byte(l.cfg.HolderID)); err == nil {
			l.cfg.Logger.Info("leader lease acquired",
				"key", l.cfg.Key, "holder", l.cfg.HolderID)
			l.setLeader(true)
			rev = l.renewLoop(ctx, kv, rev)
			l.setLeader(false)
			if ctx.Err() != nil {
				l.release(kv, rev)
				return ctx.Err()
			}
		} else if ctx.Err() != nil {
			return ctx.Err()
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(l.acquireDelay()):
		}
	}
}

// renewLoop refreshes the lease every RenewInterval until the CAS update
// fails (another candidate took the key, or a KV error — either way we can
// no longer prove ownership) or ctx is cancelled. Returns the last revision
// this holder wrote, for the shutdown release.
func (l *LeaderLease) renewLoop(ctx context.Context, kv KV, rev uint64) uint64 {
	ticker := time.NewTicker(l.cfg.RenewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return rev
		case <-ticker.C:
			next, err := kv.Update(ctx, l.cfg.Key, []byte(l.cfg.HolderID), rev)
			if err != nil {
				if ctx.Err() == nil {
					l.cfg.Logger.Warn("leader lease lost",
						"key", l.cfg.Key, "holder", l.cfg.HolderID, "error", err)
				}
				return rev
			}
			rev = next
		}
	}
}

// release deletes the lease on shutdown so a successor need not wait out the
// TTL. Fenced on our last revision to avoid deleting a successor's lease;
// failure is fine — the entry expires via the bucket TTL.
func (l *LeaderLease) release(kv KV, rev uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := kv.Delete(ctx, l.cfg.Key, LastRevision(rev)); err != nil {
		l.cfg.Logger.Debug("leader lease release failed; lease will expire via TTL",
			"key", l.cfg.Key, "error", err)
	}
}

// acquireDelay spaces acquisition attempts at ~TTL/3 with jitter so
// candidates do not lock-step on an expiring lease.
func (l *LeaderLease) acquireDelay() time.Duration {
	base := l.cfg.TTL / 3
	return base + rand.N(base/2+1)
}

func (l *LeaderLease) setLeader(v bool) {
	l.mu.Lock()
	changed := l.leader != v
	l.leader = v
	l.mu.Unlock()

	if !changed {
		return
	}
	if v && l.cfg.OnAcquired != nil {
		l.cfg.OnAcquired()
	}
	if !v && l.cfg.OnLost != nil {
		l.cfg.OnLost()
	}
}
