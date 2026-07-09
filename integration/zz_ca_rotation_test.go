//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

// ---------------------------------------------------------------------------
// CA rotation — file drop, no process restarts (Phase 0 foundation)
//
// Validates the per-(re)connect trust re-read (bus.ClientConfig.CAFile →
// RootCAsCB): rotating the NATS CA is a file drop on the trust path plus a
// NATS server cert swap — every daemon (masters, peels, the wd-01 watchdog)
// picks up the new trust at its next reconnect WITHOUT being restarted.
//
// The test is deliberately two-phase, mirroring the documented rotation
// choreography:
//   1. overlap — trust file becomes an old+new CA bundle, the NATS servers
//      get a new-CA-signed cert, nodes are rolled one at a time (quorum
//      intact throughout);
//   2. complete — the OLD CA is dropped from the trust file entirely and
//      the NATS nodes are rolled again. After this phase any client whose
//      trust pool was frozen at boot could never reconnect; only per-attempt
//      readers survive. A fleet-wide ping and a master-dispatched job prove
//      every component re-read trust from disk.
//
// This file is named zz_* so it runs LAST in the package: it permanently
// rotates the stack's CA, and while the fleet is fully functional afterwards,
// earlier tests should not run during the rolling NATS restarts.
// ---------------------------------------------------------------------------

// writeAuthFile atomically rewrites a file on the shared auth volume from
// the admin container (which is NOT a counted fleet node, so disrupting it
// never affects the ping count) using base64 + `mv` so peels' per-reconnect
// CA reads never observe a partial file.
func writeAuthFile(t *testing.T, path string, content []byte) {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString(content)
	tmp := path + ".tmp"
	script := fmt.Sprintf("printf '%%s' '%s' | base64 -d > %s && mv -f %s %s", b64, tmp, tmp, path)
	execInContainer(t, "admin", []string{"sh", "-c", script})
}

// restartService stops and starts a compose service container in place.
func restartService(t *testing.T, service string) {
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
	if err := container.Start(ctx); err != nil {
		t.Fatalf("start %s: %v", service, err)
	}
}

// rollNATSAndWait restarts the NATS nodes ONE AT A TIME, waiting for the
// whole fleet to answer pings after EACH node before moving to the next.
// Rolling all three rapidly and waiting once at the end can leave a peel
// stuck in reconnect backoff when its target node bounces mid-attempt
// (especially under full-suite load, after the cluster/master restart tests
// have already churned the JetStream RAFT); the per-node settle avoids the
// thundering reconnect. 2/3 quorum stays intact throughout.
func rollNATSAndWait(t *testing.T, phase string) {
	t.Helper()
	for _, svc := range []string{"nats", "nats-2", "nats-3"} {
		restartService(t, svc)
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		if err := waitForPeels(ctx, 4*time.Minute); err != nil {
			cancel()
			t.Fatalf("%s: fleet did not recover after restarting %s: %v", phase, svc, err)
		}
		cancel()
	}
}

func TestCARotation_FileDropWithoutProcessRestart(t *testing.T) {
	// Current CA (both trust file and the bundle base for the overlap phase).
	oldCA := execInContainer(t, "admin", []string{"cat", "/data/auth/nats-ca.crt"})
	if oldCA == "" {
		t.Fatal("could not read current nats-ca.crt")
	}

	// New CA + NATS server cert, generated in-process with the same
	// primitives playground-init uses; SANs must cover every name the
	// fleet dials (cluster overlay uses nats, nats-2, nats-3).
	newCA, err := bus.GenerateSelfSignedCA("Zester Rotation Test CA", 24*time.Hour)
	if err != nil {
		t.Fatalf("generate rotation CA: %v", err)
	}
	certPEM, keyPEM, err := newCA.IssueCert(
		"nats", nil, []string{"nats", "nats-2", "nats-3", "localhost"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("issue rotated NATS server cert: %v", err)
	}

	// Phase 1 — overlap: trust bundle = old+new, server cert = new.
	// No zester process is restarted at any point in this test.
	writeAuthFile(t, "/data/auth/nats-ca.crt", append([]byte(oldCA), newCA.CertPEM...))
	writeAuthFile(t, "/data/auth/nats-server.crt", certPEM)
	writeAuthFile(t, "/data/auth/nats-server.key", keyPEM)
	rollNATSAndWait(t, "phase 1 (overlap bundle)")

	// Phase 2 — complete: drop the old CA entirely. From here on, any
	// client that froze its trust pool at boot could never reconnect —
	// surviving the next roll proves every daemon re-reads trust per
	// connect attempt.
	writeAuthFile(t, "/data/auth/nats-ca.crt", newCA.CertPEM)
	rollNATSAndWait(t, "phase 2 (old CA dropped)")

	// Direct fan-out: every peel (including the watchdog-supervised wd-01)
	// reconnected under the new CA.
	results := execCLI(t, "*", "test.ping")
	if len(results) != len(allNodes) {
		t.Fatalf("expected %d ping results after CA rotation, got %d", len(allNodes), len(results))
	}
	for _, r := range results {
		r.checkSuccess(t, "post-rotation ping "+r.PeelID)
	}

	// Job mode exercises the master path (dispatch, KV, watcher): the
	// masters also reconnected under the new CA without a restart. Right
	// after rolling every NATS node the JetStream META LEADER may still be
	// re-electing — core-NATS pings answer while KV bucket lookups
	// transiently time out — so retry the dispatch through that window
	// instead of parsing a "context deadline exceeded" error as JSON.
	deadline := time.Now().Add(60 * time.Second)
	for {
		out := execInContainer(t, "admin",
			[]string{"zester", "--format", "json", "--no-color", "web-01", "test.ping"})
		if strings.Contains(out, "context deadline exceeded") && time.Now().Before(deadline) {
			t.Logf("JetStream not ready after NATS roll, retrying job dispatch: %s", strings.TrimSpace(out))
			time.Sleep(3 * time.Second)
			continue
		}
		var jobResults []cliResult
		if err := json.Unmarshal(extractJSON([]byte(out)), &jobResults); err != nil {
			t.Fatalf("parse job-mode CLI JSON: %v\nraw output: %s", err, out)
		}
		r := requireSuccess(t, jobResults, "web-01")
		if !r.Success {
			t.Fatalf("master-dispatched job failed after CA rotation: %s", r.Error)
		}
		return
	}
}
