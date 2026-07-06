package statefiles

import (
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
}

// NewPublisher creates a state file publisher for the master.
func NewPublisher(cfg PublisherConfig) *Publisher {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Publisher{
		statesDir:  cfg.StatesDir,
		kv:         cfg.KV,
		logger:     logger,
		allowEmpty: cfg.AllowEmpty,
	}
}

// Publish walks the states directory, reads all .zy files, and publishes
// them to the state-files KV bucket. Returns the number of published files.
func (p *Publisher) Publish(ctx context.Context) (int, error) {
	files, err := loadStateFiles(p.statesDir)
	if err != nil {
		return 0, fmt.Errorf("statefiles: walk %s: %w", p.statesDir, err)
	}
	return p.PublishFiles(ctx, files)
}

// PublishFiles publishes the given files map to the state-files KV bucket.
// Keys are forward-slash-separated relative paths; values are raw file bytes.
//
// The batch protocol is: write all file keys, write the _manifest key
// describing the complete set, bump _revision (the signal peels watch), then
// best-effort delete bucket keys absent from the manifest. The manifest lands
// before the revision bump so a peel reacting to the bump always sees a
// manifest covering the batch; stale-key deletion is garbage collection only,
// because manifest-aware peels fetch exactly the manifest's file set.
//
// Returns the number of published files.
func (p *Publisher) PublishFiles(ctx context.Context, files map[string][]byte) (int, error) {
	if len(files) == 0 && !p.allowEmpty {
		existing, err := p.bucketFileKeys(ctx)
		if err != nil {
			return 0, err
		}
		if len(existing) > 0 {
			return 0, fmt.Errorf("statefiles: refusing to publish empty file set over %d existing files (states dir empty or not yet cloned; set AllowEmpty to force)", len(existing))
		}
	}

	count := 0
	for key, data := range files {
		if _, err := p.kv.Put(ctx, key, data); err != nil {
			return count, fmt.Errorf("statefiles: publish %s: %w", key, err)
		}
		p.logger.Debug("published state file", "key", key, "size", len(data))
		count++
	}

	manifest := BuildManifest(files)
	encoded, err := bus.Encode(manifest)
	if err != nil {
		return count, fmt.Errorf("statefiles: encode manifest: %w", err)
	}
	if _, err := p.kv.Put(ctx, KeyManifest, encoded); err != nil {
		return count, fmt.Errorf("statefiles: publish manifest: %w", err)
	}

	if err := bus.BumpRevision(ctx, p.kv); err != nil {
		return count, fmt.Errorf("statefiles: bump revision: %w", err)
	}

	p.deleteStaleKeys(ctx, files)

	return count, nil
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
