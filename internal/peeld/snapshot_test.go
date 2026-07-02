package peeld

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings-snapshot.msgpack")

	in := map[string]any{
		"basket_scope": "G@cluster:a",
		"app": map[string]any{
			"port": "8080",
			"name": "web",
		},
		"flag": true,
	}
	if err := saveSettingsSnapshot(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}

	out, err := loadSettingsSnapshot(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out["basket_scope"] != "G@cluster:a" {
		t.Errorf("basket_scope = %v, want G@cluster:a", out["basket_scope"])
	}
	if out["flag"] != true {
		t.Errorf("flag = %v, want true", out["flag"])
	}
	app, ok := out["app"].(map[string]any)
	if !ok {
		t.Fatalf("app = %T, want map[string]any", out["app"])
	}
	if app["port"] != "8080" || app["name"] != "web" {
		t.Errorf("app = %v, want port=8080 name=web", app)
	}
}

func TestLoadSettingsSnapshotMissing(t *testing.T) {
	out, err := loadSettingsSnapshot(filepath.Join(t.TempDir(), "nope.msgpack"))
	if err != nil {
		t.Fatalf("missing snapshot should not error, got %v", err)
	}
	if out != nil {
		t.Errorf("out = %v, want nil", out)
	}
}

func TestLoadSettingsSnapshotCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings-snapshot.msgpack")
	if err := os.WriteFile(path, []byte("not msgpack at all \xff\xfe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSettingsSnapshot(path); err == nil {
		t.Error("corrupt snapshot should error")
	}
}

// TestPersistSettingsSnapshotHashGate verifies the write is skipped when the
// content is unchanged and re-armed when it changes.
func TestPersistSettingsSnapshotHashGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings-snapshot.msgpack")
	a := &Agent{logger: discardLogger(), settingsSnapshotPath: path}

	a.persistSettingsSnapshot(map[string]any{"k": "v1"})
	info1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after first persist: %v", err)
	}

	// Unchanged content: file must not be rewritten.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	a.persistSettingsSnapshot(map[string]any{"k": "v1"})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("unchanged settings rewrote the snapshot")
	}
	_ = info1

	// Changed content: written again.
	a.persistSettingsSnapshot(map[string]any{"k": "v2"})
	out, err := loadSettingsSnapshot(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out["k"] != "v2" {
		t.Errorf("k = %v, want v2", out["k"])
	}
}
