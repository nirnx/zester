//go:build integration

package integration

import (
	"strings"
	"testing"
)

// TestPromotionLifecycle exercises the release-promotion CLI surface over
// real NATS: publish with a TTL, promote (never expires), the fleet
// auto-rollout switch (default ON), demote, set-ttl guard rails, versions
// listing, and unpublish. Uses a throwaway "component" version far above
// anything real so the masters' auto-rollout (which only targets nodes
// LAGGING a promoted version — and dedups by rollout id) has live peels to
// consider but the assertions below complete before any rollout side
// effects matter; the version is demoted and unpublished before exit.
func TestPromotionLifecycle(t *testing.T) {
	// Publish a dummy binary under an isolated component-ish version. Use
	// the master component so the peel fleet's auto-rollout never targets
	// it (masters run no watchdogs in this stack except colocated setups).
	out := execInContainer(t, "admin", []string{"sh", "-c",
		"zester update publish /usr/local/bin/zester --component master --version 99.0.0 --ttl 1h 2>&1"})
	if !strings.Contains(out, "Published master 99.0.0") {
		t.Fatalf("publish failed: %s", out)
	}
	if !strings.Contains(out, "Expires:") || strings.Contains(out, "Expires: never") {
		t.Fatalf("publish --ttl 1h must print a concrete expiry: %s", out)
	}
	t.Cleanup(func() {
		execInContainer(t, "admin", []string{"sh", "-c",
			"zester update unpublish --component master --version 99.0.0 --force 2>/dev/null; true"})
	})

	// The auto switch defaults ON without ever being set.
	out = execInContainer(t, "admin", []string{"sh", "-c", "zester update auto status 2>&1"})
	if !strings.Contains(out, "Auto-rollout: ON") {
		t.Fatalf("auto switch must default ON: %s", out)
	}

	// Flip it off and back on.
	execInContainer(t, "admin", []string{"sh", "-c", "zester update auto off 2>&1"})
	out = execInContainer(t, "admin", []string{"sh", "-c", "zester update auto status 2>&1"})
	if !strings.Contains(out, "Auto-rollout: OFF") {
		t.Fatalf("auto off did not stick: %s", out)
	}
	execInContainer(t, "admin", []string{"sh", "-c", "zester update auto on 2>&1"})

	// Promote: expiry becomes never.
	out = execInContainer(t, "admin", []string{"sh", "-c",
		"zester update promote --component master --version 99.0.0 2>&1"})
	if !strings.Contains(out, "promoted") || !strings.Contains(out, "Expires: never") {
		t.Fatalf("promote output: %s", out)
	}

	// versions reflects PROMOTED/EXPIRES.
	out = execInContainer(t, "admin", []string{"sh", "-c",
		"zester update versions --component master 2>&1"})
	if !strings.Contains(out, "PROMOTED") || !strings.Contains(out, "99.0.0") {
		t.Fatalf("versions listing: %s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "99.0.0") && (!strings.Contains(line, "yes") || !strings.Contains(line, "never")) {
			t.Fatalf("promoted row must show yes/never: %q", line)
		}
	}

	// set-ttl on a promoted version is refused with a demote hint.
	out = execInContainer(t, "admin", []string{"sh", "-c",
		"zester update set-ttl --component master --version 99.0.0 --ttl 1h 2>&1; echo rc=$?"})
	if !strings.Contains(out, "demote") || !strings.Contains(out, "rc=1") {
		t.Fatalf("set-ttl on promoted must refuse with demote hint: %s", out)
	}

	// Unpublish without --force is refused while promoted.
	out = execInContainer(t, "admin", []string{"sh", "-c",
		"zester update unpublish --component master --version 99.0.0 2>&1; echo rc=$?"})
	if !strings.Contains(out, "PROMOTED") || !strings.Contains(out, "rc=1") {
		t.Fatalf("unpublish of promoted must require --force: %s", out)
	}

	// Demote with a short TTL: expiry resumes.
	out = execInContainer(t, "admin", []string{"sh", "-c",
		"zester update demote --component master --version 99.0.0 --ttl 2h 2>&1"})
	if !strings.Contains(out, "demoted") || strings.Contains(out, "Expires: never") {
		t.Fatalf("demote output: %s", out)
	}

	// Unpublish now succeeds; versions no longer lists it.
	out = execInContainer(t, "admin", []string{"sh", "-c",
		"zester update unpublish --component master --version 99.0.0 2>&1"})
	if !strings.Contains(out, "Unpublished master 99.0.0") {
		t.Fatalf("unpublish: %s", out)
	}
	out = execInContainer(t, "admin", []string{"sh", "-c",
		"zester update versions --component master 2>&1"})
	if strings.Contains(out, "99.0.0") {
		t.Fatalf("unpublished version still listed: %s", out)
	}

	// rollouts list works (the suite's earlier tests may or may not have
	// rolled anything — accept either shape, just not an error).
	out = execInContainer(t, "admin", []string{"sh", "-c", "zester update rollouts 2>&1; echo rc=$?"})
	if !strings.Contains(out, "rc=0") {
		t.Fatalf("rollouts list errored: %s", out)
	}
}
