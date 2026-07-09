//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Live file publishing — no master restart
//
// Historically the publisher-lease holder walked the file trees ONCE per
// lease acquisition, so adding a state file required a master restart. Now
// edits flow three ways, all exercised here against the real stack:
//
//  1. `zester fileserver update` — the explicit push-it-now command
//     (request/reply answered only by the lease holder).
//  2. The fsnotify watcher + republish interval — a file dropped into the
//     states dir lands fleet-wide with NO command and NO restart.
//
// The masters share the state-data volume, so writing through either master
// container is visible to whichever one holds the publisher lease.
// ---------------------------------------------------------------------------

// writeStateFile drops a trivially-appliable state tree into the shared
// states volume via the master container.
func writeStateFile(t *testing.T, relPath, stateID string) {
	t.Helper()
	content := fmt.Sprintf("%s:\n  test.succeed_with_changes: []\n", stateID)
	script := fmt.Sprintf("mkdir -p /data/states/$(dirname %s) && printf '%%s' '%s' > /data/states/%s", relPath, content, relPath)
	execInContainer(t, "master", []string{"sh", "-c", script})
}

// waitForStateApply polls state.apply on one peel until the freshly published
// state compiles and applies — covering publish, peel cache resync, and the
// compiler pipeline end to end.
func waitForStateApply(t *testing.T, ref string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		results := execCLI(t, "web-01", "state.apply", ref)
		if len(results) == 1 && results[0].Success {
			return
		}
		if len(results) == 1 {
			last = results[0].Error
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("state %q did not become appliable within %s (last error: %s)", ref, timeout, last)
}

// TestFileserverUpdateCommand: drop a state file, push it with
// `zester fileserver update`, apply it.
func TestFileserverUpdateCommand(t *testing.T) {
	writeStateFile(t, "itestfsrvcmd/init.zy", "itest-fsrv-cmd")

	out := execInContainer(t, "admin", []string{"zester", "fileserver", "update"})
	if !strings.Contains(out, "Republish by master") {
		t.Fatalf("fileserver update output missing master line:\n%s", out)
	}
	for _, set := range []string{"settings", "states", "reactor"} {
		if !strings.Contains(out, set) {
			t.Errorf("fileserver update output missing %q set:\n%s", set, out)
		}
	}

	waitForStateApply(t, "itestfsrvcmd", 60*time.Second)
}

// TestFileserverLivePublish: drop a state file and DO NOTHING — the lease
// holder's watcher (or the republish interval as backstop) must publish it,
// and the peel must be able to apply it, with no restart and no command.
func TestFileserverLivePublish(t *testing.T) {
	writeStateFile(t, "itestfsrvlive/init.zy", "itest-fsrv-live")

	// Budget: watcher debounce ~1s (interval backstop 30s) + peel cache
	// debounce 2s + jitter up to 5s + sync; 90s covers the backstop path too.
	waitForStateApply(t, "itestfsrvlive", 90*time.Second)
}

// TestFileserverStatus: the status command names the publisher-lease holder
// by hostname — the box operators must edit files on. Answered only by the
// holder, so a reply also proves the leader-only contract over real NATS.
func TestFileserverStatus(t *testing.T) {
	out := execInContainer(t, "admin", []string{"zester", "fileserver", "status"})
	if !strings.Contains(out, "Publisher lease holder") {
		t.Fatalf("fileserver status output missing holder header:\n%s", out)
	}
	if !strings.Contains(out, "Hostname:") || !strings.Contains(out, "Master ID:") {
		t.Fatalf("fileserver status output missing identity fields:\n%s", out)
	}
	// The compose masters' hostnames are their container IDs; the holder must
	// be one of the two master containers, not empty.
	if strings.Contains(out, "Hostname:   \n") {
		t.Fatalf("fileserver status reported an empty hostname:\n%s", out)
	}
}
