//go:build integration

package integration

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

// TestEnrollmentSSE_StreamEndpoint verifies the SSE stream endpoint returns
// proper event-stream data for an existing enrollment. Since all peels are
// already enrolled during TestMain, the stream endpoint returns the terminal
// state immediately and closes — proving the SSE endpoint is wired up and
// serving events correctly.
func TestEnrollmentSSE_StreamEndpoint(t *testing.T) {
	ctx := context.Background()

	// Step 1: Get an enrollment ID from the list.
	container, err := stack.ServiceContainer(ctx, "admin")
	if err != nil {
		t.Fatalf("get admin container: %v", err)
	}

	exitCode, reader, err := container.Exec(ctx,
		[]string{"zester", "enroll", "list", "--state", "all"},
		tcexec.Multiplexed(),
	)
	if err != nil {
		t.Fatalf("exec enroll list: %v", err)
	}
	output, _ := io.ReadAll(reader)
	if exitCode != 0 {
		t.Fatalf("enroll list exited %d: %s", exitCode, string(output))
	}

	// Parse first enrollment ID from output (lines like "enr-xxx  web-01  issued  ...").
	var enrollID string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "enr-") {
			enrollID = strings.Fields(line)[0]
			break
		}
	}
	if enrollID == "" {
		t.Fatal("no enrollment ID found in list output")
	}
	t.Logf("using enrollment ID: %s", enrollID)

	// Step 2: Use curl from a peel container (has curl + CA cert) to hit the
	// SSE stream endpoint. Since the enrollment is in a terminal state (issued
	// or active), the stream sends the initial state event and closes.
	// --max-time 5 prevents hanging if the stream doesn't close.
	curlCmd := []string{
		"curl", "-s", "--max-time", "5",
		"--cacert", "/data/auth/enroll-ca.crt",
		"-H", "Accept: text/event-stream",
		"https://master:8443/api/v1/enroll/" + enrollID + "/stream",
	}

	curlOutput := execInContainer(t, "web-01", curlCmd)
	t.Logf("SSE stream output:\n%s", curlOutput)

	// Step 3: Verify the output is valid SSE format.
	if !strings.Contains(curlOutput, "event: state") {
		t.Errorf("expected 'event: state' in SSE output, got:\n%s", curlOutput)
	}
	if !strings.Contains(curlOutput, "data: ") {
		t.Errorf("expected 'data: ' in SSE output, got:\n%s", curlOutput)
	}
	// Verify the data contains a valid enrollment state.
	if !strings.Contains(curlOutput, enrollID) {
		t.Errorf("expected enrollment ID %s in SSE data, got:\n%s", enrollID, curlOutput)
	}
}

// TestEnrollmentSSE_NotFound verifies the stream endpoint returns 404 for
// a nonexistent enrollment.
func TestEnrollmentSSE_NotFound(t *testing.T) {
	curlCmd := []string{
		"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"--cacert", "/data/auth/enroll-ca.crt",
		"-H", "Accept: text/event-stream",
		"https://master:8443/api/v1/enroll/enr-nonexistent/stream",
	}

	output := execInContainer(t, "web-01", curlCmd)
	output = strings.TrimSpace(output)
	if output != "404" {
		t.Errorf("expected HTTP 404, got %s", output)
	}
}

// TestEnrollmentSSE_ContentType verifies the stream endpoint returns the
// correct Content-Type header for SSE.
func TestEnrollmentSSE_ContentType(t *testing.T) {
	ctx := context.Background()

	// Get an enrollment ID.
	container, err := stack.ServiceContainer(ctx, "admin")
	if err != nil {
		t.Fatalf("get admin container: %v", err)
	}

	exitCode, reader, err := container.Exec(ctx,
		[]string{"zester", "enroll", "list", "--state", "all"},
		tcexec.Multiplexed(),
	)
	if err != nil {
		t.Fatalf("exec enroll list: %v", err)
	}
	output, _ := io.ReadAll(reader)
	if exitCode != 0 {
		t.Fatalf("enroll list exited %d: %s", exitCode, string(output))
	}

	var enrollID string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "enr-") {
			enrollID = strings.Fields(line)[0]
			break
		}
	}
	if enrollID == "" {
		t.Fatal("no enrollment ID found")
	}

	// Check Content-Type header via curl -I (HEAD won't work for SSE, use -D).
	curlCmd := []string{
		"curl", "-s", "--max-time", "5",
		"-D", "/dev/stderr",
		"-o", "/dev/null",
		"--cacert", "/data/auth/enroll-ca.crt",
		"-H", "Accept: text/event-stream",
		"https://master:8443/api/v1/enroll/" + enrollID + "/stream",
	}

	// execInContainer captures combined stdout+stderr via Multiplexed().
	headerOutput := execInContainer(t, "web-01", curlCmd)
	if !strings.Contains(strings.ToLower(headerOutput), "text/event-stream") {
		t.Errorf("expected Content-Type: text/event-stream in headers, got:\n%s", headerOutput)
	}
}

// TestEnrollmentSSE_PeelUsedSSE verifies that peels successfully enrolled
// using the SSE stream path by checking peel logs for "SSE stream connected".
// If SSE was unavailable (e.g., timing issue during startup), the peel would
// have fallen back to polling — both paths are acceptable, but this test
// documents which path was actually taken.
func TestEnrollmentSSE_PeelUsedSSE(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Check web-01 peel logs for SSE connection message.
	container, err := stack.ServiceContainer(ctx, "web-01")
	if err != nil {
		t.Skipf("cannot access web-01 container: %v", err)
	}

	logReader, err := container.Logs(ctx)
	if err != nil {
		t.Skipf("cannot read web-01 logs: %v", err)
	}
	logs, _ := io.ReadAll(logReader)
	logStr := string(logs)

	sseUsed := strings.Contains(logStr, "SSE stream connected")
	pollingFallback := strings.Contains(logStr, "falling back to polling")

	t.Logf("web-01 enrollment path: SSE=%v, polling_fallback=%v", sseUsed, pollingFallback)

	// At minimum, the peel must have completed enrollment (it's already
	// responding to test.ping from TestMain). Log which path was used.
	if !sseUsed && !pollingFallback {
		// Neither message found — peel might have had pre-existing creds.
		t.Log("no SSE or polling messages found (peel may have had existing credentials)")
	}
}
