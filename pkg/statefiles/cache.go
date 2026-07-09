package statefiles

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/settings"
)

// Cache syncs published files from a KV bucket to a local disk directory.
// Used on the peel side to maintain the local state-file cache, and on
// master standbys to mirror all three file buckets into their source dirs
// (multi-master convergence).
type Cache struct {
	cacheDir    string
	js          bus.JetStreamAPI
	logger      *slog.Logger
	bucket      string
	keyPrefix   string
	ignoreKeys  map[string]struct{}
	decode      func([]byte) ([]ManifestFile, error)
	decodeValue func(key string, stored []byte) ([]byte, error)
	hashFn      func([]byte) string
	onSynced    func()
	warnEdits   bool

	debounce time.Duration
	jitter   time.Duration
	retryMin time.Duration
	retryMax time.Duration

	// syncMu serializes full syncs (download + directory swap).
	syncMu sync.Mutex

	// mu guards lastSyncedRev, hasSynced, and lastSyncedSet.
	mu            sync.Mutex
	lastSyncedRev uint64
	hasSynced     bool
	lastSyncedSet map[string]string // disk-relative path -> sha256

	// syncRunning/syncRerun implement a lost-trigger-free singleflight for
	// watch-triggered sync loops (see TriggerSync).
	syncRunning atomic.Bool
	syncRerun   atomic.Bool
}

// CacheConfig configures a file cache/mirror.
type CacheConfig struct {
	// CacheDir is the local directory for cached state files.
	CacheDir string

	// JS is the JetStream context for KV access.
	JS bus.JetStreamAPI

	// Logger is the structured logger. Defaults to slog.Default().
	Logger *slog.Logger

	// Bucket is the KV bucket to sync from. Default: bus.BucketStateFiles.
	Bucket string

	// KeyPrefix, when set, restricts the sync to bucket keys carrying this
	// prefix and strips it to form the on-disk relative path (the
	// reactor-files layout: key "reactor/top.zy" -> "<dir>/top.zy"). Keys
	// outside the prefix are ignored entirely.
	KeyPrefix string

	// IgnoreKeys are additional well-known bucket keys that are neither
	// synced nor counted as file keys for torn-publish detection (e.g. the
	// settings bucket's _master_curve_pub).
	IgnoreKeys []string

	// DecodeManifest decodes the bucket's _manifest value into the file
	// list. Default: the statefiles Manifest encoding. The settings bucket
	// encodes a bare entry slice instead; its mirror injects a decoder.
	DecodeManifest func([]byte) ([]ManifestFile, error)

	// WarnLocalEdits makes Sync warn (by name) about local files that were
	// modified or added since the previous sync before overwriting/pruning
	// them — the master-standby mirror's "you edited on the wrong box"
	// breadcrumb. Detection needs a previous sync, so the first sync never
	// warns.
	WarnLocalEdits bool

	// DecodeValue, when set, transforms each fetched value BEFORE hash
	// verification and staging (the inverse of the publisher's EncodeValue —
	// e.g. opening account-key-sealed master-settings files). Manifest
	// hashes cover the DECODED plaintext. A decode failure fails the sync
	// attempt: undecodable content must never land on disk.
	DecodeValue func(key string, stored []byte) ([]byte, error)

	// HashValue must match the publisher's PublisherConfig.HashValue
	// (default: unkeyed SHA-256) — the sealed master-settings mirror passes
	// the same keyed hash so decoded plaintext verifies against the keyed
	// manifest.
	HashValue func([]byte) string

	// OnSynced fires after every successful Sync that staged files (not the
	// empty-bucket no-op) — the masterd settings mirror refreshes its
	// in-memory secret-extraction state from the freshly synced tree.
	OnSynced func()

	// SyncDebounce is the coalescing window for revision-triggered
	// re-syncs: rapid revision bumps within the window collapse into a
	// single sync. Default: 2s (mirrors the settings re-resolve path).
	SyncDebounce time.Duration

	// SyncJitter is the maximum random delay added after the debounce
	// window, spreading the fleet's re-sync burst across time.
	// Default: 5s (mirrors the settings re-resolve path).
	SyncJitter time.Duration

	// RetryBackoffMin is the initial backoff between failed sync retries.
	// Default: 1s.
	RetryBackoffMin time.Duration

	// RetryBackoffMax caps the exponential retry backoff. Default: 30s.
	RetryBackoffMax time.Duration
}

// NewCache creates a file cache/mirror.
func NewCache(cfg CacheConfig) *Cache {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	debounce := cfg.SyncDebounce
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	jitter := cfg.SyncJitter
	if jitter <= 0 {
		jitter = 5 * time.Second
	}
	retryMin := cfg.RetryBackoffMin
	if retryMin <= 0 {
		retryMin = time.Second
	}
	retryMax := cfg.RetryBackoffMax
	if retryMax <= 0 {
		retryMax = 30 * time.Second
	}
	bucket := cfg.Bucket
	if bucket == "" {
		bucket = bus.BucketStateFiles
	}
	decode := cfg.DecodeManifest
	if decode == nil {
		decode = func(data []byte) ([]ManifestFile, error) {
			var m Manifest
			if err := bus.Decode(data, &m); err != nil {
				return nil, err
			}
			return m.Files, nil
		}
	}
	ignore := make(map[string]struct{}, len(cfg.IgnoreKeys))
	for _, k := range cfg.IgnoreKeys {
		ignore[k] = struct{}{}
	}
	hashFn := cfg.HashValue
	if hashFn == nil {
		hashFn = HashFile
	}
	return &Cache{
		cacheDir:    cfg.CacheDir,
		js:          cfg.JS,
		logger:      logger,
		bucket:      bucket,
		keyPrefix:   cfg.KeyPrefix,
		ignoreKeys:  ignore,
		decode:      decode,
		decodeValue: cfg.DecodeValue,
		hashFn:      hashFn,
		onSynced:    cfg.OnSynced,
		warnEdits:   cfg.WarnLocalEdits,
		debounce:    debounce,
		jitter:      jitter,
		retryMin:    retryMin,
		retryMax:    retryMax,
	}
}

// HasSynced reports whether at least one Sync completed successfully in this
// process lifetime — the gate for the master-failover catch-up sync (a
// never-synced dir must not be overwritten from KV; see the masterd mirror).
func (c *Cache) HasSynced() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hasSynced
}

// Quiesce waits for any in-flight Sync to finish (by taking the sync lock).
// The masterd mirror calls it after cancelling the watch so a leadership
// takeover never races a half-finished directory swap.
func (c *Cache) Quiesce() {
	c.syncMu.Lock()
	defer c.syncMu.Unlock() // acquiring the lock IS the wait
}

// diskPathForKey maps a bucket key to its cache-relative path, applying the
// key prefix. ok=false means the key is outside this cache's scope.
func (c *Cache) diskPathForKey(key string) (string, bool) {
	if _, ignored := c.ignoreKeys[key]; ignored {
		return "", false
	}
	if c.keyPrefix == "" {
		return key, true
	}
	rel, found := strings.CutPrefix(key, c.keyPrefix)
	if !found || rel == "" {
		return "", false
	}
	return rel, true
}

// LastSyncedRevision returns the state-files bucket _revision value of the
// last successful Sync (0 before the first successful sync).
func (c *Cache) LastSyncedRevision() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastSyncedRev
}

// Sync performs a full download of all state files from the KV bucket to
// disk: exactly the manifest's file set is fetched (with SHA-256
// verification) and local files not in the manifest are pruned. Every
// publish writes the _manifest key before bumping _revision, so a bucket
// holding file keys without a manifest is torn (mid-publish) or tampered
// and fails the sync — the retry loop self-heals once the manifest lands. A
// bucket with no file keys at all (before the master's first publish) is a
// clean no-op that leaves the local cache untouched. The _revision and
// _manifest meta keys are never written to disk.
//
// The sync is atomic with respect to the live cache directory: the complete
// file set is staged in a fresh sibling temp directory and swapped in via
// rename, so a mid-download failure leaves the previous tree fully intact and
// the compiler never observes a mixed old/new tree.
//
// Returns the number of synced files.
func (c *Cache) Sync(ctx context.Context) (int, error) {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()

	// Re-check ctx UNDER the sync lock: Quiesce (cancel-then-wait teardown)
	// must guarantee no disk write starts after it returns. A sync goroutine
	// preempted between its loop-top ctx check and this lock would otherwise
	// run a full staging+swap with a cancelled ctx — real NATS would fail the
	// KV reads, but ctx-ignoring test fakes (and any future KV impl) would
	// not, and the write would race a directory teardown.
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	kv, err := bus.GetBucket(ctx, c.js, c.bucket)
	if err != nil {
		return 0, fmt.Errorf("statefiles: get bucket: %w", err)
	}

	// Read the target revision before downloading: if a publisher bumps the
	// revision mid-download, the recorded value stays conservative and the
	// staleness re-check in the retry loop triggers another pass.
	targetRev := bus.GetRevision(ctx, kv)

	manifest, err := c.loadManifest(ctx, kv)
	if err != nil {
		return 0, err
	}
	if manifest == nil {
		// No manifest has ever been published. The only legitimate shape
		// is a bucket with no file keys — a peel starting before the
		// master's first publish — which carries no deletion intent, so
		// the local cache is left untouched. File keys without a manifest
		// mean a torn or tampered publish: fail so the retry loop re-syncs
		// once the manifest lands.
		fileKeys, err := c.bucketFileKeys(ctx, kv)
		if err != nil {
			return 0, err
		}
		if len(fileKeys) > 0 {
			return 0, fmt.Errorf("statefiles: bucket holds %d file keys but no %s key (torn or tampered publish)", len(fileKeys), KeyManifest)
		}
		c.markSynced(targetRev, nil)
		return 0, nil
	}

	parent := filepath.Dir(c.cacheDir)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return 0, fmt.Errorf("statefiles: create cache parent %s: %w", parent, err)
	}
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(c.cacheDir)+".sync-*")
	if err != nil {
		return 0, fmt.Errorf("statefiles: create staging dir: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0755); err != nil {
		return 0, fmt.Errorf("statefiles: chmod staging dir: %w", err)
	}

	count, synced, err := c.stageManifest(ctx, kv, manifest, stage)
	if err != nil {
		return 0, err
	}

	if c.warnEdits {
		c.warnLocalEdits()
	}

	if err := c.swapIn(stage); err != nil {
		return 0, err
	}
	c.markSynced(targetRev, synced)
	c.logger.Info("state files synced", "count", count, "revision", targetRev, "bucket", c.bucket)
	if c.onSynced != nil {
		c.onSynced()
	}
	return count, nil
}

// markSynced records a completed successful sync: the revision, the synced
// disk set (for local-edit warnings), and the has-synced flag.
func (c *Cache) markSynced(rev uint64, syncedSet map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSyncedRev = rev
	c.hasSynced = true
	if syncedSet != nil || c.lastSyncedSet == nil {
		c.lastSyncedSet = syncedSet
	}
}

// warnLocalEdits compares the live tree against the previous sync's file set
// and warns (by name) about files that were locally modified or added since —
// they are about to be overwritten or pruned. Needs a previous sync to
// compare against; the first sync stays silent.
func (c *Cache) warnLocalEdits() {
	c.mu.Lock()
	prev := c.lastSyncedSet
	c.mu.Unlock()
	if prev == nil {
		return
	}
	var edited []string
	_ = filepath.WalkDir(c.cacheDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // best-effort warning walk
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, rerr := filepath.Rel(c.cacheDir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if want, ok := prev[rel]; !ok || c.hashFn(data) != want {
			edited = append(edited, rel)
		}
		return nil
	})
	if len(edited) > 0 {
		sort.Strings(edited)
		c.logger.Warn("LOCAL EDITS OVERWRITTEN by mirror sync — this master is not the publisher; edit on the lease holder (see 'zester fileserver status')",
			"dir", c.cacheDir, "files", strings.Join(edited, ", "))
	}
}

// loadManifest fetches and decodes the _manifest key. A missing key returns
// (nil, nil) — legitimate only for a bucket with no file keys, before the
// master's first publish; Sync enforces that.
func (c *Cache) loadManifest(ctx context.Context, kv bus.KV) ([]ManifestFile, error) {
	entry, err := kv.Get(ctx, KeyManifest)
	if err != nil {
		if errors.Is(err, bus.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("statefiles: get manifest: %w", err)
	}
	files, err := c.decode(entry.Value())
	if err != nil {
		return nil, fmt.Errorf("statefiles: decode manifest: %w", err)
	}
	if files == nil {
		files = []ManifestFile{}
	}
	return files, nil
}

// stageManifest downloads exactly the manifest's file set into the staging
// dir, verifying each file's SHA-256 digest. Any missing file or digest
// mismatch fails the whole sync attempt (the retry loop picks it up), so a
// torn or partially-deleted bucket is never swapped into the live cache.
// Returns the staged count and the disk-relative path -> sha256 set.
func (c *Cache) stageManifest(ctx context.Context, kv bus.KV, files []ManifestFile, stage string) (int, map[string]string, error) {
	count := 0
	synced := make(map[string]string, len(files))
	for _, f := range files {
		if f.Key == bus.KeyRevision || f.Key == KeyManifest {
			continue
		}
		rel, ok := c.diskPathForKey(f.Key)
		if !ok {
			continue
		}
		if !safeKey(rel) {
			return count, nil, fmt.Errorf("statefiles: unsafe manifest key %q", f.Key)
		}
		entry, err := kv.Get(ctx, f.Key)
		if err != nil {
			return count, nil, fmt.Errorf("statefiles: get %s: %w", f.Key, err)
		}
		data := entry.Value()
		if c.decodeValue != nil {
			// The stored bytes are an encoding (e.g. an account-key seal);
			// the manifest hash covers the DECODED plaintext.
			if data, err = c.decodeValue(f.Key, data); err != nil {
				return count, nil, fmt.Errorf("statefiles: decode %s: %w", f.Key, err)
			}
		}
		if sum := c.hashFn(data); sum != f.SHA256 {
			c.logger.Warn("state file hash mismatch, failing sync attempt",
				"key", f.Key, "want", f.SHA256, "got", sum)
			return count, nil, fmt.Errorf("statefiles: hash mismatch for %s", f.Key)
		}
		if err := writeFile(stage, rel, data); err != nil {
			return count, nil, fmt.Errorf("statefiles: stage %s: %w", f.Key, err)
		}
		synced[rel] = f.SHA256
		count++
	}
	return count, synced, nil
}

// bucketFileKeys lists the bucket's file keys within this cache's scope,
// excluding the _revision/_manifest meta keys and configured ignore keys.
func (c *Cache) bucketFileKeys(ctx context.Context, kv bus.KV) ([]string, error) {
	keys, err := kv.Keys(ctx)
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
		if _, ok := c.diskPathForKey(key); !ok {
			continue
		}
		fileKeys = append(fileKeys, key)
	}
	return fileKeys, nil
}

// swapIn atomically replaces the live cache dir with the staged tree:
// live → live.old, stage → live, remove live.old. On a failed second rename
// the previous tree is restored. A leftover .old dir from a crashed prior
// swap is removed first.
//
// Two live-dir shapes cannot be renamed and get special handling. An
// existing-but-empty live dir (e.g. baked into a container image with RUN
// mkdir, which overlayfs keeps in a lower layer where directory renames fail
// with EXDEV) is removed instead of moved aside — after the swap the live dir
// is a plain upper-layer dir and later swaps rename normally. A live dir that
// is non-empty and still un-renameable (overlayfs-merged, or a volume mount
// point, EBUSY) falls back to an in-place mirror of the staged tree: per-file
// atomic writes plus pruning, trading whole-tree atomicity for correctness on
// filesystems where the root rename is impossible.
func (c *Cache) swapIn(stage string) error {
	oldDir := c.cacheDir + ".old"
	if err := os.RemoveAll(oldDir); err != nil {
		return fmt.Errorf("statefiles: remove stale %s: %w", oldDir, err)
	}

	liveExists := true
	if _, err := os.Stat(c.cacheDir); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("statefiles: stat cache dir: %w", err)
		}
		liveExists = false
	}

	if liveExists {
		// An empty live dir holds no state worth preserving: remove it so
		// the staged tree renames straight into place (works even when the
		// dir itself is un-renameable overlayfs lower-layer content).
		if entries, err := os.ReadDir(c.cacheDir); err == nil && len(entries) == 0 {
			if err := os.Remove(c.cacheDir); err == nil {
				liveExists = false
			}
		}
	}

	if liveExists {
		if err := os.Rename(c.cacheDir, oldDir); err != nil {
			if errors.Is(err, syscall.EXDEV) || errors.Is(err, syscall.EBUSY) {
				c.logger.Info("cache dir not renameable, mirroring staged tree in place",
					"dir", c.cacheDir, "reason", err)
				return c.syncInPlace(stage)
			}
			return fmt.Errorf("statefiles: move live cache aside: %w", err)
		}
	}
	if err := os.Rename(stage, c.cacheDir); err != nil {
		if liveExists {
			if rerr := os.Rename(oldDir, c.cacheDir); rerr != nil {
				c.logger.Error("failed to restore cache dir after swap failure",
					"dir", c.cacheDir, "old_dir", oldDir, "error", rerr)
			}
		}
		return fmt.Errorf("statefiles: swap in staged cache: %w", err)
	}
	if liveExists {
		if err := os.RemoveAll(oldDir); err != nil {
			c.logger.Warn("failed to remove old cache dir", "dir", oldDir, "error", err)
		}
	}
	return nil
}

// syncInPlace mirrors the staged tree into the live cache dir without
// renaming the live root: every staged file is written through writeFile
// (atomic same-directory rename), then live files absent from the staged set
// are pruned, empty directories included. The staged set carries the same
// deletion intent as the whole-tree swap — it is exactly the manifest's file
// set — so manifest semantics hold; only whole-tree atomicity is lost, and
// only on filesystems where the root rename cannot work.
func (c *Cache) syncInPlace(stage string) error {
	staged := make(map[string]struct{})
	err := filepath.WalkDir(stage, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(stage, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := writeFile(c.cacheDir, filepath.ToSlash(rel), data); err != nil {
			return err
		}
		staged[rel] = struct{}{}
		return nil
	})
	if err != nil {
		return fmt.Errorf("statefiles: in-place sync: %w", err)
	}
	return c.pruneUnstaged(staged)
}

// pruneUnstaged removes live files whose cache-relative path is not in the
// staged set (this also collects temp files left by crashed writes), then
// removes directories that ended up empty, deepest first.
func (c *Cache) pruneUnstaged(staged map[string]struct{}) error {
	var dirs []string
	err := filepath.WalkDir(c.cacheDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if path == c.cacheDir {
			return nil
		}
		rel, rerr := filepath.Rel(c.cacheDir, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if _, ok := staged[rel]; !ok {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("statefiles: prune after in-place sync: %w", err)
	}
	// Children sort after their parent (the parent path is a strict prefix),
	// so reverse order deletes deepest dirs first. os.Remove refuses
	// non-empty dirs, which is exactly the guard needed here.
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, dir := range dirs {
		_ = os.Remove(dir)
	}
	return nil
}

// safeKey rejects keys that would escape the cache directory when joined.
func safeKey(key string) bool {
	if key == "" || strings.HasPrefix(key, "/") {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(key))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// currentRevision reads the bucket's current _revision value (0 when the
// key is missing or unreadable).
func (c *Cache) currentRevision(ctx context.Context) uint64 {
	kv, err := bus.GetBucket(ctx, c.js, c.bucket)
	if err != nil {
		return 0
	}
	return bus.GetRevision(ctx, kv)
}

// isStale reports whether the bucket's current _revision is newer than the
// last successfully synced one. Read errors report not-stale: the watch
// event for whatever changed will re-trigger a sync anyway.
func (c *Cache) isStale(ctx context.Context) bool {
	return c.currentRevision(ctx) > c.LastSyncedRevision()
}

// syncUntilCurrent runs Sync with capped exponential backoff until the
// bucket's current revision has been successfully synced. A failed sync
// keeps retrying instead of waiting for the next revision bump, and a bump
// that lands mid-sync triggers one more pass immediately.
func (c *Cache) syncUntilCurrent(ctx context.Context) {
	// Already synced at this exact revision (typically the watcher's initial
	// replay of _revision right after the startup Sync): skip the redundant
	// full re-sync. Read errors yield 0 and fall through to a sync, the safe
	// direction.
	if cur := c.currentRevision(ctx); cur != 0 && cur == c.LastSyncedRevision() {
		return
	}
	backoff := c.retryMin
	for {
		if ctx.Err() != nil {
			return
		}
		if _, err := c.Sync(ctx); err != nil {
			c.logger.Warn("state file sync failed, retrying", "error", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, c.retryMax)
			continue
		}
		backoff = c.retryMin
		if !c.isStale(ctx) {
			return
		}
	}
}

// TriggerSync runs syncUntilCurrent as a singleflight: concurrent triggers
// (e.g. a debounced invocation firing while a retry loop is still running)
// collapse into a rerun request instead of stacking loops. The rerun flag is
// set before the running CAS, so with Go's sequentially consistent atomics a
// trigger that loses the CAS is always observed by the active runner's
// post-run rerun check — no trigger is ever lost.
func (c *Cache) TriggerSync(ctx context.Context) {
	c.syncRerun.Store(true)
	for c.syncRerun.Load() {
		if !c.syncRunning.CompareAndSwap(false, true) {
			return
		}
		c.syncRerun.Store(false)
		c.syncUntilCurrent(ctx)
		c.syncRunning.Store(false)
	}
}

// Watch watches the state-files KV bucket for revision changes and performs a
// full re-sync when the revision bumps. Revision events are debounced with
// jitter (settings.DebouncedFunc) so rapid publishes collapse into one sync
// and fleet-wide re-sync bursts are spread over time; a failed sync retries
// with capped backoff until the current revision is synced. Falls back to
// incremental WatchAll if the revision key watch fails (backward compat).
// Returns a cancel function to stop watching. The watcher automatically
// reconnects with exponential backoff if the JetStream consumer is lost.
func (c *Cache) Watch(ctx context.Context) (context.CancelFunc, error) {
	kv, err := bus.GetBucket(ctx, c.js, c.bucket)
	if err != nil {
		return nil, fmt.Errorf("statefiles: get bucket for watch: %w", err)
	}

	watcher, err := kv.Watch(ctx, bus.KeyRevision)
	if err != nil {
		c.logger.Warn("revision watch failed, falling back to WatchAll", "bucket", c.bucket, "error", err)
		return c.watchAll(ctx, kv)
	}

	watchCtx, cancel := context.WithCancel(ctx)

	debounced := settings.NewDebouncedFunc(settings.DebouncedFuncConfig{
		Fn:       func() { c.TriggerSync(watchCtx) },
		Debounce: c.debounce,
		Jitter:   c.jitter,
	})

	go func() {
		defer watcher.Stop()
		defer debounced.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case entry, ok := <-watcher.Updates():
				if !ok {
					watcher.Stop()
					backoff := time.Second
					for {
						c.logger.Warn("watcher lost, reconnecting", "bucket", c.bucket, "key", bus.KeyRevision, "backoff", backoff)
						select {
						case <-watchCtx.Done():
							return
						case <-time.After(backoff):
						}
						backoff = min(backoff*2, 30*time.Second)

						newKV, err := bus.GetBucket(watchCtx, c.js, c.bucket)
						if err != nil {
							c.logger.Warn("watcher reconnect failed", "bucket", c.bucket, "error", err)
							continue
						}
						newWatcher, err := newKV.Watch(watchCtx, bus.KeyRevision)
						if err != nil {
							c.logger.Warn("watcher reconnect failed", "bucket", c.bucket, "error", err)
							continue
						}
						watcher = newWatcher
						backoff = time.Second
						c.logger.Info("watcher reconnected", "bucket", c.bucket, "key", bus.KeyRevision)
						break
					}
					continue
				}
				if entry == nil {
					continue
				}
				debounced.Trigger()
			}
		}
	}()

	return cancel, nil
}

// watchAll is the backward-compatible fallback that watches all keys incrementally.
func (c *Cache) watchAll(ctx context.Context, kv bus.KV) (context.CancelFunc, error) {
	watcher, err := kv.WatchAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("statefiles: create watcher (fallback): %w", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)

	go func() {
		defer watcher.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case entry, ok := <-watcher.Updates():
				if !ok {
					watcher.Stop()
					backoff := time.Second
					for {
						c.logger.Warn("watcher lost, reconnecting (fallback)", "bucket", c.bucket, "backoff", backoff)
						select {
						case <-watchCtx.Done():
							return
						case <-time.After(backoff):
						}
						backoff = min(backoff*2, 30*time.Second)

						newKV, err := bus.GetBucket(watchCtx, c.js, c.bucket)
						if err != nil {
							c.logger.Warn("watcher reconnect failed", "bucket", c.bucket, "error", err)
							continue
						}
						newWatcher, err := newKV.WatchAll(watchCtx)
						if err != nil {
							c.logger.Warn("watcher reconnect failed", "bucket", c.bucket, "error", err)
							continue
						}
						watcher = newWatcher
						backoff = time.Second
						c.logger.Info("watcher reconnected (fallback)", "bucket", c.bucket)
						break
					}
					continue
				}
				if entry == nil {
					continue
				}
				// A decode/onSynced cache (the sealed settings mirror) cannot
				// use the per-key incremental path: it needs manifest hash
				// verification, the atomic tree swap, and the OnSynced refresh
				// — all of which live in Sync. So on any event, run a full
				// sync instead of a per-key write.
				if c.decodeValue != nil || c.onSynced != nil {
					c.triggerSyncCtx(watchCtx)
					continue
				}
				c.handleUpdate(entry)
			}
		}
	}()

	return cancel, nil
}

// triggerSyncCtx runs a full syncUntilCurrent (used by the WatchAll fallback
// for decode/onSynced caches that must not take the incremental per-key
// path).
func (c *Cache) triggerSyncCtx(ctx context.Context) {
	go c.TriggerSync(ctx)
}

// handleUpdate processes a single KV update: writes or removes the file on
// disk (the WatchAll fallback path; the primary path is full revision-driven
// syncs). Values pass through decodeValue like staged syncs do.
func (c *Cache) handleUpdate(entry bus.KVEntry) {
	key := entry.Key()
	if key == bus.KeyRevision || key == KeyManifest {
		return
	}
	rel, ok := c.diskPathForKey(key)
	if !ok {
		return
	}
	op := entry.Operation()
	if op == bus.KVOpDelete || op == bus.KVOpPurge {
		diskPath := filepath.Join(c.cacheDir, filepath.FromSlash(rel))
		if err := os.Remove(diskPath); err != nil && !os.IsNotExist(err) {
			c.logger.Warn("failed to remove cached state file", "key", key, "error", err)
		} else {
			c.logger.Debug("removed cached state file", "key", key)
		}
		return
	}

	if !safeKey(rel) {
		c.logger.Warn("skipping unsafe state file key", "key", key)
		return
	}
	data := entry.Value()
	if c.decodeValue != nil {
		var err error
		if data, err = c.decodeValue(key, data); err != nil {
			c.logger.Warn("failed to decode cached state file, skipping", "key", key, "error", err)
			return
		}
	}
	if err := writeFile(c.cacheDir, rel, data); err != nil {
		c.logger.Warn("failed to write cached state file", "key", key, "error", err)
	} else {
		c.logger.Debug("updated cached state file", "key", key, "size", len(data))
	}
}

// writeFile writes data to {baseDir}/{key}, creating intermediate directories.
// The write is atomic: data goes to a temp file in the target directory first,
// then os.Rename replaces the target, so a concurrent reader (the compiler)
// never observes a torn file during re-sync.
func writeFile(baseDir, key string, data []byte) error {
	diskPath := filepath.Join(baseDir, filepath.FromSlash(key))
	dir := filepath.Dir(diskPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".zester-cache-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func(err error) error {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}

	if _, err := tmp.Write(data); err != nil {
		return cleanup(fmt.Errorf("write %s: %w", tmpPath, err))
	}
	// CreateTemp uses 0600; match the previous os.WriteFile mode.
	if err := tmp.Chmod(0644); err != nil {
		return cleanup(fmt.Errorf("chmod %s: %w", tmpPath, err))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, diskPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, diskPath, err)
	}
	return nil
}
