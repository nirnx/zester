package masterd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/nirnx/zester/pkg/fileserver"
)

// publishAllFiles publishes the settings, state, and reactor file sets in
// order. Every set is hash-gated (force bypasses the gate), so an unchanged
// tree writes nothing to KV — which makes the callers below free to trigger
// it liberally: the initial lease publish, the republish ticker, the file
// watcher, and the fileserver-update admin service. pubAllMu serializes them.
func (d *Daemon) publishAllFiles(ctx context.Context, force bool) []fileserver.SetResult {
	d.pubAllMu.Lock()
	defer d.pubAllMu.Unlock()
	// publishSettingsFiles publishes the sealed masters-only replica first
	// (from the same file set) before the peel-facing sanitized publish.
	return []fileserver.SetResult{
		d.publishSettingsFiles(ctx, force),
		d.publishStateFiles(ctx, force),
		d.publishReactorFiles(ctx, force),
	}
}

// runFilesRepublish re-walks the file trees every interval while the
// publisher lease is held — the backstop for edits the watcher misses
// (NFS/remote mounts, event overflow, dirs created after startup). Nearly
// free when nothing changed: each set costs one directory walk plus one
// manifest comparison.
func (d *Daemon) runFilesRepublish(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.publishAllFiles(ctx, false)
		}
	}
}

// runFilesWatcher watches the settings/states/reactor dirs (recursively) and
// publishes on change, debounced so an editor's multi-event save or a git
// pull triggers one publish. Watch errors are logged and never fatal — the
// republish ticker is the correctness backstop; the watcher is only the
// fast path. Runs while the publisher lease is held.
func (d *Daemon) runFilesWatcher(ctx context.Context) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		d.logger.Warn("files watcher unavailable; relying on the republish interval", "error", err)
		return
	}
	defer w.Close()

	// The states dir is excluded under GitFS: publishing it is GitFS's job
	// (publishStateFiles skips it too), and git operations churn events.
	roots := []string{d.cfg.SettingsDir, d.cfg.Reactor.Dir}
	if len(d.cfg.GitFS.Remotes) == 0 {
		roots = append(roots, d.cfg.StatesDir)
	}
	watched := 0
	var watchedRoots []string
	for _, root := range roots {
		if root == "" {
			continue
		}
		n, err := addWatchesRecursive(w, root)
		if err != nil {
			d.logger.Debug("files watcher: dir not watchable (covered by the republish interval)",
				"dir", root, "error", err)
			continue
		}
		watched += n
		watchedRoots = append(watchedRoots, root)
	}
	if watched == 0 {
		d.logger.Debug("files watcher: nothing to watch yet; relying on the republish interval")
		return
	}
	d.logger.Info("watching file trees for changes", "dirs", watched)

	// Debounce: reset a short timer on every relevant event; publish when it
	// fires. The publish itself is hash-gated, so over-triggering is cheap.
	const debounce = time.Second
	timer := time.NewTimer(0)
	if !timer.Stop() {
		<-timer.C
	}

	for {
		select {
		case <-ctx.Done():
			return

		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if skipWatchPath(watchedRoots, ev.Name) {
				continue
			}
			// A new directory must be watched before files land in it
			// (fsnotify is non-recursive). Best-effort: a missed subdir is
			// caught by the republish interval.
			if ev.Op.Has(fsnotify.Create) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					if _, err := addWatchesRecursive(w, ev.Name); err != nil {
						d.logger.Debug("files watcher: add new dir failed", "dir", ev.Name, "error", err)
					}
				}
			}
			timer.Reset(debounce)

		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			d.logger.Warn("files watcher error (republish interval remains the backstop)", "error", err)

		case <-timer.C:
			d.publishAllFiles(ctx, false)
		}
	}
}

// addWatchesRecursive adds a watch on root and every non-hidden directory
// under it, returning the number of watches added. Hidden dirs (.git in
// GitFS clones, the hidden .<repo>.clone-* staging dirs) are skipped —
// exactly the set the publishers' own walks skip.
func addWatchesRecursive(w *fsnotify.Watcher, root string) (int, error) {
	added := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if path != root && (strings.HasPrefix(name, ".") || name == "__pycache__") {
			return filepath.SkipDir
		}
		if err := w.Add(path); err != nil {
			return err
		}
		added++
		return nil
	})
	return added, err
}

// skipWatchPath filters events from files the publishers ignore: hidden
// files and anything under a hidden directory (editor swap files, .git
// internals, GitFS staging dirs). Only segments BELOW a watched root are
// inspected — a root that happens to live under a dot-directory ancestor
// (e.g. /home/op/.config/zester/states) must not have all events dropped.
func skipWatchPath(roots []string, path string) bool {
	rel := path
	for _, root := range roots {
		if r, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
			break
		}
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(seg, ".") && seg != "." && seg != ".." {
			return true
		}
	}
	return false
}
