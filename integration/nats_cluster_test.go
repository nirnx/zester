//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestNATSCluster verifies that Zester survives NATS node failures when
// running a 3-node JetStream cluster with Replicas:3.
//
// Subtests run sequentially — they share the compose stack and stop/start
// containers. Each subtest registers restoreService cleanup to leave the
// stack intact for the next subtest.
func TestNATSCluster(t *testing.T) {
	// Skip guard: if the NATS cluster services aren't present (e.g., someone
	// ran tests with only the base compose file), skip the whole suite.
	if !hasService("nats-2") || !hasService("nats-3") {
		t.Skip("NATS cluster services not present; skipping cluster tests")
	}

	t.Run("AllNodesRunning", func(t *testing.T) {
		// Verify all 3 NATS nodes are running.
		if !isServiceRunning(t, "nats") {
			t.Fatal("nats is not running")
		}
		if !isServiceRunning(t, "nats-2") {
			t.Fatal("nats-2 is not running")
		}
		if !isServiceRunning(t, "nats-3") {
			t.Fatal("nats-3 is not running")
		}

		// All 5 peels should respond.
		results := execCLI(t, "*", "test.ping")
		if len(results) != 5 {
			t.Fatalf("expected 5 ping results, got %d", len(results))
		}
		for _, r := range results {
			r.checkSuccess(t, fmt.Sprintf("peel %s ping", r.PeelID))
		}
	})

	t.Run("SurviveSingleNodeFailure", func(t *testing.T) {
		restoreService(t, "nats-2")

		stopService(t, "nats-2")
		t.Log("nats-2 stopped, waiting 10s for JetStream leader re-election...")
		time.Sleep(10 * time.Second)

		// All peels should still respond — 2/3 NATS quorum is intact.
		results := execCLI(t, "*", "test.ping")
		if len(results) != 5 {
			t.Fatalf("expected 5 ping results after nats-2 failure, got %d", len(results))
		}
		for _, r := range results {
			r.checkSuccess(t, fmt.Sprintf("peel %s after nats-2 stop", r.PeelID))
		}

		// cmd.run should work (verifies end-to-end data path).
		cmdResults := execCLI(t, "web-01", "cmd.run", "echo nats-cluster-ok")
		cr := requireSuccess(t, cmdResults, "web-01")
		if !strings.Contains(cr.Results[0].Details["stdout"], "nats-cluster-ok") {
			t.Errorf("expected 'nats-cluster-ok', got %q", cr.Results[0].Details["stdout"])
		}
	})

	t.Run("KVAvailableDuringNodeFailure", func(t *testing.T) {
		restoreService(t, "nats-3")

		stopService(t, "nats-3")
		t.Log("nats-3 stopped, waiting 10s for re-election...")
		time.Sleep(10 * time.Second)

		// facts.get should work — KV with Replicas:3 maintains quorum at 2/3.
		factsResults := execCLI(t, "web-01", "facts.get", "os.family")
		fr := requireSuccess(t, factsResults, "web-01")
		if got := fr.Results[0].Details["result"]; got != "debian" {
			t.Errorf("expected os.family=\"debian\", got %q", got)
		}

		// settings.get should work — settings KV also has Replicas:3.
		settingsResults := execCLI(t, "web-01", "settings.get", "timezone")
		sr := requireSuccess(t, settingsResults, "web-01")
		if got := sr.Results[0].Details["result"]; got != "UTC" {
			t.Errorf("expected timezone=\"UTC\", got %q", got)
		}
	})

	t.Run("FileManaged_DuringNodeFailure", func(t *testing.T) {
		restoreService(t, "nats")

		stopService(t, "nats")
		t.Log("nats (node 1) stopped, waiting 10s for re-election...")
		time.Sleep(10 * time.Second)

		// file.managed on web-03 should work — tests master reconnection
		// to surviving cluster nodes.
		cleanupFile(t, "web-03", "/tmp/nats-cluster-test.txt")
		fmResults := execCLI(t, "web-03", "file.managed", "/tmp/nats-cluster-test.txt",
			"content=nats cluster HA test", "mode=0644")
		fmr := requireSuccess(t, fmResults, "web-03")
		if !fmr.Results[0].Changed {
			t.Error("expected changed=true for new file")
		}

		// Verify file content.
		verify := execCLI(t, "web-03", "cmd.run", "cat /tmp/nats-cluster-test.txt")
		vr := requireSuccess(t, verify, "web-03")
		if !strings.Contains(vr.Results[0].Details["stdout"], "nats cluster HA test") {
			t.Errorf("expected file content 'nats cluster HA test', got %q", vr.Results[0].Details["stdout"])
		}
	})

	t.Run("NodeRecoversAfterRestart", func(t *testing.T) {
		restoreService(t, "nats-2")

		stopService(t, "nats-2")
		t.Log("nats-2 stopped, restarting...")
		startService(t, "nats-2")

		// Wait for node to rejoin cluster and catch up on RAFT replication.
		t.Log("waiting 15s for nats-2 to rejoin cluster...")
		time.Sleep(15 * time.Second)

		if !isServiceRunning(t, "nats-2") {
			t.Fatal("nats-2 did not come back up")
		}

		// All peels should respond after the node recovers.
		results := execCLI(t, "*", "test.ping")
		if len(results) != 5 {
			t.Fatalf("expected 5 ping results after nats-2 recovery, got %d", len(results))
		}
		for _, r := range results {
			r.checkSuccess(t, fmt.Sprintf("peel %s after nats-2 recovery", r.PeelID))
		}
	})

	t.Run("CombinedNATSAndMasterFailure", func(t *testing.T) {
		restoreService(t, "nats-3")
		restoreService(t, "master")

		// Stop both nats-3 and master simultaneously.
		stopService(t, "nats-3")
		stopService(t, "master")
		t.Log("nats-3 + master stopped, waiting 15s for re-election + master-2 takeover...")
		time.Sleep(15 * time.Second)

		// 2/3 NATS quorum intact + master-2 alive → system should survive.
		if !isServiceRunning(t, "master-2") {
			t.Fatal("master-2 unexpectedly stopped")
		}

		results := execCLI(t, "*", "test.ping")
		if len(results) != 5 {
			t.Fatalf("expected 5 ping results after combined failure, got %d", len(results))
		}
		for _, r := range results {
			r.checkSuccess(t, fmt.Sprintf("peel %s after nats-3 + master failure", r.PeelID))
		}

		// cmd.run should work end-to-end.
		cmdResults := execCLI(t, "db-01", "cmd.run", "echo combined-failover-ok")
		cr := requireSuccess(t, cmdResults, "db-01")
		if !strings.Contains(cr.Results[0].Details["stdout"], "combined-failover-ok") {
			t.Errorf("expected 'combined-failover-ok', got %q", cr.Results[0].Details["stdout"])
		}
	})
}
