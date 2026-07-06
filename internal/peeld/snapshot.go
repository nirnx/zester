package peeld

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/nirnx/zester/pkg/bus"
)

// defaultSettingsSnapshotPath is the well-known on-disk location of the
// last-known-good resolved-settings snapshot (msgpack, 0600). The peel writes
// it on every successful settings resolution and loads it at boot, so a peel
// that restarts during a NATS outage warm-starts cachedSettings, the
// basket_scope, and settings-sourced schedule entries from the settings it
// last enforced (offline-first startup, roadmap C3 / finding 5).
const defaultSettingsSnapshotPath = "/data/settings-snapshot.msgpack"

// saveSettingsSnapshot persists a resolved settings map to path (msgpack,
// mode 0600, atomic temp-file + rename).
func saveSettingsSnapshot(path string, settings map[string]any) error {
	data, err := bus.Encode(settings)
	if err != nil {
		return fmt.Errorf("peeld: encode settings snapshot: %w", err)
	}
	return writeFileAtomic(path, data, 0o600)
}

// loadSettingsSnapshot reads a settings snapshot from path. A missing file is
// not an error: it returns (nil, nil) so first boots proceed without a warm
// cache.
func loadSettingsSnapshot(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("peeld: read settings snapshot: %w", err)
	}
	var settings map[string]any
	if err := bus.Decode(data, &settings); err != nil {
		return nil, fmt.Errorf("peeld: decode settings snapshot: %w", err)
	}
	return settings, nil
}

// hashSettings produces a deterministic SHA-256 of a settings map (sorted map
// keys remove Go iteration randomness). Used to skip rewriting an unchanged
// snapshot: settings resolve on every state execution, but the snapshot only
// hits disk when the content actually changed.
func hashSettings(settings map[string]any) [sha256.Size]byte {
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	enc.SetSortMapKeys(true)
	_ = enc.Encode(settings)
	return sha256.Sum256(buf.Bytes())
}

// persistSettingsSnapshot writes the last-known-good settings snapshot to
// disk, skipping the write when the content hash matches the last persist.
// Called on every successful resolve — the initial connected-phase resolve,
// watcher-driven reResolve, and per-execution resolves — all of which run
// under execMu, so calls are serialized. Failures are logged and never fail
// the resolve that triggered them.
func (a *Agent) persistSettingsSnapshot(settings map[string]any) {
	if a.settingsSnapshotPath == "" {
		return
	}
	h := hashSettings(settings)
	a.snapMu.Lock()
	unchanged := h == a.snapHash
	if !unchanged {
		a.snapHash = h
	}
	a.snapMu.Unlock()
	if unchanged {
		return
	}
	if err := saveSettingsSnapshot(a.settingsSnapshotPath, settings); err != nil {
		a.logger.Warn("failed to persist settings snapshot",
			"path", a.settingsSnapshotPath, "error", err)
	}
}

// writeFileAtomic writes data to path via a same-directory temp file and
// os.Rename, so readers never observe a partially written file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("peeld: create dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("peeld: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("peeld: chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("peeld: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("peeld: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("peeld: rename temp file: %w", err)
	}
	return nil
}
