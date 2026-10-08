//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestEnrollRevoke_CutsNATSAccess pins the security-review HIGH-1 fix:
// `zester enroll revoke` must actually invalidate the peel's NATS
// credentials, not just flip the enrollment record. The master re-signs the
// account JWT with a revocation list and pushes it to the nats-server over
// the system account; the server closes the peel's connection ("User
// Authentication Revoked") and refuses its reconnects. The peel then drops
// out of presence and targeting, and the master purges its facts/secrets.
//
// Runs second-to-last (zzy_ prefix: after zz_unreachable, which still needs
// web-02 live, and before zzz_permissions, which only greps logs for
// "permissions violation" — an authentication revocation is a different
// error class) because it permanently removes web-02 from the fleet.
func TestEnrollRevoke_CutsNATSAccess(t *testing.T) {
	const peel = "web-02"
	ctx := context.Background()

	// Sanity: the peel answers before the revoke.
	requireSuccess(t, execCLI(t, peel, "test.ping"), peel)

	// Find its active enrollment ID in `enroll list` (ID is column 1, PEEL
	// ID column 2; the display form of a dash-only id is the id itself).
	out := execInContainer(t, "admin", []string{"zester", "--no-color", "enroll", "list", "--state", "active"})
	enrollID := ""
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == peel && strings.HasPrefix(f[0], "enr-") {
			enrollID = f[0]
			break
		}
	}
	if enrollID == "" {
		t.Fatalf("no active enrollment for %s in:\n%s", peel, out)
	}

	// Revoke via the request/reply admin path (a running master applies it
	// and pushes the account JWT). The CLI must report a clean cut-off: any
	// WARNING means the push did not land.
	out = execInContainer(t, "admin", []string{"sh", "-c",
		"zester --no-color enroll revoke " + enrollID + " --reason integration-test 2>&1; echo EXIT:$?"})
	if !strings.Contains(out, "EXIT:0") || !strings.Contains(out, "revoked (peel: "+peel+")") {
		t.Fatalf("revoke failed:\n%s", out)
	}
	if strings.Contains(out, "WARNING") {
		t.Fatalf("revoke reported a NATS-level revocation warning — the account JWT push did not land:\n%s", out)
	}
	if !strings.Contains(out, "NATS credentials revoked") {
		t.Errorf("revoke output should confirm the NATS-level cut-off:\n%s", out)
	}

	// The master logged the push and the nats-server applied it: the peel's
	// bus client sees the server close its connection with the revocation
	// error ("nats: authentication revoked" via the async error handler),
	// its reconnects are refused ("authorization violation") and nats.go
	// gives up ("NATS connection closed"). Peel containers run under systemd,
	// so the peel's log lives in journald, not in `docker logs`.
	if err := waitForAnyServiceLog(ctx, []string{"master", "master-2"}, "account JWT revocation list pushed to nats-server", 60*time.Second); err != nil {
		t.Fatalf("master never logged the revocation push: %v", err)
	}
	waitForCondition(t, 90*time.Second, 3*time.Second, peel+" journal shows the NATS revocation", func() bool {
		journal := strings.ToLower(execInContainer(t, peel, []string{"journalctl", "--no-pager", "-o", "cat", "-u", "zester-peel"}))
		return strings.Contains(journal, "authentication revoked") || strings.Contains(journal, "authorization violation")
	})
	// Every NATS server refuses the revoked JWT on reconnect (the cluster
	// applied the pushed account JWT everywhere the account is loaded).
	if err := waitForAnyServiceLog(ctx, []string{"nats", "nats-2", "nats-3"}, "authentication error", 60*time.Second); err != nil {
		t.Fatalf("no nats-server logged an authentication error for the revoked peel: %v", err)
	}

	// Presence: the heartbeat key was purged (and the peel cannot renew it),
	// so `zester peel list` reports it offline or not at all.
	waitForCondition(t, 90*time.Second, 3*time.Second, peel+" offline in peel list", func() bool {
		out := execInContainer(t, "admin", []string{"zester", "--no-color", "peel", "list"})
		for line := range strings.SplitSeq(out, "\n") {
			f := strings.Fields(line)
			if len(f) >= 4 && f[0] == peel {
				return f[3] == "no"
			}
		}
		return true // not listed at all — purged
	})

	// Targeting: the facts key is gone, so a glob no longer resolves to it,
	// and a direct request/reply gets no answer from the dead connection.
	results := execCLIJob(t, "web-*", "test.ping")
	for _, r := range results {
		if r.PeelID == peel && r.Success {
			t.Errorf("revoked peel %s still answered a job dispatch: %+v", peel, r)
		}
	}
	results = execCLI(t, peel, "test.ping")
	for _, r := range results {
		if r.Success {
			t.Errorf("revoked peel %s still answered a direct request: %+v", peel, r)
		}
	}

	// The enrollment record is revoked and the readiness entry is healthy.
	out = execInContainer(t, "admin", []string{"zester", "--no-color", "enroll", "show", enrollID})
	if !strings.Contains(out, "revoked") {
		t.Errorf("enroll show should report the revoked state:\n%s", out)
	}
	readyz := execInContainer(t, "master", []string{"sh", "-c", "wget -qO- http://127.0.0.1:9091/readyz || curl -s http://127.0.0.1:9091/readyz"})
	if !strings.Contains(readyz, `"revocation"`) {
		t.Errorf("readyz should carry the revocation check:\n%s", readyz)
	}
	if strings.Contains(readyz, "revocation list not pushed") || strings.Contains(readyz, "revocation unavailable") {
		t.Errorf("revocation readiness degraded after a successful revoke:\n%s", readyz)
	}
}
