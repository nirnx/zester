package settings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/template"
)

// Resolver performs peel-side settings resolution: loading raw .zy files
// from a shared KV bucket, evaluating targeting, rendering templates locally,
// and decrypting secrets. This offloads template compilation from the master.
type Resolver struct {
	peelID    string
	js        bus.JetStreamAPI
	engine    *template.Engine
	encryptor *auth.Encryptor
	matcher   TargetMatcher
	logger    *slog.Logger

	// mu guards senderPub and the revision cache below. InvalidateCache and
	// SetSenderPub may be called from watcher goroutines (WatchSecrets,
	// WatchMasterCurvePub callbacks) concurrently with Resolve.
	mu        sync.Mutex
	senderPub string

	// Revision-based cache: Resolve() skips KV reads when _revision
	// in settings-files hasn't changed since the last successful resolve.
	// These fields are only written on full success — no error path caches,
	// so a failed resolve (e.g., unresolved secret placeholders) is never
	// served from cache later.
	cachedResult   map[string]any
	cachedRevision uint64

	// invalidationGen increments on every InvalidateCache call. Resolve
	// snapshots it at entry and refuses to write the cache if it changed
	// mid-flight, so an invalidation (secrets rotation, curve key change)
	// that races an in-flight Resolve is never wiped out by that resolve
	// caching its pre-invalidation inputs.
	invalidationGen uint64
}

// ResolverConfig configures the peel-side settings resolver.
type ResolverConfig struct {
	// PeelID is this peel's unique identifier.
	PeelID string

	// JS is the JetStream context for KV access.
	JS bus.JetStreamAPI

	// Engine is the template engine for rendering .zy files.
	Engine *template.Engine

	// Encryptor decrypts secrets encrypted for this peel.
	Encryptor *auth.Encryptor

	// SenderPub is the master's curve public key for decryption.
	SenderPub string

	// Matcher evaluates targeting patterns. Defaults to SimpleTargetMatcher.
	Matcher TargetMatcher

	// Logger is the structured logger. Defaults to slog.Default().
	Logger *slog.Logger
}

func (c *ResolverConfig) defaults() {
	if c.Matcher == nil {
		c.Matcher = &SimpleTargetMatcher{}
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// NewResolver creates a peel-side settings resolver.
func NewResolver(cfg ResolverConfig) (*Resolver, error) {
	cfg.defaults()

	if cfg.PeelID == "" {
		return nil, fmt.Errorf("settings: PeelID is required")
	}
	if cfg.JS == nil {
		return nil, fmt.Errorf("settings: JetStream context is required")
	}
	if cfg.Engine == nil {
		return nil, fmt.Errorf("settings: template engine is required")
	}

	return &Resolver{
		peelID:    cfg.PeelID,
		js:        cfg.JS,
		engine:    cfg.Engine,
		encryptor: cfg.Encryptor,
		senderPub: cfg.SenderPub,
		matcher:   cfg.Matcher,
		logger:    cfg.Logger,
	}, nil
}

// SenderPub returns the current master curve public key.
func (r *Resolver) SenderPub() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.senderPub
}

// SetSenderPub sets the master's curve public key used for decrypting secrets.
// Call this after discovering the master's key (e.g., from the master-curve-pub
// KV entry). Changing the key invalidates the resolver cache.
func (r *Resolver) SetSenderPub(pub string) {
	r.mu.Lock()
	if pub == r.senderPub {
		r.mu.Unlock()
		return
	}
	r.senderPub = pub
	r.mu.Unlock()
	r.InvalidateCache()
}

// InvalidateCache forces the next Resolve() call to re-read from KV, even if
// the settings-files _revision hasn't changed. Call it whenever a resolution
// input outside the settings-files bucket changes:
//
//   - after master curve key rotation (SetSenderPub does this automatically);
//   - from the secrets-watch callback (WatchSecrets), so per-peel secrets that
//     arrive or rotate without a settings-files revision bump are picked up on
//     the next resolve instead of being swallowed by the revision gate.
//
// Safe for concurrent use with Resolve.
func (r *Resolver) InvalidateCache() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cachedResult = nil
	r.cachedRevision = 0
	r.invalidationGen++
}

// Resolve performs the full peel-side settings resolution:
//  1. Loads top.zy from the settings-files KV bucket
//  2. Parses top.zy and resolves which files apply to this peel
//  3. Loads only the matching .zy files from KV (selective loading)
//  4. Renders matching templates with local facts
//  5. Loads and decrypts per-peel secrets
//  6. Merges secrets into the rendered settings
//
// If any __ZESTER_SECRET:...__ placeholders survive the final merge (secrets
// not yet published, or missing keys), Resolve returns an error naming the
// affected secret keys and does NOT cache the result — a later resolve after
// the secrets arrive must not be served the poisoned cache.
func (r *Resolver) Resolve(ctx context.Context, facts map[string]any) (map[string]any, error) {
	// Revision gate: if _revision in settings-files hasn't changed since
	// the last resolve, return the cached result. This prevents reading
	// partially-written files when re-resolve is triggered by unrelated
	// KV events (e.g., secrets watcher, WatchAll fallback).
	//
	// revAtStart is sampled ONCE here, before any manifest/file/secret
	// reads, and is the only revision the cache may be keyed on: it is
	// conservative (can only lag the batch actually read, never lead it),
	// so a publish landing mid-resolve can never pin an old batch under a
	// newer revision number.
	sfKV, _ := bus.GetBucket(ctx, r.js, bus.BucketSettingsFiles)
	r.mu.Lock()
	genAtStart := r.invalidationGen
	r.mu.Unlock()
	var revAtStart uint64
	if sfKV != nil {
		revAtStart = bus.GetRevision(ctx, sfKV)
		r.mu.Lock()
		if revAtStart > 0 && revAtStart == r.cachedRevision && r.cachedResult != nil {
			cached := r.cachedResult
			r.mu.Unlock()
			r.logger.Debug("settings resolve cache hit", "revision", revAtStart)
			return cached, nil
		}
		r.mu.Unlock()
	}

	// Load the publish manifest. It defines batch membership and per-file
	// hashes, giving torn-read detection: every file loaded below must be
	// listed with a matching hash. Every publish writes it before bumping
	// _revision, so its absence is only legitimate before the master's very
	// first publish — when no settings content exists either. loadTopFile
	// enforces that: settings content without a manifest fails the resolve.
	manifest, err := r.loadManifest(ctx)
	if err != nil {
		return nil, fmt.Errorf("settings: load manifest: %w", err)
	}

	// Load only top.zy from KV (single read instead of listing all keys).
	topData, err := r.loadTopFile(ctx, manifest)
	if err != nil {
		return nil, fmt.Errorf("settings: load top.zy: %w", err)
	}
	if topData == nil {
		r.logger.Debug("no top.zy found in settings files")
		return map[string]any{}, nil
	}

	top, err := ParseTopFile(topData)
	if err != nil {
		return nil, fmt.Errorf("settings: parse top.zy: %w", err)
	}

	// Resolve which settings files apply to this peel.
	refs := top.ResolveForPeel(r.peelID, facts, r.matcher)
	if len(refs) == 0 {
		r.logger.Debug("no settings matched for peel", "peel", r.peelID)
		return map[string]any{}, nil
	}

	// Load only matching files from KV (selective loading).
	rawFiles, err := r.loadMatchingFiles(ctx, refs, manifest)
	if err != nil {
		return nil, fmt.Errorf("settings: load matching files: %w", err)
	}

	// Load and decrypt per-peel secrets up front so cross-file references
	// (e.g., {{ settings.db_password }}) see real values, not placeholders.
	secrets, err := r.loadSecrets(ctx)
	if err != nil {
		r.logger.Warn("failed to load secrets, continuing without", "peel", r.peelID, "error", err)
	}

	// Render and merge matched files.
	merged := map[string]any{}
	for _, ref := range refs {
		filePath := resolveRefToKVKey(ref)
		data, ok := rawFiles[filePath]
		if !ok {
			continue // already warned in loadMatchingFiles
		}

		rendered, err := r.engine.RenderString(filePath, string(data), template.RenderContext{
			Facts:    facts,
			Settings: merged,
		})
		if err != nil {
			return nil, fmt.Errorf("settings: render %q for peel %s: %w", ref, r.peelID, err)
		}

		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(rendered), &parsed); err != nil {
			return nil, fmt.Errorf("settings: parse rendered %q: %w", ref, err)
		}
		merged = MergeSettings(merged, parsed)

		// Replace secret placeholders immediately so the next file's
		// template rendering sees decrypted values.
		if len(secrets) > 0 {
			merged = mergeSecrets(merged, secrets)
		}
	}

	// Treat surviving secret placeholders as a resolve failure. Caching a
	// result that still contains literal __ZESTER_SECRET:...__ strings would
	// poison the revision-gated cache: services would be configured with
	// placeholder values, and a later re-resolve (after the secrets arrive)
	// would be served the stale cache.
	if unresolved := findUnresolvedSecretKeys(merged); len(unresolved) > 0 {
		return nil, fmt.Errorf("settings: resolve for peel %s: unresolved secret placeholders for keys: %s",
			r.peelID, strings.Join(unresolved, ", "))
	}

	r.logger.Info("peel-side settings resolved",
		"peel", r.peelID,
		"refs", len(refs),
		"keys", len(merged),
	)

	// Cache result keyed by the entry-sampled _revision so subsequent
	// calls with the same revision short-circuit without KV reads. This is
	// the ONLY place the cache is written: every error path above returns
	// without caching, so failed resolves are never served from cache.
	// Skipped when:
	//   - InvalidateCache ran mid-flight — this resolve read its inputs
	//     before the invalidation event (e.g. a secrets rotation), so
	//     caching it would silently pin the pre-rotation values;
	//   - _revision changed mid-flight — a publish completed while this
	//     resolve was loading/rendering (rendering can take 60s+ when
	//     templates invoke execution modules). The data read is a clean OLD
	//     batch (manifest-verified), so caching it under revAtStart would
	//     be harmless — but caching it under the NEW revision would pin the
	//     old batch for the whole next generation. Skip entirely and let
	//     the pending watch-triggered re-resolve run against the settled
	//     new revision.
	if sfKV != nil {
		revNow := bus.GetRevision(ctx, sfKV)
		r.mu.Lock()
		switch {
		case r.invalidationGen != genAtStart:
			r.logger.Debug("settings resolve result not cached: invalidated mid-flight")
		case revNow != revAtStart:
			r.logger.Debug("settings resolve result not cached: revision changed mid-flight",
				"entry_revision", revAtStart, "current_revision", revNow)
		default:
			r.cachedResult = merged
			r.cachedRevision = revAtStart
		}
		r.mu.Unlock()
	}

	return merged, nil
}

// findUnresolvedSecretKeys deep-scans a resolved settings value for surviving
// secret placeholders (SecretPlaceholderPrefix markers) in nested maps, slices,
// and string values — including placeholders embedded inside larger strings
// (e.g., rendered into a connection string). It returns the sorted,
// deduplicated secret keys referenced by the surviving placeholders.
func findUnresolvedSecretKeys(v any) []string {
	seen := make(map[string]struct{})
	scanUnresolvedSecrets(v, seen)
	if len(seen) == 0 {
		return nil
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// scanUnresolvedSecrets recursively walks maps, slices, and strings,
// collecting the secret keys of any placeholder markers found.
func scanUnresolvedSecrets(v any, seen map[string]struct{}) {
	switch val := v.(type) {
	case string:
		for _, key := range extractPlaceholderKeys(val) {
			seen[key] = struct{}{}
		}
	case map[string]any:
		for _, child := range val {
			scanUnresolvedSecrets(child, seen)
		}
	case map[any]any:
		for _, child := range val {
			scanUnresolvedSecrets(child, seen)
		}
	case []any:
		for _, child := range val {
			scanUnresolvedSecrets(child, seen)
		}
	}
}

// extractPlaceholderKeys returns the secret keys of all placeholder markers
// found in s, at any position. A marker without a closing suffix reports the
// remainder of the string as the key so the error still names something useful.
func extractPlaceholderKeys(s string) []string {
	var keys []string
	for {
		start := strings.Index(s, SecretPlaceholderPrefix)
		if start < 0 {
			return keys
		}
		rest := s[start+len(SecretPlaceholderPrefix):]
		end := strings.Index(rest, SecretPlaceholderSuffix)
		if end < 0 {
			if rest == "" {
				rest = "(unknown)"
			}
			return append(keys, rest)
		}
		key := rest[:end]
		if key == "" {
			key = "(unknown)"
		}
		keys = append(keys, key)
		s = rest[end+len(SecretPlaceholderSuffix):]
	}
}

// loadManifest reads the publish manifest from the settings-files KV bucket.
// Returns a nil map (and no error) only when the _manifest key is absent —
// legitimate solely before the master's very first publish; a manifest that
// exists always yields a non-nil map, even when it lists zero files.
// A present-but-undecodable manifest is an error: the bucket is mid-write or
// corrupt, and resolving against it would risk a torn snapshot.
func (r *Resolver) loadManifest(ctx context.Context) (map[string]string, error) {
	kv, err := bus.GetBucket(ctx, r.js, bus.BucketSettingsFiles)
	if err != nil {
		return nil, fmt.Errorf("get settings-files bucket: %w", err)
	}

	var entries []ManifestEntry
	if err := bus.KVGet(ctx, kv, ManifestKey, &entries); err != nil {
		if errors.Is(err, bus.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get %s: %w", ManifestKey, err)
	}

	m := make(map[string]string, len(entries))
	for _, e := range entries {
		m[e.Key] = e.SHA256
	}
	return m, nil
}

// verifyAgainstManifest checks one loaded file against the publish manifest.
// A file that is loaded must be listed with a matching content hash;
// anything else means the bucket is mid-publish (torn read) or holds stale
// content, and the resolve must fail rather than render a mixed snapshot.
func verifyAgainstManifest(manifest map[string]string, key string, data []byte) error {
	wantHash, listed := manifest[key]
	if !listed {
		return fmt.Errorf("file %q present in KV but not listed in manifest (stale or torn publish)", key)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != wantHash {
		return fmt.Errorf("file %q content hash %s does not match manifest %s (torn publish)", key, got, wantHash)
	}
	return nil
}

// loadTopFile reads only the top.zy file from the settings-files KV bucket.
// Returns nil data (not an error) if top.zy does not exist and the manifest
// does not list it — including when no manifest exists at all, the clean
// "before the master's first publish" state. A present top.zy without a
// manifest is an error: every publish writes the manifest before the
// revision bump, so the bucket is torn or tampered and no batch
// verification is possible. With a manifest, top.zy is verified against it
// like any other file.
func (r *Resolver) loadTopFile(ctx context.Context, manifest map[string]string) ([]byte, error) {
	kv, err := bus.GetBucket(ctx, r.js, bus.BucketSettingsFiles)
	if err != nil {
		return nil, fmt.Errorf("get settings-files bucket: %w", err)
	}

	entry, err := kv.Get(ctx, "top.zy")
	if err != nil {
		if errors.Is(err, bus.ErrKeyNotFound) {
			if _, listed := manifest["top.zy"]; listed {
				return nil, fmt.Errorf("file %q listed in manifest but missing from KV (torn publish)", "top.zy")
			}
			return nil, nil
		}
		return nil, fmt.Errorf("get top.zy: %w", err)
	}

	if manifest == nil {
		return nil, fmt.Errorf("file %q present in KV but %s key missing (torn or tampered bucket)", "top.zy", ManifestKey)
	}
	if err := verifyAgainstManifest(manifest, "top.zy", entry.Value()); err != nil {
		return nil, err
	}

	return entry.Value(), nil
}

// loadMatchingFiles reads only the KV entries that correspond to the
// resolved settings references. Every loaded file must be listed in the
// manifest with a matching hash, and a listed file that is missing from KV
// is an error (torn-read protection); only files that are absent from BOTH
// KV and the manifest get warn-and-skip behavior. The manifest is always
// non-nil here: loadTopFile already failed the resolve if settings content
// exists without one.
func (r *Resolver) loadMatchingFiles(ctx context.Context, refs []string, manifest map[string]string) (map[string][]byte, error) {
	kv, err := bus.GetBucket(ctx, r.js, bus.BucketSettingsFiles)
	if err != nil {
		return nil, fmt.Errorf("get settings-files bucket: %w", err)
	}

	files := make(map[string][]byte, len(refs))
	for _, ref := range refs {
		key := resolveRefToKVKey(ref)
		entry, err := kv.Get(ctx, key)
		if err != nil {
			if errors.Is(err, bus.ErrKeyNotFound) {
				if _, listed := manifest[key]; listed {
					return nil, fmt.Errorf("file %q listed in manifest but missing from KV (torn publish)", key)
				}
				r.logger.Warn("settings file not found in KV", "ref", ref, "key", key)
				continue
			}
			return nil, fmt.Errorf("get settings file %q: %w", key, err)
		}

		if err := verifyAgainstManifest(manifest, key, entry.Value()); err != nil {
			return nil, err
		}

		files[key] = entry.Value()
	}

	return files, nil
}

// loadSecrets loads and decrypts this peel's secrets from the secrets KV bucket.
func (r *Resolver) loadSecrets(ctx context.Context) (map[string]string, error) {
	senderPub := r.SenderPub()
	if r.encryptor == nil || senderPub == "" {
		return nil, nil
	}

	kv, err := bus.GetBucket(ctx, r.js, bus.BucketSecrets)
	if err != nil {
		return nil, fmt.Errorf("get secrets bucket: %w", err)
	}

	var encrypted map[string]string
	if err := bus.KVGet(ctx, kv, r.peelID, &encrypted); err != nil {
		return nil, fmt.Errorf("get secrets for peel %s: %w", r.peelID, err)
	}

	decrypted := make(map[string]string, len(encrypted))
	for key, val := range encrypted {
		if auth.IsEncryptedValue(val) {
			plaintext, err := r.encryptor.OpenSettingsValue(val, senderPub)
			if err != nil {
				return nil, fmt.Errorf("decrypt secret %q: %w", key, err)
			}
			decrypted[key] = string(plaintext)
		} else {
			decrypted[key] = val
		}
	}

	return decrypted, nil
}

// resolveRefToKVKey converts a settings reference (e.g., "common.base")
// to a KV key path (e.g., "common/base.zy").
func resolveRefToKVKey(ref string) string {
	parts := strings.Split(ref, ".")
	return strings.Join(parts, "/") + ".zy"
}

// mergeSecrets injects decrypted secret values into the settings map.
// Secret keys use dot notation for nested paths (e.g., "database.password").
func mergeSecrets(settings map[string]any, secrets map[string]string) map[string]any {
	result := make(map[string]any, len(settings))
	for k, v := range settings {
		result[k] = v
	}
	for dotPath, val := range secrets {
		setNestedValue(result, dotPath, val)
	}
	return result
}

// setNestedValue sets a value at a dot-separated path in a nested map.
func setNestedValue(m map[string]any, dotPath string, val any) {
	parts := strings.Split(dotPath, ".")
	current := m
	for i := 0; i < len(parts)-1; i++ {
		next, ok := current[parts[i]].(map[string]any)
		if !ok {
			next = make(map[string]any)
			current[parts[i]] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = val
}
