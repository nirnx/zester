package peeld

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
)

// Persisted JID dedup (roadmap B9 peel half, finding 32): the per-JID epoch
// map that fences stale dispatches now (a) survives peel restarts via an
// on-disk msgpack snapshot, so a re-delivered ExecRequest is rejected even
// after a restart, and (b) is capped with insertion-order eviction, fixing
// the unbounded growth of the old in-memory map.
const (
	// defaultDedupPath is the well-known on-disk location of the jid→epoch
	// snapshot (msgpack, 0600), alongside the other /data peel state.
	defaultDedupPath = "/data/peel-dedup.msgpack"

	// dedupCapacity bounds the number of tracked JIDs; the oldest entry (by
	// first observation) is evicted when a new JID would exceed it.
	dedupCapacity = 4096

	// dedupSaveDelay debounces disk writes after a change.
	dedupSaveDelay = time.Second
)

// dedupTracker records the highest dispatch epoch seen per JID. It preserves
// the pre-existing epoch-fencing semantics (higher epoch wins, lower epoch is
// stale) and adds duplicate rejection: a JID re-delivered at an epoch <= the
// stored one is rejected instead of re-executed.
type dedupTracker struct {
	path      string // "" disables persistence (tests)
	capacity  int
	saveDelay time.Duration
	logger    *slog.Logger

	mu    sync.Mutex
	m     map[string]uint64
	order []string    // JID insertion order, for capacity eviction
	timer *time.Timer // armed while a debounced save is pending
}

func newDedupTracker(path string, capacity int, saveDelay time.Duration, logger *slog.Logger) *dedupTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &dedupTracker{
		path:      path,
		capacity:  capacity,
		saveDelay: saveDelay,
		logger:    logger,
		m:         make(map[string]uint64),
	}
}

// Load seeds the tracker from the on-disk snapshot. A missing file is not an
// error (first boot). Insertion order across restarts is arbitrary — the cap
// only needs approximate oldest-first behavior, not exact ordering.
func (d *dedupTracker) Load() error {
	if d.path == "" {
		return nil
	}
	data, err := os.ReadFile(d.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("peeld: read dedup state: %w", err)
	}
	var m map[string]uint64
	if err := bus.Decode(data, &m); err != nil {
		return fmt.Errorf("peeld: decode dedup state: %w", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for jid, epoch := range m {
		if len(d.order) >= d.capacity {
			break
		}
		if _, ok := d.m[jid]; !ok {
			d.order = append(d.order, jid)
		}
		d.m[jid] = epoch
	}
	return nil
}

// Observe applies the epoch-fencing rules for one dispatch and returns the
// previously stored epoch plus whether the dispatch must be rejected:
//
//   - unknown JID: record the epoch, accept.
//   - epoch > stored: record the new epoch, accept (a newer master won the
//     fence — unchanged from the pre-persistence semantics).
//   - epoch <= stored: reject. `<` is a stale dispatch from a superseded
//     master (the original fencing rule); `==` is a re-delivery of an
//     already-observed dispatch (the B9 dedup — combined with persistence it
//     holds across peel restarts).
//
// Accepted observations schedule a debounced save to disk.
func (d *dedupTracker) Observe(jid string, epoch uint64) (prev uint64, rejected bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	prev, seen := d.m[jid]
	if seen && epoch <= prev {
		return prev, true
	}
	if !seen {
		if len(d.order) >= d.capacity && d.capacity > 0 {
			evict := d.order[0]
			d.order = d.order[1:]
			delete(d.m, evict)
		}
		d.order = append(d.order, jid)
	}
	d.m[jid] = epoch
	d.scheduleSaveLocked()
	return prev, false
}

// ObserveDurable applies Observe's fencing rules and, when the dispatch is
// ACCEPTED, synchronously flushes the snapshot to disk before returning
// (finding C0): the master's ack-window redispatch re-sends the SAME
// (jid, epoch) to peels it has not heard from, so a peel that crashes right
// after accepting a job and restarts within that window must still find the
// record on disk — a purely debounced save leaves a ~1s window where a
// non-idempotent job would be re-executed. Rejected dispatches skip the
// flush. A failed flush is logged at Warn (durability is degraded, not the
// dispatch itself) and the debounced save is re-armed as a retry.
func (d *dedupTracker) ObserveDurable(jid string, epoch uint64) (prev uint64, rejected bool) {
	prev, rejected = d.Observe(jid, epoch)
	if rejected {
		return prev, true
	}
	if err := d.Flush(); err != nil {
		d.logger.Warn("dedup state synchronous save failed; duplicate protection is not crash-safe until the next save",
			"path", d.path, "error", err)
		d.mu.Lock()
		d.scheduleSaveLocked()
		d.mu.Unlock()
	}
	return prev, false
}

// Len reports the number of tracked JIDs.
func (d *dedupTracker) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.m)
}

// scheduleSaveLocked arms the debounced save timer. Must be called with d.mu
// held. A failed save is logged at Debug and retried on the next change —
// dedup persistence is best-effort hardening, never a hot-path failure.
func (d *dedupTracker) scheduleSaveLocked() {
	if d.timer != nil || d.path == "" {
		return
	}
	d.timer = time.AfterFunc(d.saveDelay, func() {
		if err := d.Flush(); err != nil {
			d.logger.Debug("dedup state save failed", "path", d.path, "error", err)
		}
	})
}

// Flush persists the current state synchronously (msgpack, 0600, atomic
// rename) and disarms any pending debounced save. Called by the debounce
// timer and once on shutdown.
func (d *dedupTracker) Flush() error {
	d.mu.Lock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if d.path == "" {
		d.mu.Unlock()
		return nil
	}
	snapshot := make(map[string]uint64, len(d.m))
	for k, v := range d.m {
		snapshot[k] = v
	}
	path := d.path
	d.mu.Unlock()

	data, err := bus.Encode(snapshot)
	if err != nil {
		return fmt.Errorf("peeld: encode dedup state: %w", err)
	}
	return writeFileAtomic(path, data, 0o600)
}
