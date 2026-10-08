//go:build integration

package integration

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// TestUpdateRollout_AbortResetsNodeAndRerunResumes pins the 0.7.1 rollout
// fixes end to end on wd-01, the only watchdog-supervised node in the stack:
//
//  1. a rollout with a long soak is aborted while the node soaks → the
//     controller must roll the node back to idle right away (not leave it
//     soaking for its confirm-deadline), recorded as rolled_back;
//  2. a second rollout to the same node must start clean (no "cannot prepare
//     in state …" failure) and complete.
//
// The published binary is the stack's own peel binary under a new version
// label: the swapped-in child is functionally identical, so soak passes. Runs
// late (zz_ prefix) because it restarts wd-01's peel several times.
func TestUpdateRollout_AbortResetsNodeAndRerunResumes(t *testing.T) {
	execInContainer(t, "admin", []string{"cp", "/usr/local/bin/zester-peel", "/tmp/rollout-peel"})
	t.Cleanup(func() {
		execInContainer(t, "admin", []string{"rm", "-f", "/tmp/rollout-peel"})
	})

	// Publish for the admin container's own platform (the CLI defaults
	// --goos/--goarch to its runtime): the peel containers run the same
	// arch as the Docker host — arm64 on Apple silicon, amd64 in CI — and
	// the controller looks the manifest up by the node's reported platform.
	const ver = "v9.9.9-rollout"
	pub := execInContainer(t, "admin", []string{
		"zester", "update", "publish", "/tmp/rollout-peel",
		"--component", "peel", "--version", ver,
	})
	if !strings.Contains(pub, "Published peel") {
		t.Fatalf("publish failed: %s", pub)
	}

	nodeState := func() string {
		out := execInContainer(t, "admin", []string{"zester", "--no-color", "update", "status", "--component", "peel"})
		for line := range strings.SplitSeq(out, "\n") {
			f := strings.Fields(line)
			if len(f) >= 3 && f[0] == "wd-01" {
				return f[2] // STATE column
			}
		}
		return ""
	}
	rolloutStatus := func(id string) string {
		return execInContainer(t, "admin", []string{"zester", "--no-color", "update", "status", "--component", "peel", "--rollout", id})
	}
	// waitNode polls the node state and, on timeout, fails with the rollout
	// record so a failure explains itself (which node step failed and why).
	waitNode := func(rolloutID, what string, timeout time.Duration, ok func(string) bool) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if ok(nodeState()) {
				return
			}
			time.Sleep(3 * time.Second)
		}
		t.Fatalf("timed out waiting for %s (node state %q)\n--- rollout %s ---\n%s", what, nodeState(), rolloutID, rolloutStatus(rolloutID))
	}
	waitForCondition(t, 60*time.Second, 2*time.Second, "wd-01 idle before the rollout", func() bool {
		s := nodeState()
		return s == "idle" || s == "confirmed"
	})

	// 1. Rollout with a long soak, aborted mid-soak.
	out := execInContainer(t, "admin", []string{
		"zester", "--no-color", "update", "rollout", "--component", "peel", "--version", ver,
		"--target", "wd-01", "--soak-time", "10m", "--max-failed", "1",
	})
	rolloutID := rolloutIDFrom(out)
	if rolloutID == "" {
		t.Fatalf("no rollout id in:\n%s", out)
	}
	waitNode(rolloutID, "wd-01 soaking", 3*time.Minute, func(s string) bool { return s == "soaking" })

	abort := execInContainer(t, "admin", []string{"zester", "--no-color", "update", "abort", rolloutID})
	if strings.Contains(strings.ToLower(abort), "error") {
		t.Fatalf("abort failed:\n%s", abort)
	}
	// The node must come back to idle well before its confirm-deadline
	// (3× soak = 30m); the abort-time rollback does it in seconds.
	waitNode(rolloutID, "wd-01 rolled back to idle after abort", 2*time.Minute, func(s string) bool { return s == "idle" })
	var aborted string
	waitForCondition(t, 60*time.Second, 3*time.Second, "rollout record shows rolled_back", func() bool {
		aborted = rolloutStatus(rolloutID)
		return strings.Contains(aborted, "aborted") && strings.Contains(aborted, "rolled_back")
	})
	if !strings.Contains(aborted, "rolled back on abort from soaking") {
		t.Errorf("abort should record why the node was rolled back:\n%s", aborted)
	}

	// 2. A fresh rollout to the same node starts clean and completes.
	waitForCondition(t, 2*time.Minute, 3*time.Second, "wd-01 answering after rollback", func() bool {
		results := execCLI(t, "wd-01", "test.ping")
		return len(results) == 1 && results[0].Success
	})
	out = execInContainer(t, "admin", []string{
		"zester", "--no-color", "update", "rollout", "--component", "peel", "--version", ver,
		"--target", "wd-01", "--soak-time", "20s", "--max-failed", "1",
	})
	rolloutID = rolloutIDFrom(out)
	if rolloutID == "" {
		t.Fatalf("no rollout id in:\n%s", out)
	}
	var last string
	waitForCondition(t, 4*time.Minute, 5*time.Second, "second rollout completed", func() bool {
		last = rolloutStatus(rolloutID)
		if strings.Contains(last, "aborted") {
			t.Fatalf("second rollout aborted — the node was not reset cleanly:\n%s", last)
		}
		return strings.Contains(last, "completed")
	})
	if !strings.Contains(last, "confirmed") || strings.Contains(last, "cannot prepare") {
		t.Errorf("second rollout should confirm wd-01 without a state refusal:\n%s", last)
	}

	// Leave the node answering for the tests that follow.
	waitForCondition(t, 2*time.Minute, 3*time.Second, "wd-01 answering after the rollout", func() bool {
		results := execCLI(t, "wd-01", "test.ping")
		return len(results) == 1 && results[0].Success
	})
}

// rolloutIDFrom extracts the id from `Rollout started: rol-...` CLI output.
func rolloutIDFrom(out string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "Rollout started:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Rollout started:"))
		}
	}
	return ""
}

// TestUpdateRollout_DriverDeathMidSoakIsAdoptedAndConfirmed pins the
// adoption fix: the master driving a rollout dies while the node soaks (the
// single-master self-update shape — the driver always dies with the old
// master process), the surviving master adopts the orphaned rollout and must
// CONFIRM the soaking node instead of re-sending prepare (which the watchdog
// refuses, and which used to count as a failure → abort → no confirm →
// confirm-deadline rollback). Needs the multi-master overlay (master-2).
func TestUpdateRollout_DriverDeathMidSoakIsAdoptedAndConfirmed(t *testing.T) {
	execInContainer(t, "admin", []string{"cp", "/usr/local/bin/zester-peel", "/tmp/adopt-peel"})
	t.Cleanup(func() {
		execInContainer(t, "admin", []string{"rm", "-f", "/tmp/adopt-peel"})
	})
	const ver = "v9.9.9-adopt"
	pub := execInContainer(t, "admin", []string{
		"zester", "update", "publish", "/tmp/adopt-peel", "--component", "peel", "--version", ver,
	})
	if !strings.Contains(pub, "Published peel") {
		t.Fatalf("publish failed: %s", pub)
	}

	nodeState := func() string {
		out := execInContainer(t, "admin", []string{"zester", "--no-color", "update", "status", "--component", "peel"})
		for line := range strings.SplitSeq(out, "\n") {
			f := strings.Fields(line)
			if len(f) >= 3 && f[0] == "wd-01" {
				return f[2]
			}
		}
		return ""
	}
	rolloutStatus := func(id string) string {
		return execInContainer(t, "admin", []string{"zester", "--no-color", "update", "status", "--component", "peel", "--rollout", id})
	}
	waitForCondition(t, 2*time.Minute, 3*time.Second, "wd-01 idle before the rollout", func() bool {
		s := nodeState()
		return s == "idle" || s == "confirmed"
	})

	// Soak long enough to kill the driver mid-soak, short enough that the
	// adopter's confirm (after its own soak window if the node has not
	// reported soak_passed yet) lands well inside the watchdog's 5m
	// confirm-deadline: adoption ≤ ~130s after the driver dies (60s stale +
	// 60s scan interval), plus at most one 90s soak.
	out := execInContainer(t, "admin", []string{
		"zester", "--no-color", "update", "rollout", "--component", "peel", "--version", ver,
		"--target", "wd-01", "--soak-time", "90s", "--max-failed", "1",
	})
	rolloutID := rolloutIDFrom(out)
	if rolloutID == "" {
		t.Fatalf("no rollout id in:\n%s", out)
	}
	waitForCondition(t, 3*time.Minute, 3*time.Second, "wd-01 soaking", func() bool { return nodeState() == "soaking" })

	// The driver is whichever master logged the batch start for this id.
	driver := ""
	for _, m := range []string{"master", "master-2"} {
		if strings.Contains(serviceLogs(t, m), `"id":"`+rolloutID+`"`) {
			driver = m
			break
		}
	}
	if driver == "" {
		t.Fatalf("could not find the driving master for %s in master logs", rolloutID)
	}
	survivor := "master-2"
	if driver == "master-2" {
		survivor = "master"
	}
	t.Logf("rollout %s driven by %s; stopping it mid-soak, %s should adopt", rolloutID, driver, survivor)
	stopService(t, driver)
	t.Cleanup(func() { restoreService(t, driver) })

	// Adoption: stale heartbeat (60s) + resume scan (≤60s).
	waitForCondition(t, 4*time.Minute, 5*time.Second, survivor+" adopts the orphaned rollout", func() bool {
		return strings.Contains(serviceLogs(t, survivor), "adopting orphaned rollout") &&
			strings.Contains(serviceLogs(t, survivor), `"id":"`+rolloutID+`"`)
	})

	var last string
	waitForCondition(t, 5*time.Minute, 5*time.Second, "adopted rollout completed", func() bool {
		last = rolloutStatus(rolloutID)
		if strings.Contains(last, "aborted") {
			t.Fatalf("adopted rollout aborted — the adopter re-prepared instead of confirming:\n%s", last)
		}
		return strings.Contains(last, "completed")
	})
	if !strings.Contains(last, "confirmed") || !strings.Contains(last, "resumed from soaking") {
		t.Errorf("adopted rollout should confirm wd-01 with a 'resumed from soaking' note:\n%s", last)
	}
	if strings.Contains(serviceLogs(t, survivor), "cannot prepare in state") {
		t.Errorf("the adopter must not re-send prepare to a soaking node")
	}
	waitForCondition(t, 2*time.Minute, 3*time.Second, "wd-01 confirmed", func() bool {
		s := nodeState()
		return s == "confirmed" || s == "idle"
	})
	results := execCLI(t, "wd-01", "test.ping")
	requireSuccess(t, results, "wd-01")
}

// serviceLogs returns a compose service's full container log.
func serviceLogs(t *testing.T, service string) string {
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
	data, _ := io.ReadAll(rc)
	return string(data)
}
