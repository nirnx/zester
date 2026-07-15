//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"
)

// TestUnreachableFastPath_StoppedPeel pins the presence-aware job semantics
// end to end against a genuinely stopped peel:
//
//   - targeting still resolves the stopped peel (facts persist — heartbeat
//     presence never changes the target set);
//   - the dispatch classifies it suspected-offline, publishes anyway, and
//     after the ack-window re-publish + grace finalizes it with a synthetic
//     UNREACHABLE return — the CLI finishes in seconds, not the 60s timeout;
//   - live peels return normally; the CLI reports per-target status and
//     exits with the documented unreachable code (3).
//
// zz_ prefix: runs late in the suite because it stops a peel mid-run (the
// cleanup restarts it and waits for it to answer again).
func TestUnreachableFastPath_StoppedPeel(t *testing.T) {
	stopService(t, "web-03")
	t.Cleanup(func() {
		startService(t, "web-03")
		waitForCondition(t, 3*time.Minute, 3*time.Second, "web-03 responding again", func() bool {
			results := execCLI(t, "web-03", "test.ping")
			return len(results) == 1 && results[0].Success
		})
	})

	// The fast path keys on the peel-heartbeat presence hint (TTL 30s): wait
	// for the stale beat to expire so the dispatch classifies web-03 as
	// suspected-offline. (Dispatching earlier is correct but slow — the job
	// would honor its full deadline.)
	waitForCondition(t, 90*time.Second, 3*time.Second, "web-03 heartbeat expiry", func() bool {
		out := execInContainer(t, "admin", []string{"zester", "--no-color", "peel", "list"})
		for line := range strings.SplitSeq(out, "\n") {
			f := strings.Fields(line)
			if len(f) >= 4 && f[0] == "web-03" {
				return f[3] == "no"
			}
		}
		return false
	})

	// Job-mode ping across all three web peels: finishes in ~ackWindow+grace,
	// with an explicit UNREACHABLE line and exit code 3.
	start := time.Now()
	out := execInContainer(t, "admin", []string{"sh", "-c",
		"zester --no-color 'web-*' test.ping; echo EXIT:$?"})
	elapsed := time.Since(start)

	if elapsed > 30*time.Second {
		t.Errorf("ping with a stopped target took %s — the unreachable fast path did not finalize early", elapsed)
	}
	if !strings.Contains(out, "web-03: UNREACHABLE") {
		t.Errorf("output missing the explicit UNREACHABLE status for web-03:\n%s", out)
	}
	if !strings.Contains(out, "UNREACHABLE: no heartbeat at dispatch and no ack after republish") {
		t.Errorf("output missing the canonical unreachable reason:\n%s", out)
	}
	for _, peel := range []string{"web-01", "web-02"} {
		if !strings.Contains(out, peel+":") {
			t.Errorf("output missing the live peel %s:\n%s", peel, out)
		}
	}
	if !strings.Contains(out, "EXIT:3") {
		t.Errorf("exit code != 3 (unreachable class):\n%s", out)
	}

	// JSON output: the per-target status vocabulary scripts key on.
	jout := execInContainer(t, "admin", []string{"sh", "-c",
		"zester --format json --no-color 'web-*' test.ping; echo EXIT:$?"})
	for _, want := range []string{`"status": "unreachable"`, `"status": "success"`, "EXIT:3"} {
		if !strings.Contains(jout, want) {
			t.Errorf("JSON output missing %q:\n%s", want, jout)
		}
	}
}
