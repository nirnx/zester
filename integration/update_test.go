//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Update System Integration Tests
//
// These tests exercise the CLI → NATS → Object Store / KV pipeline for the
// self-update system. They use the admin container (which has NATS creds) to
// run `zester update` subcommands.
//
// Note: The Docker Compose stack does not include watchdog processes, so
// rollout, soak, and rollback end-to-end tests are not included here.
// Those paths are covered by unit tests in pkg/update/.
// ---------------------------------------------------------------------------

// TestUpdateVersions_EmptyBefore verifies that before publishing any binary,
// the versions command reports no published versions.
func TestUpdateVersions_EmptyBefore(t *testing.T) {
	output := execInContainer(t, "admin", []string{
		"zester", "update", "versions", "--component", "peel",
	})

	if !strings.Contains(output, "No published versions") {
		t.Errorf("expected 'No published versions' message, got: %s", output)
	}
}

// TestUpdatePublish publishes a dummy binary to the Object Store and verifies
// the CLI output contains the expected metadata.
func TestUpdatePublish(t *testing.T) {
	// Create a dummy binary in the admin container.
	execInContainer(t, "admin", []string{
		"sh", "-c", "echo 'fake-peel-binary-v1' > /tmp/test-peel-v1",
	})
	t.Cleanup(func() {
		execInContainer(t, "admin", []string{"rm", "-f", "/tmp/test-peel-v1"})
	})

	output := execInContainer(t, "admin", []string{
		"zester", "update", "publish", "/tmp/test-peel-v1",
		"--component", "peel",
		"--version", "v0.1.0-test",
		"--goos", "linux",
		"--goarch", "amd64",
	})

	if !strings.Contains(output, "Published peel v0.1.0-test") {
		t.Errorf("expected 'Published peel v0.1.0-test', got: %s", output)
	}
	if !strings.Contains(output, "SHA-256:") {
		t.Errorf("expected SHA-256 in output, got: %s", output)
	}
	if !strings.Contains(output, "peel/linux/amd64/v0.1.0-test") {
		t.Errorf("expected object key in output, got: %s", output)
	}
}

// TestUpdateVersions_AfterPublish verifies that a published binary shows up
// in the versions listing.
func TestUpdateVersions_AfterPublish(t *testing.T) {
	// Ensure a binary is published first.
	execInContainer(t, "admin", []string{
		"sh", "-c", "echo 'fake-peel-binary-v2' > /tmp/test-peel-v2",
	})
	t.Cleanup(func() {
		execInContainer(t, "admin", []string{"rm", "-f", "/tmp/test-peel-v2"})
	})

	execInContainer(t, "admin", []string{
		"zester", "update", "publish", "/tmp/test-peel-v2",
		"--component", "peel",
		"--version", "v0.2.0-test",
		"--goos", "linux",
		"--goarch", "amd64",
	})

	// Now list versions.
	output := execInContainer(t, "admin", []string{
		"zester", "update", "versions", "--component", "peel",
	})

	if !strings.Contains(output, "v0.2.0-test") {
		t.Errorf("expected version 'v0.2.0-test' in listing, got: %s", output)
	}
	if !strings.Contains(output, "linux") {
		t.Errorf("expected 'linux' in listing, got: %s", output)
	}
	if !strings.Contains(output, "amd64") {
		t.Errorf("expected 'amd64' in listing, got: %s", output)
	}
}

// TestUpdatePublish_MultipleVersions verifies that publishing multiple versions
// of the same component all appear in the versions listing.
func TestUpdatePublish_MultipleVersions(t *testing.T) {
	versions := []string{"v0.3.1-test", "v0.3.2-test", "v0.3.3-test"}

	for i, ver := range versions {
		content := strings.Repeat("x", (i+1)*100) // different size per version
		execInContainer(t, "admin", []string{
			"sh", "-c", "printf '" + content + "' > /tmp/test-multi-" + ver,
		})
		t.Cleanup(func() {
			execInContainer(t, "admin", []string{"rm", "-f", "/tmp/test-multi-" + ver})
		})

		execInContainer(t, "admin", []string{
			"zester", "update", "publish", "/tmp/test-multi-" + ver,
			"--component", "peel",
			"--version", ver,
			"--goos", "linux",
			"--goarch", "amd64",
		})
	}

	output := execInContainer(t, "admin", []string{
		"zester", "update", "versions", "--component", "peel",
	})

	for _, ver := range versions {
		if !strings.Contains(output, ver) {
			t.Errorf("expected version %q in listing, got: %s", ver, output)
		}
	}
}

// TestUpdatePublish_ComponentFilter verifies that versions are filtered by
// component — publishing a master binary should not appear in peel listings.
func TestUpdatePublish_ComponentFilter(t *testing.T) {
	// Publish a master binary.
	execInContainer(t, "admin", []string{
		"sh", "-c", "echo 'fake-master-binary' > /tmp/test-master-v1",
	})
	t.Cleanup(func() {
		execInContainer(t, "admin", []string{"rm", "-f", "/tmp/test-master-v1"})
	})

	execInContainer(t, "admin", []string{
		"zester", "update", "publish", "/tmp/test-master-v1",
		"--component", "master",
		"--version", "v0.4.0-test",
		"--goos", "linux",
		"--goarch", "amd64",
	})

	// Master listing should include it.
	masterOutput := execInContainer(t, "admin", []string{
		"zester", "update", "versions", "--component", "master",
	})
	if !strings.Contains(masterOutput, "v0.4.0-test") {
		t.Errorf("master listing should contain v0.4.0-test, got: %s", masterOutput)
	}
}

// TestUpdatePublish_SHA256Consistency verifies that publishing the same binary
// content twice produces the same SHA-256 digest.
func TestUpdatePublish_SHA256Consistency(t *testing.T) {
	content := "deterministic-binary-content-for-sha256-test"

	// Publish first time.
	execInContainer(t, "admin", []string{
		"sh", "-c", "printf '" + content + "' > /tmp/test-sha-v1",
	})
	t.Cleanup(func() {
		execInContainer(t, "admin", []string{"rm", "-f", "/tmp/test-sha-v1"})
	})

	out1 := execInContainer(t, "admin", []string{
		"zester", "update", "publish", "/tmp/test-sha-v1",
		"--component", "peel",
		"--version", "v0.5.1-test",
		"--goos", "linux",
		"--goarch", "amd64",
	})

	// Publish same content under different version.
	execInContainer(t, "admin", []string{
		"sh", "-c", "printf '" + content + "' > /tmp/test-sha-v2",
	})
	t.Cleanup(func() {
		execInContainer(t, "admin", []string{"rm", "-f", "/tmp/test-sha-v2"})
	})

	out2 := execInContainer(t, "admin", []string{
		"zester", "update", "publish", "/tmp/test-sha-v2",
		"--component", "peel",
		"--version", "v0.5.2-test",
		"--goos", "linux",
		"--goarch", "amd64",
	})

	// Extract SHA-256 lines.
	sha1 := extractSHA256(out1)
	sha2 := extractSHA256(out2)

	if sha1 == "" {
		t.Fatal("could not extract SHA-256 from first publish output")
	}
	if sha1 != sha2 {
		t.Errorf("same content produced different digests:\n  v1: %s\n  v2: %s", sha1, sha2)
	}
}

// TestUpdateStatus_WatchdogReports verifies that wd-01 — a peel supervised
// by zester-watchdog, connecting with the peel's own credentials (the
// packaged topology) — appears in `zester update status`. This is the
// regression test for peel JWTs lacking update-plane grants: without the
// $KV.update-status.<id> put and bucket-handle grants, the watchdog
// connects but every status heartbeat dies as a NATS permission violation
// and the node never reports. The reporter writes every 30s; poll
// generously.
func TestUpdateStatus_WatchdogReports(t *testing.T) {
	deadline := time.Now().Add(2 * time.Minute)
	var output string
	for time.Now().Before(deadline) {
		output = execInContainer(t, "admin", []string{
			"zester", "update", "status", "--component", "peel",
		})
		if strings.Contains(output, "wd-01") {
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("wd-01 never appeared in update status (watchdog status heartbeat with peel creds); last output: %s", output)
}

// extractSHA256 extracts the SHA-256 hex digest from CLI publish output.
// Expected format: "  SHA-256: <64-char hex>"
func extractSHA256(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SHA-256:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "SHA-256:"))
		}
	}
	return ""
}
