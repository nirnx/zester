//go:build integration

package integration

import (
	"context"
	"io"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Permissions sentinel — runs LAST (zzz_) so it observes the full run.
//
// NATS JWT grants are hand-enumerated, and JetStream needs companion subjects
// the client library mints at runtime (flow control, ack, deliver inboxes)
// that a static scan of our own code can never find. Two grant gaps shipped to
// production this way (the update-status key and the object-store flow-control
// subject), each surfacing only as a runtime "permissions violation" that the
// component logs and otherwise tolerates.
//
// This sentinel is the catch-all: every component logs a permission denial as
// a NATS async error, so scanning the whole fleet's logs at the end of the run
// fails the suite on ANY violation any test happened to exercise — including
// library-generated subjects no static check could enumerate. It pairs with
// the tests that actively drive each privileged operation (status heartbeat,
// object-store download) so those subjects are actually hit.
// ---------------------------------------------------------------------------

// containerLogs returns a service container's accumulated stdout/stderr logs.
func containerLogs(ctx context.Context, service string) (string, error) {
	c, err := stack.ServiceContainer(ctx, service)
	if err != nil {
		return "", err
	}
	rc, err := c.Logs(ctx)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	return string(data), err
}

func TestNoPermissionsViolations(t *testing.T) {
	ctx := context.Background()
	// Every component that connects to NATS with restricted creds.
	services := append([]string{"master", "master-2", "admin"}, allNodes...)

	var offenders []string
	for _, svc := range services {
		logs, err := containerLogs(ctx, svc)
		if err != nil {
			t.Logf("skip %s: cannot read logs: %v", svc, err)
			continue
		}
		for _, line := range strings.Split(logs, "\n") {
			if strings.Contains(strings.ToLower(line), "permissions violation") {
				offenders = append(offenders, svc+": "+strings.TrimSpace(line))
				break // one representative line per service is enough
			}
		}
	}

	if len(offenders) > 0 {
		t.Fatalf("NATS permissions violations in component logs — a JWT grant gap "+
			"(decode the affected creds with `zester auth lint <creds-file>`):\n\n%s",
			strings.Join(offenders, "\n\n"))
	}
}
