//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestStartupStates_AppliesAtBoot pins the peel's startup_states knob (Salt
// `startup_states` parity) end to end: db-02 boots with
// `--startup-states sls --startup-sls-list hello` (docker-compose.yml) and
// must apply the hello tree autonomously — enrollment → first state-file
// sync → boot-time apply, with NO test or operator ever dispatching states
// at db-02. This pins the config→boot-apply wiring; note the peel image also
// BAKES the playground tree (hello included), so the not-ready retry and
// first-sync-gate arms are NOT distinguishable here — those are pinned by
// the internal/peeld unit suite (TestRunStartupStates_*).
func TestStartupStates_AppliesAtBoot(t *testing.T) {
	deadline := time.Now().Add(2 * time.Minute)
	var last string
	for time.Now().Before(deadline) {
		// Exec runs INSIDE the db-02 container, so the file existing there is
		// itself the locality proof (each peel container has its own /tmp).
		out, code, err := tryExecInContainer(context.Background(), "db-02",
			[]string{"cat", "/tmp/hello-zester.txt"})
		if err == nil && code == 0 && strings.Contains(out, "Hello from Zester!") {
			return
		}
		last = out
		time.Sleep(2 * time.Second)
	}
	// Dump the peel's own startup-states trail before failing — the
	// difference between "never configured" (flags lost, e.g. a compose
	// overlay replacing the command) and "configured but stuck retrying".
	journal, _, _ := tryExecInContainer(context.Background(), "db-02",
		[]string{"sh", "-c", "journalctl -u zester-peel --no-pager | grep -i 'startup states' | tail -20"})
	t.Fatalf("startup_states never applied the hello tree on db-02 (last: %q)\nstartup-states journal:\n%s", last, journal)
}
