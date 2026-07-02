//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers: container lifecycle
// ---------------------------------------------------------------------------

// stopService stops a Docker Compose service container. Registers a cleanup
// to restart it so subsequent tests find the stack intact.
func stopService(t *testing.T, service string) {
	t.Helper()
	ctx := context.Background()

	container, err := stack.ServiceContainer(ctx, service)
	if err != nil {
		t.Fatalf("get container %s: %v", service, err)
	}

	timeout := 10 * time.Second
	if err := container.Stop(ctx, &timeout); err != nil {
		t.Fatalf("stop %s: %v", service, err)
	}
}

// startService starts a previously stopped Docker Compose service container.
func startService(t *testing.T, service string) {
	t.Helper()
	ctx := context.Background()

	container, err := stack.ServiceContainer(ctx, service)
	if err != nil {
		t.Fatalf("get container %s: %v", service, err)
	}

	if err := container.Start(ctx); err != nil {
		t.Fatalf("start %s: %v", service, err)
	}
}

// isServiceRunning returns whether a compose service container is running.
func isServiceRunning(t *testing.T, service string) bool {
	t.Helper()
	ctx := context.Background()

	container, err := stack.ServiceContainer(ctx, service)
	if err != nil {
		t.Fatalf("get container %s: %v", service, err)
		return false
	}
	return container.IsRunning()
}

// getMasterLogs returns the full log output from a master service container.
func getMasterLogs(t *testing.T, service string) string {
	t.Helper()
	ctx := context.Background()

	container, err := stack.ServiceContainer(ctx, service)
	if err != nil {
		t.Fatalf("get container %s: %v", service, err)
	}

	rc, err := container.Logs(ctx)
	if err != nil {
		t.Fatalf("logs %s: %v", service, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read logs %s: %v", service, err)
	}
	return string(data)
}

// masterIDPattern matches both slog text output (master_id=master-2OPx...)
// and the daemons' default JSON output ("master_id":"master-2OPx...").
var masterIDPattern = regexp.MustCompile(`master_id"?[=:]"?(master-[A-Za-z0-9]+)`)

// extractMasterID parses the first master_id from slog-formatted log output.
func extractMasterID(t *testing.T, logs string) string {
	t.Helper()
	m := masterIDPattern.FindStringSubmatch(logs)
	if m == nil {
		t.Fatalf("no master_id found in logs:\n%s", truncate(logs, 500))
	}
	return m[1]
}

// extractAllMasterIDs returns every unique master_id found in the log output.
// A restarted container may have multiple IDs from successive runs.
func extractAllMasterIDs(t *testing.T, logs string) []string {
	t.Helper()
	matches := masterIDPattern.FindAllStringSubmatch(logs, -1)
	seen := make(map[string]bool)
	var ids []string
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			ids = append(ids, m[1])
		}
	}
	return ids
}

// truncate returns at most n bytes of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// restoreService registers a t.Cleanup that restarts a service if it is
// not running, ensuring subsequent tests find the stack intact.
func restoreService(t *testing.T, service string) {
	t.Helper()
	t.Cleanup(func() {
		if !isServiceRunning(t, service) {
			startService(t, service)
			// Give it a moment to reconnect to NATS and re-init.
			time.Sleep(15 * time.Second)
		}
	})
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestMultiMaster(t *testing.T) {
	// Subtests run sequentially — they share the compose stack and
	// stop/start containers.

	t.Run("BothMastersRunConcurrently", func(t *testing.T) {
		restoreService(t, "master")
		restoreService(t, "master-2")

		// Both master containers should be running.
		if !isServiceRunning(t, "master") {
			t.Fatal("master is not running")
		}
		if !isServiceRunning(t, "master-2") {
			t.Fatal("master-2 is not running")
		}

		// Each master should have a unique KSUID-based master_id.
		logs1 := getMasterLogs(t, "master")
		logs2 := getMasterLogs(t, "master-2")
		id1 := extractMasterID(t, logs1)
		id2 := extractMasterID(t, logs2)

		if id1 == id2 {
			t.Fatalf("both masters have the same ID: %s", id1)
		}
		t.Logf("master IDs: %s, %s", id1, id2)

		// All 5 peels should respond to test.ping.
		results := execCLI(t, "*", "test.ping")
		if len(results) != 5 {
			t.Fatalf("expected 5 ping results, got %d", len(results))
		}
		for _, r := range results {
			r.checkSuccess(t, fmt.Sprintf("peel %s ping", r.PeelID))
		}

		// Settings should be available (both masters publish the same KV data).
		settings := execCLI(t, "web-01", "settings.get", "timezone")
		r := requireSuccess(t, settings, "web-01")
		if got := r.Results[0].Details["result"]; got != "UTC" {
			t.Errorf("expected timezone=UTC, got %q", got)
		}
	})

	t.Run("SurvivePrimaryFailure", func(t *testing.T) {
		restoreService(t, "master")

		stopService(t, "master")
		t.Logf("master stopped, waiting 5s for settle...")
		time.Sleep(5 * time.Second)

		// master-2 should still be running.
		if !isServiceRunning(t, "master-2") {
			t.Fatal("master-2 unexpectedly stopped")
		}

		// All peels should still respond (zester --direct uses NATS request/reply,
		// which bypasses masters entirely).
		results := execCLI(t, "*", "test.ping")
		if len(results) != 5 {
			t.Fatalf("expected 5 ping results after primary failure, got %d", len(results))
		}
		for _, r := range results {
			r.checkSuccess(t, fmt.Sprintf("peel %s after primary master stop", r.PeelID))
		}

		// cmd.run should also work.
		cmdResults := execCLI(t, "web-01", "cmd.run", "echo failover-ok")
		cr := requireSuccess(t, cmdResults, "web-01")
		if !strings.Contains(cr.Results[0].Details["stdout"], "failover-ok") {
			t.Errorf("expected 'failover-ok', got %q", cr.Results[0].Details["stdout"])
		}
	})

	t.Run("SurviveSecondaryFailure", func(t *testing.T) {
		restoreService(t, "master-2")

		stopService(t, "master-2")
		t.Logf("master-2 stopped, waiting 5s for settle...")
		time.Sleep(5 * time.Second)

		// Primary master should still be running.
		if !isServiceRunning(t, "master") {
			t.Fatal("master unexpectedly stopped")
		}

		results := execCLI(t, "*", "test.ping")
		if len(results) != 5 {
			t.Fatalf("expected 5 ping results after secondary failure, got %d", len(results))
		}
		for _, r := range results {
			r.checkSuccess(t, fmt.Sprintf("peel %s after secondary master stop", r.PeelID))
		}

		cmdResults := execCLI(t, "db-01", "cmd.run", "echo secondary-failover-ok")
		cr := requireSuccess(t, cmdResults, "db-01")
		if !strings.Contains(cr.Results[0].Details["stdout"], "secondary-failover-ok") {
			t.Errorf("expected 'secondary-failover-ok', got %q", cr.Results[0].Details["stdout"])
		}
	})

	t.Run("MasterRestart", func(t *testing.T) {
		restoreService(t, "master")

		// Capture the pre-restart master ID.
		logsBefore := getMasterLogs(t, "master")
		idsBefore := extractAllMasterIDs(t, logsBefore)

		stopService(t, "master")
		t.Logf("master stopped, restarting...")
		startService(t, "master")

		// Wait for the restarted master to fully initialize:
		// NATS reconnect + KV bucket init + settings/state republish + heartbeat.
		t.Logf("waiting 15s for master re-init...")
		time.Sleep(15 * time.Second)

		if !isServiceRunning(t, "master") {
			t.Fatal("master did not come back up")
		}

		// The restarted master generates a new KSUID, so its logs should
		// contain at least one master_id not seen before the restart.
		logsAfter := getMasterLogs(t, "master")
		idsAfter := extractAllMasterIDs(t, logsAfter)

		beforeSet := make(map[string]bool, len(idsBefore))
		for _, id := range idsBefore {
			beforeSet[id] = true
		}

		newFound := false
		for _, id := range idsAfter {
			if !beforeSet[id] {
				t.Logf("new master ID after restart: %s", id)
				newFound = true
				break
			}
		}
		if !newFound {
			t.Errorf("expected a new master_id after restart; before=%v after=%v", idsBefore, idsAfter)
		}

		// All peels should respond after the master comes back.
		results := execCLI(t, "*", "test.ping")
		if len(results) != 5 {
			t.Fatalf("expected 5 ping results after restart, got %d", len(results))
		}
		for _, r := range results {
			r.checkSuccess(t, fmt.Sprintf("peel %s after master restart", r.PeelID))
		}
	})

	t.Run("StateApplyDuringFailover", func(t *testing.T) {
		restoreService(t, "master-2")

		stopService(t, "master-2")
		t.Logf("master-2 stopped, running state operations...")
		time.Sleep(5 * time.Second)

		// file.managed should work — peels handle execution locally.
		cleanupFile(t, "web-02", "/tmp/multi-master-test.txt")
		fmResults := execCLI(t, "web-02", "file.managed", "/tmp/multi-master-test.txt",
			"content=multi-master failover test", "mode=0644")
		fmr := requireSuccess(t, fmResults, "web-02")
		if !fmr.Results[0].Changed {
			t.Error("expected changed=true for new file")
		}

		// state.apply should work — compiler runs on the peel, state files
		// are already cached from the KV bucket.
		cleanupFile(t, "web-02", "/tmp/hello-zester.txt")
		saResults := execCLI(t, "web-02", "state.apply", "hello")
		requireSuccess(t, saResults, "web-02")

		verify := execCLI(t, "web-02", "cmd.run", "cat /tmp/hello-zester.txt")
		vr := requireSuccess(t, verify, "web-02")
		if !strings.Contains(vr.Results[0].Details["stdout"], "Hello from Zester") {
			t.Errorf("expected 'Hello from Zester', got %q", vr.Results[0].Details["stdout"])
		}

		// settings.get should work — settings KV is independent of masters.
		sResults := execCLI(t, "web-02", "settings.get", "timezone")
		sr := requireSuccess(t, sResults, "web-02")
		if got := sr.Results[0].Details["result"]; got != "UTC" {
			t.Errorf("expected timezone=UTC, got %q", got)
		}

		fmt.Println("all state operations succeeded during master-2 failover")
	})
}
