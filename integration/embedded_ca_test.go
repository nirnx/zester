//go:build integration

package integration

import (
	"context"
	"io"
	"strings"
	"testing"

	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

// ---------------------------------------------------------------------------
// Embedded CA — offline CLI
//
// The shared compose stack runs the master in EXTERNAL CA mode (the embedded
// path destabilizes the failover suite; it is covered by unit tests —
// pkg/enroll TestHandleBootstrap / TestHandleEnroll_TrustBinding /
// TestResolveTrust_* and internal/masterd TestStartCA_* — and end-to-end by a
// dedicated deploy). What we CAN exercise here without any master involvement
// is the offline `zester ca` command group.
// ---------------------------------------------------------------------------

// execAllowFail runs a command in a container and returns (exitCode, output)
// WITHOUT failing the test on a non-zero exit — for commands that
// intentionally exit non-zero (e.g. `ca init` refusing to overwrite).
func execAllowFail(t *testing.T, service string, cmd []string) (int, string) {
	t.Helper()
	ctx := context.Background()
	container, err := stack.ServiceContainer(ctx, service)
	if err != nil {
		t.Fatalf("get container %s: %v", service, err)
	}
	code, reader, err := container.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("exec in %s %v: %v", service, cmd, err)
	}
	out, _ := io.ReadAll(reader)
	return code, string(out)
}

// TestCACLI_Offline exercises the offline `zester ca` verbs inside a container:
// init a fresh CA, read its fingerprint, refuse to overwrite, and issue a NATS
// server cert.
func TestCACLI_Offline(t *testing.T) {
	dir := "/tmp/ca-cli-test"
	execInContainer(t, "admin", []string{"rm", "-rf", dir})
	t.Cleanup(func() { execInContainer(t, "admin", []string{"rm", "-rf", dir}) })

	initOut := execInContainer(t, "admin", []string{"zester", "ca", "init", "--dir", dir})
	if !strings.Contains(initOut, "SPKI pin:") {
		t.Fatalf("ca init did not print an SPKI pin:\n%s", initOut)
	}

	fpr := execInContainer(t, "admin", []string{"zester", "ca", "fingerprint", "--dir", dir})
	if !strings.HasPrefix(strings.TrimSpace(fpr), "sha256:") {
		t.Errorf("ca fingerprint = %q, want sha256: pin", fpr)
	}

	// init must refuse to overwrite an existing CA (exits non-zero).
	code, reinit := execAllowFail(t, "admin", []string{"zester", "ca", "init", "--dir", dir})
	if code == 0 || !strings.Contains(strings.ToLower(reinit), "already exists") {
		t.Errorf("second ca init did not refuse (exit %d):\n%s", code, reinit)
	}

	issue := execInContainer(t, "admin", []string{
		"zester", "ca", "issue", "nats-server", "--dir", dir, "--dns", "nats.example,localhost", "--out", dir + "/out",
	})
	if !strings.Contains(issue, "Issued nats-server") {
		t.Errorf("ca issue nats-server failed:\n%s", issue)
	}
}
