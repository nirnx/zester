package statefiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/nirnx/zester/pkg/bus"
)

// Publisher walks a states directory and publishes .zy files to the
// state-files KV bucket. Used on the master side.
type Publisher struct {
	statesDir  string
	kv         bus.KV
	logger     *slog.Logger
	allowEmpty bool
	encode     func(key string, plaintext []byte) ([]byte, error)
	hashFn     func([]byte) string
}

// PublisherConfig configures the state file publisher.
type PublisherConfig struct {
	// StatesDir is the root directory containing .zy state files.
	StatesDir string

	// KV is the state-files KV bucket.
	KV bus.KV

	// Logger is the structured logger. Defaults to slog.Default().
	Logger *slog.Logger

	// AllowEmpty permits publishing an empty file set over a bucket that
	// still holds state files. An empty publish writes an empty _manifest,
	// which makes every peel prune its entire local cache — almost always a
	// misconfigured or not-yet-cloned states dir rather than intent, so it
	// is refused unless this is set.
	AllowEmpty bool

	// EncodeValue, when set, transforms each file's bytes at Put time (e.g.
	// sealing them to the account curve key for the master-settings
	// replica). The MANIFEST keeps hashing the PLAINTEXT as given, so the
	// hash-gate stays deterministic even when the encoding is randomized
	// (NaCl boxes differ on every seal). Consumers must apply the matching
	// CacheConfig.DecodeValue.
	EncodeValue func(key string, plaintext []byte) ([]byte, error)

	// HashValue overrides the per-file manifest hash (default: unkeyed
	// SHA-256). The sealed master-settings publisher passes a KEYED hash so
	// the plaintext-derived manifest is not an offline oracle for a $KV.>
	// reader. Consumers must set the matching CacheConfig.HashValue.
	HashValue func([]byte) string
}

// NewPublisher creates a state file publisher for the master.
func NewPublisher(cfg PublisherConfig) *Publisher {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	hashFn := cfg.HashValue
	if hashFn == nil {
		hashFn = HashFile
	}
	return &Publisher{
		statesDir:  cfg.StatesDir,
		kv:         cfg.KV,
		logger:     logger,
		allowEmpty: cfg.AllowEmpty,
		encode:     cfg.EncodeValue,
		hashFn:     hashFn,
	}
}

// Result describes one publish attempt.
type Result struct {
	// Files is the size of the (would-be) published file set.
	Files int

	// Changed reports whether anything was written: false means the computed
	// manifest was byte-identical to the bucket's _manifest, so the whole
	// publish — file puts, manifest, revision bump, stale-key prune — was
	// skipped and no peel resyncs.
	Changed bool
}

// Publish walks the states directory, reads all .zy files, and publishes
// them to the state-files KV bucket. The publish is hash-gated: when the
// computed manifest matches the bucket's, nothing is written (Result.Changed
// false) — which makes periodic and watcher-triggered republishes free.
func (p *Publisher) Publish(ctx context.Context) (Result, error) {
	files, err := loadStateFiles(p.statesDir)
	if err != nil {
		return Result{}, fmt.Errorf("statefiles: walk %s: %w", p.statesDir, err)
	}
	return p.publishFiles(ctx, files, false)
}

// PublishForce is Publish without the hash-gate: it rewrites every file key,
// the manifest, and the revision even when nothing changed. Used by the
// operator's explicit `zester fileserver update --force` — an unconditional
// re-put also self-heals a bucket whose file keys were tampered with or torn
// while the manifest stayed intact (a state the gate would otherwise skip).
func (p *Publisher) PublishForce(ctx context.Context) (Result, error) {
	files, err := loadStateFiles(p.statesDir)
	if err != nil {
		return Result{}, fmt.Errorf("statefiles: walk %s: %w", p.statesDir, err)
	}
	return p.publishFiles(ctx, files, true)
}

// PublishFiles publishes the given files map to the state-files KV bucket
// (hash-gated, like Publish). Keys are forward-slash-separated relative
// paths; values are raw file bytes.
func (p *Publisher) PublishFiles(ctx context.Context, files map[string][]byte) (Result, error) {
	return p.publishFiles(ctx, files, false)
}

// PublishFilesForce is PublishFiles without the hash-gate (see PublishForce).
func (p *Publisher) PublishFilesForce(ctx context.Context, files map[string][]byte) (Result, error) {
	return p.publishFiles(ctx, files, true)
}

// publishFiles implements the batch protocol: write all file keys, write the
// _manifest key describing the complete set, bump _revision (the signal peels
// watch), then best-effort delete bucket keys absent from the manifest. The
// manifest lands before the revision bump so a peel reacting to the bump
// always sees a manifest covering the batch; stale-key deletion is garbage
// collection only, because manifest-aware peels fetch exactly the manifest's
// file set.
//
// Unless force is set, the publish is skipped entirely when the computed
// manifest is byte-identical to the bucket's current _manifest.
func (p *Publisher) publishFiles(ctx context.Context, files map[string][]byte, force bool) (Result, error) {
	if len(files) == 0 && !p.allowEmpty {
		existing, err := p.bucketFileKeys(ctx)
		if err != nil {
			return Result{}, err
		}
		if len(existing) > 0 {
			return Result{}, fmt.Errorf("statefiles: refusing to publish empty file set over %d existing files (states dir empty or not yet cloned; set AllowEmpty to force)", len(existing))
		}
	}

	manifest := BuildManifestWith(files, p.hashFn)
	encoded, err := bus.Encode(manifest)
	if err != nil {
		return Result{}, fmt.Errorf("statefiles: encode manifest: %w", err)
	}

	// Hash-gate: BuildManifest sorts entries and msgpack encoding is
	// deterministic, so byte equality with the stored _manifest means the
	// exact same file set with the exact same content hashes. Equality alone
	// is not enough — the gate also verifies the matched publish actually
	// COMPLETED (see gateIntegrityOK); a torn or tampered bucket falls
	// through to a full repairing publish instead of being pinned forever.
	if !force {
		if cur, err := p.kv.Get(ctx, KeyManifest); err == nil && bytes.Equal(cur.Value(), encoded) &&
			p.gateIntegrityOK(ctx, cur, manifest) {
			p.logger.Debug("statefiles: publish skipped, manifest unchanged", "files", len(files))
			return Result{Files: len(files), Changed: false}, nil
		}
	}

	count := 0
	for key, data := range files {
		stored := data
		if p.encode != nil {
			var err error
			if stored, err = p.encode(key, data); err != nil {
				return Result{Files: count, Changed: true}, fmt.Errorf("statefiles: encode %s: %w", key, err)
			}
		}
		if _, err := p.kv.Put(ctx, key, stored); err != nil {
			return Result{Files: count, Changed: true}, fmt.Errorf("statefiles: publish %s: %w", key, err)
		}
		p.logger.Debug("published state file", "key", key, "size", len(stored))
		count++
	}

	if _, err := p.kv.Put(ctx, KeyManifest, encoded); err != nil {
		return Result{Files: count, Changed: true}, fmt.Errorf("statefiles: publish manifest: %w", err)
	}

	if err := bus.BumpRevision(ctx, p.kv); err != nil {
		return Result{Files: count, Changed: true}, fmt.Errorf("statefiles: bump revision: %w", err)
	}

	p.deleteStaleKeys(ctx, files)

	return Result{Files: count, Changed: true}, nil
}

// gateIntegrityOK verifies that a byte-identical stored manifest reflects a
// COMPLETED publish, so the hash-gate never pins a torn bucket:
//
//   - the _revision bump must have landed AFTER the manifest write (entry
//     revisions are bucket-monotonic). A publish interrupted between the
//     _manifest Put and BumpRevision would otherwise never notify peels —
//     every later gated publish would skip the bump forever;
//   - every manifest-listed file key must still exist: during the tolerated
//     dual-leader window, a losing master's stale-key prune can delete a key
//     the winning manifest lists, leaving peels unable to sync.
//
// Any verification failure (or inability to verify) falls through to a full
// publish — the safe direction. Content tampering with intact keys still
// needs an explicit --force (documented).
func (p *Publisher) gateIntegrityOK(ctx context.Context, manifestEntry bus.KVEntry, m Manifest) bool {
	revEntry, err := p.kv.Get(ctx, bus.KeyRevision)
	if err != nil || revEntry.Revision() <= manifestEntry.Revision() {
		p.logger.Info("statefiles: gate integrity check failed (revision bump missing or older than manifest); republishing")
		return false
	}
	keys, err := p.kv.Keys(ctx)
	if err != nil {
		return false
	}
	present := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		present[k] = struct{}{}
	}
	for _, f := range m.Files {
		if _, ok := present[f.Key]; !ok {
			p.logger.Info("statefiles: gate integrity check failed (manifest-listed key missing); republishing", "key", f.Key)
			return false
		}
	}
	return true
}

// bucketFileKeys lists the bucket's state-file keys (excluding the _revision
// and _manifest meta keys).
func (p *Publisher) bucketFileKeys(ctx context.Context) ([]string, error) {
	keys, err := p.kv.Keys(ctx)
	if err != nil {
		if errors.Is(err, bus.ErrNoKeysFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("statefiles: list keys: %w", err)
	}
	var fileKeys []string
	for _, key := range keys {
		if key == bus.KeyRevision || key == KeyManifest {
			continue
		}
		fileKeys = append(fileKeys, key)
	}
	return fileKeys, nil
}

// deleteStaleKeys removes bucket keys absent from the published file set.
// The _revision and _manifest meta keys are never deleted. This is
// best-effort garbage collection — the manifest is the source of truth on
// the read side — so individual failures are logged and left for the next
// publish to retry.
func (p *Publisher) deleteStaleKeys(ctx context.Context, files map[string][]byte) {
	keys, err := p.bucketFileKeys(ctx)
	if err != nil {
		p.logger.Warn("failed to list keys for stale-key cleanup", "error", err)
		return
	}
	for _, key := range keys {
		if _, ok := files[key]; ok {
			continue
		}
		if err := p.kv.Delete(ctx, key); err != nil {
			p.logger.Warn("failed to delete stale state file key", "key", key, "error", err)
			continue
		}
		p.logger.Info("deleted stale state file key", "key", key)
	}
}

// loadStateFiles walks a directory and loads all .zy files into a map
// keyed by forward-slash-separated relative paths.
func loadStateFiles(dir string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip hidden files/dirs and VCS directories
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || name == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return fmt.Errorf("rel path for %s: %w", path, err)
		}
		key := filepath.ToSlash(rel)
		files[key] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk states dir %s: %w", dir, err)
	}
	return files, nil
}
