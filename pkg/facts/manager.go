package facts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/nirnx/zester/pkg/bus"
)

// ManagerConfig configures the facts manager.
type ManagerConfig struct {
	// PeelID is the unique identifier for this peel.
	PeelID string

	// JS is the JetStream context for KV operations.
	JS bus.JetStreamAPI

	// Collectors is the list of fact collectors to run.
	Collectors []Collector

	// Logger is the structured logger. Defaults to slog.Default().
	Logger *slog.Logger
}

// Manager runs fact collectors and publishes results to NATS KV.
type Manager struct {
	peelID     string
	js         bus.JetStreamAPI
	kv         bus.KV
	collectors []Collector
	logger     *slog.Logger

	mu       sync.RWMutex
	facts    Facts
	rootKeys map[string][]string // collector name → top-level keys set by root-merging collectors
	lastHash [sha256.Size]byte   // hash of last published facts (sorted-key msgpack)

	cancel context.CancelFunc
	done   chan struct{}
}

// NewManager creates a facts manager. Call Start to begin collection.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.PeelID == "" {
		return nil, fmt.Errorf("facts: PeelID is required")
	}
	if cfg.JS == nil {
		return nil, fmt.Errorf("facts: JetStream context is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Manager{
		peelID:     cfg.PeelID,
		js:         cfg.JS,
		collectors: cfg.Collectors,
		logger:     cfg.Logger,
		facts:      make(Facts),
		rootKeys:   make(map[string][]string),
		done:       make(chan struct{}),
	}, nil
}

// Start runs an initial collection of all facts, publishes to KV,
// and starts background goroutines for collectors with Interval() > 0.
// It is Collect followed by StartPublishing; offline-first callers (the peel
// daemon) invoke the two halves separately so local fact collection works
// while NATS is unreachable.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.Collect(ctx); err != nil {
		return err
	}
	return m.StartPublishing(ctx)
}

// Collect runs all collectors once, merging results into the in-memory facts
// map without touching NATS. It is the local half of Start: the peel calls it
// at boot so provider detection, templating, and the scheduler have real
// facts even while the control plane is down, then calls StartPublishing once
// the connection is up.
func (m *Manager) Collect(ctx context.Context) error {
	if err := m.collectAll(ctx); err != nil {
		return fmt.Errorf("facts: initial collection: %w", err)
	}
	return nil
}

// StartPublishing resolves the facts KV bucket, publishes the current facts,
// and starts background goroutines for collectors with Interval() > 0 (they
// re-publish on change). This is the NATS-dependent half of Start and is safe
// to retry until it returns nil: the background collectors are only started
// once, after the bucket lookup and initial publish both succeed.
func (m *Manager) StartPublishing(ctx context.Context) error {
	kv, err := bus.GetBucket(ctx, m.js, bus.BucketFacts)
	if err != nil {
		return fmt.Errorf("facts: get facts bucket: %w", err)
	}
	m.kv = kv

	// Publish initial facts.
	if err := m.publish(ctx); err != nil {
		return fmt.Errorf("facts: initial publish: %w", err)
	}

	// Start scheduled collectors.
	childCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	for _, c := range m.collectors {
		if c.Interval() > 0 {
			go m.scheduleCollector(childCtx, c)
		}
	}

	return nil
}

// Stop cancels background collection goroutines.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
}

// SetFact sets a single fact key/value that will be published alongside
// collector-gathered facts. Useful for injecting static facts like
// the peel's curve public key.
func (m *Manager) SetFact(key string, value any) {
	m.mu.Lock()
	m.facts[key] = value
	m.mu.Unlock()
}

// GetFacts returns a copy of the current aggregated facts.
func (m *Manager) GetFacts() Facts {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make(Facts, len(m.facts))
	for k, v := range m.facts {
		cp[k] = v
	}
	return cp
}

// collectAll runs all collectors and merges results into the facts map.
func (m *Manager) collectAll(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, c := range m.collectors {
		data, err := c.Collect(ctx)
		if err != nil {
			m.logger.Warn("collector failed",
				"collector", c.Name(),
				"error", err,
			)
			continue
		}
		m.mergeCollectorData(c, data)
	}
	return nil
}

// collectOne runs a single collector and merges its results.
func (m *Manager) collectOne(ctx context.Context, c Collector) error {
	data, err := c.Collect(ctx)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.mergeCollectorData(c, data)
	m.mu.Unlock()

	return nil
}

// mergeCollectorData stores collector output in the facts map.
// If the collector implements RootMerger, its keys are merged at the
// top level (like Salt's /etc/salt/grains). Otherwise, they are nested
// under the collector's Name(). Must be called with m.mu held.
func (m *Manager) mergeCollectorData(c Collector, data map[string]any) {
	if rm, ok := c.(RootMerger); ok && rm.MergeAtRoot() {
		name := c.Name()
		// Remove previously-tracked keys from this collector.
		for _, k := range m.rootKeys[name] {
			delete(m.facts, k)
		}
		// Merge new keys at root level and track them.
		keys := make([]string, 0, len(data))
		for k, v := range data {
			m.facts[k] = v
			keys = append(keys, k)
		}
		m.rootKeys[name] = keys
	} else {
		m.facts[c.Name()] = data
	}
}

// publish writes the current facts to the NATS KV bucket.
// Skips the write if facts are identical to the last publish,
// avoiding unnecessary KV revisions that would trigger downstream watchers.
func (m *Manager) publish(ctx context.Context) error {
	m.mu.RLock()
	factsCopy := make(Facts, len(m.facts))
	for k, v := range m.facts {
		factsCopy[k] = v
	}
	m.mu.RUnlock()

	hash := hashFacts(factsCopy)

	m.mu.Lock()
	if hash == m.lastHash {
		m.mu.Unlock()
		m.logger.Debug("facts unchanged, skipping publish", "peel_id", m.peelID)
		return nil
	}
	m.lastHash = hash
	m.mu.Unlock()

	_, err := bus.KVPut(ctx, m.kv, m.peelID, factsCopy)
	if err != nil {
		return fmt.Errorf("facts: publish to KV: %w", err)
	}

	m.logger.Debug("facts published", "peel_id", m.peelID)
	return nil
}

// hashFacts produces a deterministic SHA-256 hash of a Facts map by
// encoding with sorted map keys, removing Go map iteration randomness.
func hashFacts(f Facts) [sha256.Size]byte {
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	enc.SetSortMapKeys(true)
	_ = enc.Encode(f)
	return sha256.Sum256(buf.Bytes())
}

// scheduleCollector runs a collector on its configured interval.
func (m *Manager) scheduleCollector(ctx context.Context, c Collector) {
	ticker := time.NewTicker(c.Interval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.collectOne(ctx, c); err != nil {
				m.logger.Warn("scheduled collector failed",
					"collector", c.Name(),
					"error", err,
				)
				continue
			}
			if err := m.publish(ctx); err != nil {
				m.logger.Warn("scheduled publish failed",
					"collector", c.Name(),
					"error", err,
				)
			}
		}
	}
}

// WatchFunc is called when a peel's facts change in the KV store.
type WatchFunc func(peelID string, facts Facts)

// Watch starts watching the facts KV bucket for changes.
// The callback is invoked for each update (delete and purge markers are
// skipped). Call the returned cancel function to stop.
// This is intended for the master side. The watcher automatically reconnects
// with exponential backoff if the JetStream consumer is lost (e.g., NATS cluster failure).
func Watch(ctx context.Context, js bus.JetStreamAPI, fn WatchFunc, logger *slog.Logger) (context.CancelFunc, error) {
	return watchFactsEntries(ctx, js, func(entry bus.KVEntry) {
		if entry.Operation() == bus.KVOpDelete ||
			entry.Operation() == bus.KVOpPurge {
			return
		}

		var facts Facts
		if err := bus.Decode(entry.Value(), &facts); err != nil {
			return
		}
		fn(entry.Key(), facts)
	}, nil, logger)
}
