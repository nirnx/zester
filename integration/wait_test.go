//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

// pingResponsive reports whether every id in want currently answers
// test.ping for target over --direct. Unlike execCLI it tolerates a CLI run
// that cannot even reach NATS (no JSON on stdout) — that transient state is
// exactly what callers poll through after a NATS node or master restart.
func pingResponsive(t *testing.T, target string, want []string) bool {
	t.Helper()
	ctx := context.Background()
	container, err := stack.ServiceContainer(ctx, "admin")
	if err != nil {
		return false
	}
	cmd := []string{"zester", "--format", "json", "--no-color", "--direct", "--timeout", "10s", target, "test.ping"}
	_, reader, err := container.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		return false
	}
	output, _ := io.ReadAll(reader)
	var results []cliResult
	if err := json.Unmarshal(extractJSON(output), &results); err != nil {
		return false
	}
	ok := make(map[string]bool, len(results))
	for _, r := range results {
		if r.Success {
			ok[r.PeelID] = true
		}
	}
	for _, id := range want {
		if !ok[id] {
			return false
		}
	}
	return true
}

// waitFleetResponsive polls until every node in allNodes answers test.ping —
// the honest replacement for a fixed "settle" sleep after a NATS node or a
// master was stopped or started (the suite used to burn 10-15s per step).
func waitFleetResponsive(t *testing.T, timeout time.Duration) {
	t.Helper()
	waitForCondition(t, timeout, 2*time.Second, "all peels responding to test.ping", func() bool {
		return pingResponsive(t, "*", allNodes)
	})
}

// readyMarker is the log line a compose service prints once it serves again
// after a (re)start; "" for services without one.
func readyMarker(service string) string {
	switch {
	case strings.HasPrefix(service, "master"):
		return "zester-master ready"
	case strings.HasPrefix(service, "nats"):
		return "Server is ready"
	}
	return ""
}

// startServiceAndWait starts a stopped compose service and waits until it
// logs a NEW ready marker (container logs persist across restarts, so the
// occurrence count must grow) and the whole fleet answers again.
func startServiceAndWait(t *testing.T, service string, timeout time.Duration) {
	t.Helper()
	marker := readyMarker(service)
	before := 0
	if marker != "" {
		before = strings.Count(serviceLogs(t, service), marker)
	}
	startService(t, service)
	if marker != "" {
		waitForCondition(t, timeout, 2*time.Second, service+" logs "+marker, func() bool {
			return strings.Count(serviceLogs(t, service), marker) > before
		})
	}
	waitFleetResponsive(t, timeout)
}
