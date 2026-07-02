package basket

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/ptorbus/zester/pkg/bus"
)

// PublisherConfig configures the basket publisher.
type PublisherConfig struct {
	// PeelID is the peel's identifier, used as the key prefix.
	PeelID string

	// JS is the JetStream context for KV operations.
	JS bus.JetStreamAPI

	// Functions maps function names to their refresh intervals.
	// Function names use dot-notation matching fact keys
	// (e.g., "default_ipv4", "network.hostname").
	Functions map[string]time.Duration

	// Logger is the structured logger. Defaults to slog.Default().
	Logger *slog.Logger
}

// Publisher publishes peel facts to the basket KV bucket for cross-peel queries.
type Publisher struct {
	peelID    string
	js        bus.JetStreamAPI
	kv        bus.KV
	functions map[string]time.Duration
	logger    *slog.Logger

	// hashMu guards lastHash, which caches the hash of the last
	// successfully-published value per KV key. Refresh goroutines
	// (one per function) write to the map concurrently.
	hashMu   sync.Mutex
	lastHash map[string][sha256.Size]byte

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPublisher creates a basket publisher.
func NewPublisher(cfg PublisherConfig) (*Publisher, error) {
	if cfg.PeelID == "" {
		return nil, fmt.Errorf("basket: PeelID is required")
	}
	if cfg.JS == nil {
		return nil, fmt.Errorf("basket: JetStream context is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Publisher{
		peelID:    cfg.PeelID,
		js:        cfg.JS,
		functions: cfg.Functions,
		logger:    cfg.Logger,
		lastHash:  make(map[string][sha256.Size]byte),
	}, nil
}

// Start begins publishing basket data. It publishes all functions immediately,
// then starts a goroutine per function to refresh on the configured interval.
// factsFn is called to get the current facts map each time a publish is needed.
func (p *Publisher) Start(ctx context.Context, factsFn func() map[string]any) error {
	kv, err := bus.GetBucket(ctx, p.js, bus.BucketBasket)
	if err != nil {
		return fmt.Errorf("basket: get basket bucket: %w", err)
	}
	p.kv = kv

	// Initial publish of all functions.
	facts := factsFn()
	for name := range p.functions {
		published, err := p.publish(ctx, name, facts)
		if err != nil {
			p.logger.Warn("basket: initial publish failed", "function", name, "error", err)
		} else if published {
			p.logger.Info("basket published", "function", name, "key", p.peelID+"."+name)
		}
	}

	// Start periodic refresh goroutines.
	childCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel

	for name, interval := range p.functions {
		if interval <= 0 {
			interval = 5 * time.Minute
		}
		p.wg.Add(1)
		go p.refreshLoop(childCtx, name, interval, factsFn)
	}

	return nil
}

// Stop cancels all refresh goroutines and waits for them to finish.
func (p *Publisher) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
}

func (p *Publisher) refreshLoop(ctx context.Context, name string, interval time.Duration, factsFn func() map[string]any) {
	defer p.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := p.publish(ctx, name, factsFn()); err != nil {
				p.logger.Warn("basket: refresh publish failed", "function", name, "error", err)
			}
		}
	}
}

// publish looks up the basket value for one function and writes it to KV.
// The write is skipped when the value is identical to the last successful
// publish for the same key (mirrors the hash-skip in facts.Manager.publish),
// avoiding unnecessary KV revisions that would trigger downstream watchers.
// The cached hash is only updated after a successful put, so a failed write
// is retried on the next tick. Returns whether a KV write happened.
func (p *Publisher) publish(ctx context.Context, name string, facts map[string]any) (bool, error) {
	val := lookupDotKey(facts, name)
	if val == nil {
		p.logger.Debug("basket function value not found in facts", "function", name)
		return false, nil
	}
	key := p.peelID + "." + name
	hash := hashValue(val)

	p.hashMu.Lock()
	last, ok := p.lastHash[key]
	p.hashMu.Unlock()
	if ok && last == hash {
		p.logger.Debug("basket value unchanged, skipping publish", "function", name, "key", key)
		return false, nil
	}

	if _, err := bus.KVPut(ctx, p.kv, key, val); err != nil {
		return false, fmt.Errorf("basket: publish %q: %w", key, err)
	}

	p.hashMu.Lock()
	p.lastHash[key] = hash
	p.hashMu.Unlock()
	return true, nil
}

// hashValue produces a deterministic SHA-256 hash of a basket value by
// encoding with sorted map keys, removing Go map iteration randomness.
func hashValue(v any) [sha256.Size]byte {
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	enc.SetSortMapKeys(true)
	_ = enc.Encode(v)
	return sha256.Sum256(buf.Bytes())
}

// lookupDotKey traverses a nested map using dot-separated keys.
// For example, "network.hostname" looks up map["network"].(map[string]any)["hostname"].
// Single-segment keys like "default_ipv4" look up map["default_ipv4"] directly.
func lookupDotKey(m map[string]any, key string) any {
	parts := strings.Split(key, ".")
	var current any = m
	for _, part := range parts {
		cm, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = cm[part]
		if !ok {
			return nil
		}
	}
	return current
}

// ParseFunctions reads basket_functions from compiled settings and returns
// a map of function name to refresh interval.
// Settings format: basket_functions: {default_ipv4: "5m", network.hostname: "5m"}
func ParseFunctions(settings map[string]any) map[string]time.Duration {
	if settings == nil {
		return nil
	}
	raw, ok := settings["basket_functions"].(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]time.Duration, len(raw))
	for name, v := range raw {
		interval := 5 * time.Minute // default
		if s, ok := v.(string); ok {
			if d, err := time.ParseDuration(s); err == nil {
				interval = d
			}
		}
		result[name] = interval
	}
	return result
}
