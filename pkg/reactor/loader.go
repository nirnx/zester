package reactor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/settings"
)

// KeyManifest is the well-known KV key in the reactor-files bucket holding
// the published file-set manifest (same convention as the settings and
// state-files buckets). A bucket holding reactor files without a manifest is
// a torn or tampered publish and fails the load; an empty bucket (before the
// master's very first publish) is a clean no-op yielding an empty rule set.
const KeyManifest = "_manifest"

// RuleSource provides the current rule snapshot to the engine and the test
// service. Implementations must never return nil concurrently-mutated sets;
// the Loader satisfies it with atomically swapped immutable snapshots.
type RuleSource interface {
	RuleSet() *RuleSet
}

// manifestEntry mirrors settings.ManifestEntry: one published file's KV key
// and the SHA-256 hex digest of its stored bytes.
type manifestEntry struct {
	Key    string `msgpack:"key"`
	SHA256 string `msgpack:"sha256"`
}

// decodeManifest accepts both manifest wire shapes in use in this codebase:
// the settings-style bare sorted list ([]ManifestEntry) and the
// statefiles-style wrapper (Manifest{files: [...]}) — the masterd publisher
// reuses one of the two existing publishers, so the loader tolerates either.
func decodeManifest(data []byte) ([]manifestEntry, error) {
	var entries []manifestEntry
	if err := bus.Decode(data, &entries); err == nil {
		return entries, nil
	}
	var wrapped struct {
		Files []manifestEntry `msgpack:"files"`
	}
	if err := bus.Decode(data, &wrapped); err == nil {
		return wrapped.Files, nil
	}
	return nil, fmt.Errorf("reactor: manifest is neither a []ManifestEntry list nor a {files: [...]} wrapper")
}

// LoaderConfig configures a Loader.
type LoaderConfig struct {
	// KV is the reactor-files bucket (bus.BucketReactorFiles).
	KV bus.KV

	// Logger defaults to slog.Default().
	Logger *slog.Logger

	// Debounce coalesces rapid _revision bumps into one reload.
	// Default: 2s.
	Debounce time.Duration

	// Jitter is the maximum random delay added after the debounce window
	// (thundering-herd damping across masters). Default: 5s.
	Jitter time.Duration

	// OnRulesLoaded fires after every successful load/swap with the number
	// of active rules (zester_reactor_rules_loaded gauge).
	OnRulesLoaded func(n int)

	// OnRuleError fires on every failed load (zester_reactor_rule_errors_total).
	OnRuleError func()
}

// Loader keeps an atomically swapped RuleSet loaded from the reactor-files
// KV bucket: it lists the bucket, verifies every loaded file against the
// _manifest (sha256), parses reactor/top.zy, compiles the rules, and watches
// _revision for debounced hot reloads. ANY parse/verify error keeps the
// last-known-good snapshot (Warn + OnRuleError) — the loader never crashes
// and never half-applies.
type Loader struct {
	kv            bus.KV
	logger        *slog.Logger
	debounce      time.Duration
	jitter        time.Duration
	onRulesLoaded func(int)
	onRuleError   func()

	mu      sync.RWMutex
	current *RuleSet
	lastErr error
}

// NewLoader creates a Loader. Call Start to perform the initial load and
// begin watching for updates, or Load for a one-shot load.
func NewLoader(cfg LoaderConfig) (*Loader, error) {
	if cfg.KV == nil {
		return nil, fmt.Errorf("reactor: loader: KV is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Debounce == 0 {
		cfg.Debounce = 2 * time.Second
	}
	if cfg.Jitter == 0 {
		cfg.Jitter = 5 * time.Second
	}
	return &Loader{
		kv:            cfg.KV,
		logger:        logger,
		debounce:      cfg.Debounce,
		jitter:        cfg.Jitter,
		onRulesLoaded: cfg.OnRulesLoaded,
		onRuleError:   cfg.OnRuleError,
		current:       &RuleSet{Files: map[string][]byte{}},
	}, nil
}

// RuleSet returns the current immutable snapshot (never nil; empty before
// the first successful load).
func (l *Loader) RuleSet() *RuleSet {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.current
}

// LastError returns the error from the most recent load attempt, or nil when
// the last load succeeded. Backs the masterd readiness check's degraded
// state ("running on last-known-good rules").
func (l *Loader) LastError() error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.lastErr
}

// Start performs the initial load (failure is non-fatal: the empty/LKG set
// stays active and the error is retained for LastError) and starts the
// _revision watch goroutine, which triggers debounced reloads until ctx is
// cancelled.
func (l *Loader) Start(ctx context.Context) {
	if err := l.Load(ctx); err != nil {
		l.logger.Warn("reactor: initial rule load failed; starting with last-known-good rules", "error", err)
	}

	deb := settings.NewDebouncedFunc(settings.DebouncedFuncConfig{
		Fn: func() {
			if ctx.Err() != nil {
				return
			}
			// Load logs + counts its own failures.
			_ = l.Load(ctx)
		},
		Debounce: l.debounce,
		Jitter:   l.jitter,
	})

	go l.watchRevision(ctx, deb)
}

// watchRevision watches the bucket's _revision key and triggers the
// debounced reload on every put. Watcher loss reconnects with capped
// backoff; a fresh watcher's replay also triggers a reload, closing the gap
// between the initial load and the watch attach.
func (l *Loader) watchRevision(ctx context.Context, deb *settings.DebouncedFunc) {
	defer deb.Stop()

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		watcher, err := l.kv.Watch(ctx, bus.KeyRevision)
		if err != nil {
			l.logger.Warn("reactor: revision watch failed, retrying", "backoff", backoff, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = time.Second

	updates:
		for {
			select {
			case <-ctx.Done():
				_ = watcher.Stop()
				return
			case entry, ok := <-watcher.Updates():
				if !ok {
					// Consumer lost; reconnect.
					_ = watcher.Stop()
					l.logger.Warn("reactor: revision watcher lost, reconnecting", "backoff", backoff)
					select {
					case <-ctx.Done():
						return
					case <-time.After(backoff):
					}
					break updates
				}
				if entry == nil {
					continue // end-of-replay sentinel
				}
				if entry.Operation() != bus.KVOpPut {
					continue
				}
				deb.Trigger()
			}
		}
	}
}

// Load performs one load + atomic swap. On error the last-known-good set is
// retained, the error is Warn-logged, counted via OnRuleError, and returned.
func (l *Loader) Load(ctx context.Context) error {
	rs, err := l.load(ctx)
	if err != nil {
		l.mu.Lock()
		l.lastErr = err
		l.mu.Unlock()
		l.logger.Warn("reactor: rule load failed; keeping last-known-good rules", "error", err)
		if l.onRuleError != nil {
			l.onRuleError()
		}
		return err
	}

	l.mu.Lock()
	l.current = rs
	l.lastErr = nil
	l.mu.Unlock()

	l.lintRules(rs)
	if l.onRulesLoaded != nil {
		l.onRulesLoaded(len(rs.Rules))
	}
	l.logger.Info("reactor: rules loaded", "rules", len(rs.Rules), "files", len(rs.Files))
	return nil
}

// load builds a fresh snapshot from the bucket without touching Loader state.
func (l *Loader) load(ctx context.Context) (*RuleSet, error) {
	keys, err := bus.ListKeysWithPrefix(ctx, l.kv, bus.WildcardMany)
	if err != nil {
		return nil, fmt.Errorf("reactor: list reactor-files keys: %w", err)
	}
	var fileKeys []string
	for _, k := range keys {
		if k == bus.KeyRevision || k == KeyManifest {
			continue
		}
		fileKeys = append(fileKeys, k)
	}

	entries, err := l.loadManifest(ctx, len(fileKeys))
	if err != nil {
		return nil, err
	}
	if entries == nil {
		// Empty bucket before the first publish: clean no-op.
		return &RuleSet{Files: map[string][]byte{}}, nil
	}

	files, err := l.fetchManifestFiles(ctx, entries)
	if err != nil {
		return nil, err
	}

	top, ok := files[TopKey]
	if !ok {
		if len(files) > 0 {
			l.logger.Warn("reactor: no top file published; reactor matches nothing", "key", TopKey, "files", len(files))
		}
		return &RuleSet{Files: files}, nil
	}

	rules, err := ParseTopFile(top)
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		if _, ok := files[RefPath(r.Ref)]; !ok {
			return nil, fmt.Errorf("reactor: rule %q references missing reaction file %s", r.Ref, RefPath(r.Ref))
		}
	}
	return &RuleSet{Rules: rules, Files: files}, nil
}

// loadManifest fetches and decodes the _manifest key. A missing manifest is
// legitimate only when the bucket holds no file content (pre-first-publish);
// content without a manifest is a torn or tampered publish. A nil, error-free
// return means "empty bucket".
func (l *Loader) loadManifest(ctx context.Context, fileKeyCount int) ([]manifestEntry, error) {
	entry, err := l.kv.Get(ctx, KeyManifest)
	if err != nil {
		if errors.Is(err, bus.ErrKeyNotFound) {
			if fileKeyCount > 0 {
				return nil, fmt.Errorf("reactor: bucket holds %d file keys but no %s key (torn or tampered publish)", fileKeyCount, KeyManifest)
			}
			return nil, nil
		}
		return nil, fmt.Errorf("reactor: get %s: %w", KeyManifest, err)
	}
	entries, err := decodeManifest(entry.Value())
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []manifestEntry{}
	}
	return entries, nil
}

// fetchManifestFiles fetches EXACTLY the manifest's file set, verifying each
// file's SHA-256 against its manifest entry. Listed-but-missing and hash
// mismatches fail the load (torn-read protection); bucket keys not listed in
// the manifest are inert prune leftovers and are ignored.
func (l *Loader) fetchManifestFiles(ctx context.Context, entries []manifestEntry) (map[string][]byte, error) {
	files := make(map[string][]byte, len(entries))
	for _, me := range entries {
		if err := validateFileKey(me.Key); err != nil {
			return nil, err
		}
		entry, err := l.kv.Get(ctx, me.Key)
		if err != nil {
			if errors.Is(err, bus.ErrKeyNotFound) {
				return nil, fmt.Errorf("reactor: manifest lists %s but the key is missing (torn publish)", me.Key)
			}
			return nil, fmt.Errorf("reactor: get %s: %w", me.Key, err)
		}
		sum := sha256.Sum256(entry.Value())
		if got := hex.EncodeToString(sum[:]); got != me.SHA256 {
			return nil, fmt.Errorf("reactor: file %s hash mismatch: manifest %s, stored %s (torn or tampered publish)", me.Key, me.SHA256, got)
		}
		files[me.Key] = entry.Value()
	}
	return files, nil
}

// validateFileKey rejects manifest keys that are meta keys or could escape
// the namespace when mapped to paths.
func validateFileKey(key string) error {
	if key == "" || key == bus.KeyRevision || key == KeyManifest {
		return fmt.Errorf("reactor: manifest lists invalid file key %q", key)
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("reactor: manifest lists absolute file key %q", key)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == ".." {
			return fmt.Errorf("reactor: manifest lists path-escaping file key %q", key)
		}
	}
	return nil
}

var (
	// enrollActionRe detects enroll.* action blocks in raw (pre-render)
	// reaction file text — a lint, not an authorization gate (the executor
	// enforces the real origin/require_peel gates).
	enrollActionRe = regexp.MustCompile(`(?m)^\s*enroll\.(approve|reject|revoke)\s*:`)

	// requirePeelStarRe detects a bare-star require_peel in raw reaction
	// file text (both the map form and the "- require_peel: ..." list-item
	// form).
	requirePeelStarRe = regexp.MustCompile(`(?m)^\s*(-\s+)?require_peel\s*:\s*['"]?\*['"]?\s*(#.*)?$`)
)

// lintRules emits amendment-10 warnings after a successful load: an
// enroll-referencing rule whose match glob is a bare star, and reaction
// files whose require_peel is a bare star. Lints never fail the load.
func (l *Loader) lintRules(rs *RuleSet) {
	for _, r := range rs.Rules {
		src, ok := rs.File(r.Ref)
		if !ok || !enrollActionRe.Match(src) {
			continue
		}
		if r.Pattern == "*" {
			l.logger.Warn("reactor: enroll-referencing rule matches EVERY event (bare-star glob); scope it to _master/enroll/... keys",
				"rule", r.Ref, "pattern", r.Pattern)
		}
		if requirePeelStarRe.Match(src) {
			l.logger.Warn("reactor: enroll reaction uses a bare-star require_peel; it will approve ANY pending peel ID",
				"rule", r.Ref, "file", RefPath(r.Ref))
		}
	}
}
