//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/modules/compose"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/settings"
)

// cliResult mirrors cmd/zester/cmd/output.go outputRecord for JSON parsing.
type cliResult struct {
	PeelID  string        `json:"peel_id"`
	Success bool          `json:"success"`
	Results []stateResult `json:"results"`
	Error   string        `json:"error"`
}

// logStateErrors logs per-state error details for a failed result.
func (r cliResult) logStateErrors(t *testing.T) {
	t.Helper()
	for _, sr := range r.Results {
		if sr.Error != "" {
			t.Logf("  state %s: error=%q", sr.Name, sr.Error)
		} else if sr.Skipped {
			t.Logf("  state %s: skipped (%s)", sr.Name, sr.SkipReason)
		}
	}
}

// requireSuccess fails the test (Fatal) with verbose per-state error detail.
func (r cliResult) requireSuccess(t *testing.T, context string) {
	t.Helper()
	if r.Success {
		return
	}
	r.logStateErrors(t)
	t.Fatalf("%s failed: %s", context, r.Error)
}

// checkSuccess reports failure (Errorf) with verbose per-state error detail.
// Use in loops where other iterations should still run.
func (r cliResult) checkSuccess(t *testing.T, context string) {
	t.Helper()
	if r.Success {
		return
	}
	r.logStateErrors(t)
	t.Errorf("%s failed: %s", context, r.Error)
}

type stateResult struct {
	Name       string            `json:"name"`
	Changed    bool              `json:"changed"`
	Details    map[string]string `json:"details,omitempty"`
	Duration   int64             `json:"duration"`
	Diff       string            `json:"diff,omitempty"`
	Error      string            `json:"error,omitempty"`
	Skipped    bool              `json:"skipped,omitempty"`
	SkipReason string            `json:"skip_reason,omitempty"`
}

// allPeels is the set of Ubuntu-based peel IDs in the Docker Compose stack
// (used by tests that need real useradd/apt tooling).
var allPeels = []string{"web-01", "web-02", "web-03", "db-01", "db-02"}

// allNodes is every enrolled node in the stack: the five peels plus wd-01,
// the Alpine-based peel supervised by zester-watchdog (packaged topology).
// Fleet-wide fan-out assertions count against this list.
var allNodes = []string{"web-01", "web-02", "web-03", "db-01", "db-02", "wd-01"}

// stack is the shared Docker Compose stack, started once in TestMain.
var stack *compose.DockerCompose

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var err error
	stack, err = compose.NewDockerComposeWith(
		compose.StackIdentifier("zester-integration"),
		compose.WithStackFiles(
			"../docker-compose.yml",
			"docker-compose.multi-master.yml",
			"docker-compose.nats-cluster.yml",
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compose setup: %v\n", err)
		os.Exit(1)
	}

	// Full cleanup: remove all containers, volumes, networks, and locally-built
	// images from any previous run so every test session starts completely fresh.
	// RemoveImagesLocal removes project-built images (master, peel, init, admin)
	// while keeping base images (nats, golang, ubuntu) in the Docker cache.
	_ = stack.Down(ctx, compose.RemoveOrphans(true), compose.RemoveVolumes(true), compose.RemoveImagesLocal)

	// Start the full stack. Docker Compose handles ordering via depends_on:
	//   init (certs) → nats cluster → masters → peels
	// We don't use compose.Wait(true) because it aborts immediately on any
	// container exit, making debugging impossible.
	fmt.Println("==> Starting full stack...")
	if err = stack.Up(ctx, compose.Recreate("force")); err != nil {
		fmt.Fprintf(os.Stderr, "compose up: %v\n", err)
		teardown()
		os.Exit(1)
	}

	// Wait for NATS cluster: all 3 nodes must log "Server is ready".
	fmt.Println("==> Waiting for NATS cluster...")
	for _, svc := range []string{"nats", "nats-2", "nats-3"} {
		if err := waitForServiceLog(ctx, svc, "Server is ready", 2*time.Minute); err != nil {
			dumpServiceLogs(ctx, svc)
			fmt.Fprintf(os.Stderr, "nats ready: %v\n", err)
			teardown()
			os.Exit(1)
		}
	}

	// Wait for JetStream cluster consensus: at least one node must elect a
	// metadata leader before KV bucket creation (R3) can succeed.
	fmt.Println("==> Waiting for JetStream metadata leader election...")
	if err := waitForAnyServiceLog(ctx, []string{"nats", "nats-2", "nats-3"}, "metadata leader", 2*time.Minute); err != nil {
		for _, svc := range []string{"nats", "nats-2", "nats-3"} {
			dumpServiceLogs(ctx, svc)
		}
		fmt.Fprintf(os.Stderr, "jetstream cluster: %v\n", err)
		teardown()
		os.Exit(1)
	}
	fmt.Println("==> NATS cluster ready (JetStream meta leader elected)")

	// Wait for masters: both must log "zester-master ready" (fires after
	// JetStream storage init, settings publish, job system, heartbeat,
	// and enrollment server are all up).
	fmt.Println("==> Waiting for masters...")
	for _, svc := range []string{"master", "master-2"} {
		if err := waitForServiceLog(ctx, svc, "zester-master ready", 3*time.Minute); err != nil {
			dumpServiceLogs(ctx, svc)
			fmt.Fprintf(os.Stderr, "master ready: %v\n", err)
			teardown()
			os.Exit(1)
		}
	}
	fmt.Println("==> Masters ready")

	// Approve peel enrollments (peels retry with backoff until approved).
	if err := approveEnrollments(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "approve enrollments: %v\n", err)
		teardown()
		os.Exit(1)
	}

	// Wait for every node to respond to test.ping. The timeout must account
	// for: enrollment connection retries (~60-90s), poll-until-approved backoff
	// (10s → 20s → 40s → 80s worst case), and facts publishing (~5s).
	if err := waitForPeels(ctx, 5*time.Minute); err != nil {
		for _, svc := range allNodes {
			dumpServiceLogs(ctx, svc)
		}
		fmt.Fprintf(os.Stderr, "wait for peels: %v\n", err)
		teardown()
		os.Exit(1)
	}
	fmt.Println("==> All services ready, running tests...")

	code := m.Run()
	teardown()
	os.Exit(code)
}

func teardown() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if stack != nil {
		if err := stack.Down(ctx, compose.RemoveOrphans(true), compose.RemoveVolumes(true), compose.RemoveImagesLocal); err != nil {
			fmt.Fprintf(os.Stderr, "compose down: %v\n", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// execInContainer runs a command in a named Docker Compose service and returns
// the combined stdout+stderr output. Fails the test on nonzero exit code.
func execInContainer(t *testing.T, service string, cmd []string) string {
	t.Helper()
	ctx := context.Background()

	container, err := stack.ServiceContainer(ctx, service)
	if err != nil {
		t.Fatalf("get container %s: %v", service, err)
	}

	exitCode, reader, err := container.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("exec in %s %v: %v", service, cmd, err)
	}

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read exec output from %s: %v", service, err)
	}

	if exitCode != 0 {
		t.Fatalf("exec in %s exited %d: %s", service, exitCode, string(output))
	}
	return string(output)
}

// execCLI runs zester with --direct --format json in the admin container
// (request/reply, bypasses the job system). Parses the JSON output into
// a slice of cliResult. Tolerates nonzero exit codes (the CLI returns 1
// on any peel error) and only fails if the output cannot be parsed.
func execCLI(t *testing.T, target, module string, args ...string) []cliResult {
	t.Helper()

	cmd := []string{"zester", "--format", "json", "--no-color", "--direct", target, module}
	cmd = append(cmd, args...)
	return runZester(t, cmd)
}

// execCLIJob runs zester in job mode (default, no --direct) with --format json.
// This exercises the full dispatch flow: CLI → master → peel → return.
// Status messages ("Targeting...", "Job dispatched") go to stderr so stdout
// contains only the JSON result array.
func execCLIJob(t *testing.T, target, module string, args ...string) []cliResult {
	t.Helper()

	cmd := []string{"zester", "--format", "json", "--no-color", target, module}
	cmd = append(cmd, args...)
	return runZester(t, cmd)
}

// runZester executes a zester command in the admin container and parses JSON output.
func runZester(t *testing.T, cmd []string) []cliResult {
	t.Helper()

	ctx := context.Background()
	container, err := stack.ServiceContainer(ctx, "admin")
	if err != nil {
		t.Fatalf("get admin container: %v", err)
	}

	_, reader, err := container.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("exec CLI %v: %v", cmd, err)
	}

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read CLI output: %v", err)
	}

	jsonBytes := extractJSON(output)
	var results []cliResult
	if err := json.Unmarshal(jsonBytes, &results); err != nil {
		t.Fatalf("parse CLI JSON: %v\nraw output: %s", err, string(output))
	}
	return results
}

// extractJSON extracts the JSON array from Multiplexed() output that may
// contain stderr noise (e.g., error messages) mixed with stdout JSON.
func extractJSON(data []byte) []byte {
	s := string(data)
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start == -1 || end == -1 || end < start {
		return data
	}
	return []byte(s[start : end+1])
}

// requireSuccess asserts that exactly one result exists for the given peel ID
// and that it succeeded. Returns the matching result for further assertions.
func requireSuccess(t *testing.T, results []cliResult, expectedPeel string) cliResult {
	t.Helper()
	for _, r := range results {
		if r.PeelID == expectedPeel {
			r.requireSuccess(t, fmt.Sprintf("peel %s", expectedPeel))
			return r
		}
	}
	t.Fatalf("no result found for peel %s in %d results", expectedPeel, len(results))
	return cliResult{} // unreachable
}

// waitForPeels polls test.ping against '*' until every expected node
// responds successfully, or the timeout expires. Each iteration re-runs
// auto-approve first: nodes that enroll after the initial approval batch
// (e.g. wd-01, whose watchdog starts the peel child on its own schedule)
// would otherwise stay pending forever.
func waitForPeels(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	expected := make(map[string]bool, len(allNodes))
	for _, id := range allNodes {
		expected[id] = true
	}

	for time.Now().Before(deadline) {
		container, err := stack.ServiceContainer(ctx, "admin")
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}

		// Approve any enrollments that arrived since the last pass
		// (idempotent; exits 0 when nothing is pending).
		_, approveOut, approveErr := container.Exec(ctx,
			[]string{"sh", "/playground/auto-approve.sh"}, tcexec.Multiplexed())
		if approveErr == nil {
			if out, _ := io.ReadAll(approveOut); strings.Contains(string(out), "Approving") {
				fmt.Printf("waitForPeels: %s", string(out))
			}
		}

		cmd := []string{"zester", "--format", "json", "--no-color", "--direct", "*", "test.ping"}
		_, reader, err := container.Exec(ctx, cmd, tcexec.Multiplexed())
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}

		output, _ := io.ReadAll(reader)

		var results []cliResult
		if err := json.Unmarshal(extractJSON(output), &results); err != nil {
			fmt.Printf("waitForPeels: JSON parse error: %v\nraw output (%d bytes): %s\n", err, len(output), string(output))
			time.Sleep(3 * time.Second)
			continue
		}

		found := make(map[string]bool)
		for _, r := range results {
			if r.Success {
				found[r.PeelID] = true
			}
		}

		if len(found) >= len(expected) {
			allReady := true
			for id := range expected {
				if !found[id] {
					allReady = false
					break
				}
			}
			if allReady {
				fmt.Printf("all %d peels responding\n", len(expected))
				return nil
			}
		}

		var missing []string
		for id := range expected {
			if !found[id] {
				missing = append(missing, id)
			}
		}
		sort.Strings(missing)
		fmt.Printf("waiting for peels: %d/%d ready (missing: %v)\n", len(found), len(expected), missing)
		time.Sleep(3 * time.Second)
	}

	return fmt.Errorf("timed out waiting for all peels after %s", timeout)
}

// waitForServiceLog polls a Docker Compose service's logs until the target
// string appears or the timeout expires. Returns nil on success.
func waitForServiceLog(ctx context.Context, service, target string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		container, err := stack.ServiceContainer(ctx, service)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		logReader, err := container.Logs(ctx)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		logs, _ := io.ReadAll(logReader)
		if strings.Contains(string(logs), target) {
			fmt.Printf("==> %s: found %q in logs\n", service, target)
			return nil
		}

		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for %q in %s logs after %s", target, service, timeout)
}

// waitForAnyServiceLog polls multiple services' logs until any one of them
// contains the target string. Returns nil on first match, error on timeout.
func waitForAnyServiceLog(ctx context.Context, services []string, target string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, svc := range services {
			container, err := stack.ServiceContainer(ctx, svc)
			if err != nil {
				continue
			}
			logReader, err := container.Logs(ctx)
			if err != nil {
				continue
			}
			logs, _ := io.ReadAll(logReader)
			if strings.Contains(string(logs), target) {
				fmt.Printf("==> %s: found %q in logs\n", svc, target)
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for %q in any of %v after %s", target, services, timeout)
}

// dumpServiceLogs prints the last logs from a Docker Compose service to stderr
// for debugging when a service fails to start.
func dumpServiceLogs(ctx context.Context, service string) {
	container, err := stack.ServiceContainer(ctx, service)
	if err != nil {
		fmt.Fprintf(os.Stderr, "--- %s logs: container not found: %v ---\n", service, err)
		return
	}

	logReader, err := container.Logs(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "--- %s logs: error reading: %v ---\n", service, err)
		return
	}

	logs, _ := io.ReadAll(logReader)
	fmt.Fprintf(os.Stderr, "--- %s logs ---\n%s\n--- end %s logs ---\n", service, string(logs), service)
}

// approveEnrollments runs auto-approve.sh in the admin container, retrying
// until enrollments are approved or the timeout expires.
// In a JetStream cluster, the master's storage init can take ~2-3 minutes
// (RAFT meta-leader warm-up), then peels need ~30s to complete enrollment.
func approveEnrollments(ctx context.Context) error {
	deadline := time.Now().Add(5 * time.Minute)
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		container, err := stack.ServiceContainer(ctx, "admin")
		if err != nil {
			time.Sleep(10 * time.Second)
			continue
		}

		exitCode, reader, err := container.Exec(
			ctx,
			[]string{"sh", "/playground/auto-approve.sh"},
			tcexec.Multiplexed(),
		)
		if err != nil {
			time.Sleep(10 * time.Second)
			continue
		}

		output, _ := io.ReadAll(reader)
		fmt.Printf("auto-approve attempt %d (exit %d): %s\n", attempt, exitCode, string(output))

		if exitCode == 0 && strings.Contains(string(output), "Approved") {
			return nil
		}

		time.Sleep(10 * time.Second)
	}
	return fmt.Errorf("failed to approve enrollments after %d attempts (5m timeout)", attempt)
}

// hasService returns whether a named service exists in the compose stack.
// Used as a skip guard for tests that depend on optional services (e.g., nats-2).
func hasService(service string) bool {
	ctx := context.Background()
	_, err := stack.ServiceContainer(ctx, service)
	return err == nil
}

// cleanupFile registers a t.Cleanup that removes a file from a container.
func cleanupFile(t *testing.T, service, path string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		container, err := stack.ServiceContainer(ctx, service)
		if err != nil {
			return // best effort
		}
		//nolint:errcheck
		container.Exec(ctx, []string{"rm", "-f", path}, tcexec.Multiplexed())
	})
}

// ---------------------------------------------------------------------------
// Tests 1-4: Basic Connectivity (both direct and job modes)
// ---------------------------------------------------------------------------

func TestPing_Direct(t *testing.T) {
	results := execCLI(t, "web-01", "test.ping")
	r := requireSuccess(t, results, "web-01")

	if len(r.Results) == 0 {
		t.Fatal("no results returned")
	}
	if got := r.Results[0].Details["result"]; got != "true" {
		t.Errorf("expected result=\"true\", got %q", got)
	}
}

func TestPing_Job(t *testing.T) {
	results := execCLIJob(t, "web-01", "test.ping")
	r := requireSuccess(t, results, "web-01")

	if len(r.Results) == 0 {
		t.Fatal("no results returned")
	}
	if got := r.Results[0].Details["result"]; got != "true" {
		t.Errorf("expected result=\"true\", got %q", got)
	}
}

func TestCmdRun_Direct(t *testing.T) {
	results := execCLI(t, "web-01", "cmd.run", "echo hello-direct")
	r := requireSuccess(t, results, "web-01")

	if len(r.Results) == 0 {
		t.Fatal("no results returned")
	}
	if !strings.Contains(r.Results[0].Details["stdout"], "hello-direct") {
		t.Errorf("expected stdout to contain 'hello-direct', got %q", r.Results[0].Details["stdout"])
	}
}

func TestCmdRun_Job(t *testing.T) {
	results := execCLIJob(t, "web-01", "cmd.run", "echo hello-job")
	r := requireSuccess(t, results, "web-01")

	if len(r.Results) == 0 {
		t.Fatal("no results returned")
	}
	if !strings.Contains(r.Results[0].Details["stdout"], "hello-job") {
		t.Errorf("expected stdout to contain 'hello-job', got %q", r.Results[0].Details["stdout"])
	}
}

func TestMultiPeel_Fanout_Direct(t *testing.T) {
	results := execCLI(t, "*", "test.ping")

	if len(results) != len(allNodes) {
		t.Fatalf("expected %d results, got %d", len(allNodes), len(results))
	}

	seen := make(map[string]bool)
	for _, r := range results {
		r.checkSuccess(t, fmt.Sprintf("peel %s", r.PeelID))
		seen[r.PeelID] = true
	}

	for _, id := range allNodes {
		if !seen[id] {
			t.Errorf("missing result for peel %s", id)
		}
	}
}

func TestMultiPeel_Fanout_Job(t *testing.T) {
	results := execCLIJob(t, "*", "test.ping")

	if len(results) != len(allNodes) {
		t.Fatalf("expected %d results, got %d", len(allNodes), len(results))
	}

	seen := make(map[string]bool)
	for _, r := range results {
		r.checkSuccess(t, fmt.Sprintf("peel %s", r.PeelID))
		seen[r.PeelID] = true
	}

	for _, id := range allNodes {
		if !seen[id] {
			t.Errorf("missing result for peel %s", id)
		}
	}
}

func TestTargeting_Glob(t *testing.T) {
	results := execCLI(t, "web-*", "test.ping")

	if len(results) != 3 {
		t.Fatalf("expected 3 results for web-*, got %d", len(results))
	}

	var ids []string
	for _, r := range results {
		r.checkSuccess(t, fmt.Sprintf("peel %s", r.PeelID))
		ids = append(ids, r.PeelID)
	}
	sort.Strings(ids)

	expected := []string{"web-01", "web-02", "web-03"}
	for i, id := range expected {
		if ids[i] != id {
			t.Errorf("position %d: expected %s, got %s", i, id, ids[i])
		}
	}
}

func TestTargeting_Glob_Job(t *testing.T) {
	results := execCLIJob(t, "web-*", "test.ping")

	if len(results) != 3 {
		t.Fatalf("expected 3 results for web-*, got %d", len(results))
	}

	var ids []string
	for _, r := range results {
		r.checkSuccess(t, fmt.Sprintf("peel %s", r.PeelID))
		ids = append(ids, r.PeelID)
	}
	sort.Strings(ids)

	expected := []string{"web-01", "web-02", "web-03"}
	for i, id := range expected {
		if ids[i] != id {
			t.Errorf("position %d: expected %s, got %s", i, id, ids[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Tests 5-6: Facts & Settings
// ---------------------------------------------------------------------------

func TestFactsGet(t *testing.T) {
	results := execCLI(t, "web-01", "facts.get", "os.family")
	r := requireSuccess(t, results, "web-01")

	if len(r.Results) == 0 {
		t.Fatal("no results returned")
	}
	// Peels run Ubuntu 24.04 (Dockerfile.peel), so os.family = "debian".
	if got := r.Results[0].Details["result"]; got != "debian" {
		t.Errorf("expected os.family=\"debian\", got %q", got)
	}
}

func TestSettingsGet(t *testing.T) {
	results := execCLI(t, "web-01", "settings.get", "timezone")
	r := requireSuccess(t, results, "web-01")

	if len(r.Results) == 0 {
		t.Fatal("no results returned")
	}
	// From playground/settings/common/base.zy: timezone: UTC
	if got := r.Results[0].Details["result"]; got != "UTC" {
		t.Errorf("expected timezone=\"UTC\", got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Tests 7-10: Go State Modules (both modes)
// ---------------------------------------------------------------------------

func TestFileManaged_Direct(t *testing.T) {
	cleanupFile(t, "web-01", "/tmp/integration-direct.txt")

	results := execCLI(t, "web-01", "file.managed", "/tmp/integration-direct.txt",
		"content=direct mode content", "mode=0644")
	r := requireSuccess(t, results, "web-01")

	if len(r.Results) == 0 {
		t.Fatal("no results returned")
	}
	if !r.Results[0].Changed {
		t.Error("expected changed=true for new file creation")
	}

	// Verify file content via cmd.run.
	verify := execCLI(t, "web-01", "cmd.run", "cat /tmp/integration-direct.txt")
	vr := requireSuccess(t, verify, "web-01")
	if !strings.Contains(vr.Results[0].Details["stdout"], "direct mode content") {
		t.Errorf("expected file content 'direct mode content', got %q", vr.Results[0].Details["stdout"])
	}
}

func TestFileManaged_Job(t *testing.T) {
	cleanupFile(t, "web-01", "/tmp/integration-job.txt")

	results := execCLIJob(t, "web-01", "file.managed", "/tmp/integration-job.txt",
		"content=job mode content", "mode=0644")
	r := requireSuccess(t, results, "web-01")

	if len(r.Results) == 0 {
		t.Fatal("no results returned")
	}
	if !r.Results[0].Changed {
		t.Error("expected changed=true for new file creation")
	}

	// Verify file content via direct cmd.run (fast verification).
	verify := execCLI(t, "web-01", "cmd.run", "cat /tmp/integration-job.txt")
	vr := requireSuccess(t, verify, "web-01")
	if !strings.Contains(vr.Results[0].Details["stdout"], "job mode content") {
		t.Errorf("expected file content 'job mode content', got %q", vr.Results[0].Details["stdout"])
	}
}

func TestStateApply_GoModules_Direct(t *testing.T) {
	cleanupFile(t, "web-01", "/tmp/hello-zester.txt")

	results := execCLI(t, "web-01", "state.apply", "hello")
	r := requireSuccess(t, results, "web-01")

	if len(r.Results) == 0 {
		t.Fatal("no results returned")
	}

	// The hello state writes /tmp/hello-zester.txt with templated content.
	verify := execCLI(t, "web-01", "cmd.run", "cat /tmp/hello-zester.txt")
	vr := requireSuccess(t, verify, "web-01")
	if !strings.Contains(vr.Results[0].Details["stdout"], "Hello from Zester") {
		t.Errorf("expected 'Hello from Zester' in file, got %q", vr.Results[0].Details["stdout"])
	}
}

func TestStateApply_GoModules_Job(t *testing.T) {
	cleanupFile(t, "web-02", "/tmp/hello-zester.txt")

	results := execCLIJob(t, "web-02", "state.apply", "hello")
	r := requireSuccess(t, results, "web-02")

	if len(r.Results) == 0 {
		t.Fatal("no results returned")
	}

	verify := execCLI(t, "web-02", "cmd.run", "cat /tmp/hello-zester.txt")
	vr := requireSuccess(t, verify, "web-02")
	if !strings.Contains(vr.Results[0].Details["stdout"], "Hello from Zester") {
		t.Errorf("expected 'Hello from Zester' in file, got %q", vr.Results[0].Details["stdout"])
	}
}

// ---------------------------------------------------------------------------
// Tests 11-14: Starlark Modules (job mode for state.apply, direct for reads)
// ---------------------------------------------------------------------------

func TestStateApply_StarlarkGlobal(t *testing.T) {
	cleanupFile(t, "web-01", "/tmp/starlark-report.txt")

	results := execCLIJob(t, "web-01", "state.apply", "starlark-test")
	r := requireSuccess(t, results, "web-01")

	if len(r.Results) == 0 {
		t.Fatal("no results returned")
	}

	// Find the sysinfo.report state result.
	found := false
	for _, sr := range r.Results {
		if strings.Contains(sr.Name, "sysinfo.report") || strings.Contains(sr.Name, "system-report") {
			if !sr.Changed {
				t.Error("expected changed=true for sysinfo.report")
			}
			found = true
			break
		}
	}
	if !found {
		names := make([]string, len(r.Results))
		for i, sr := range r.Results {
			names[i] = sr.Name
		}
		t.Errorf("sysinfo.report result not found in: %v", names)
	}
}

func TestStarlark_Idempotent(t *testing.T) {
	// Ensure the report file exists from a fresh apply.
	cleanupFile(t, "web-01", "/tmp/starlark-report.txt")

	results := execCLIJob(t, "web-01", "state.apply", "starlark-test")
	requireSuccess(t, results, "web-01")

	// Second apply: report_check sees the file exists → needs_change=false.
	results2 := execCLIJob(t, "web-01", "state.apply", "starlark-test")
	r := requireSuccess(t, results2, "web-01")

	for _, sr := range r.Results {
		if strings.Contains(sr.Name, "sysinfo.report") || strings.Contains(sr.Name, "system-report") {
			if sr.Changed {
				t.Error("expected changed=false on second apply (idempotent check)")
			}
			return
		}
	}
	t.Error("sysinfo.report result not found in second apply")
}

func TestStateApply_StarlarkDynamic(t *testing.T) {
	cleanupFile(t, "web-01", "/tmp/greeter-output.txt")

	results := execCLIJob(t, "web-01", "state.apply", "dynamic-test")
	r := requireSuccess(t, results, "web-01")

	if len(r.Results) == 0 {
		t.Fatal("no results returned")
	}

	// Find the greeter.hello state result.
	found := false
	for _, sr := range r.Results {
		if strings.Contains(sr.Name, "greeter.hello") || strings.Contains(sr.Name, "greet-user") {
			if !sr.Changed {
				t.Error("expected changed=true for greeter.hello")
			}
			found = true
			break
		}
	}
	if !found {
		names := make([]string, len(r.Results))
		for i, sr := range r.Results {
			names[i] = sr.Name
		}
		t.Errorf("greeter.hello result not found in: %v", names)
	}
}

func TestStarlark_HotReload(t *testing.T) {
	cleanupFile(t, "web-01", "/tmp/greeter-output.txt")

	// Step 1: Apply v1 (original greeter.star) via job mode.
	results := execCLIJob(t, "web-01", "state.apply", "dynamic-test")
	requireSuccess(t, results, "web-01")

	// Verify v1 output contains the original name.
	v1 := execCLI(t, "web-01", "cmd.run", "cat /tmp/greeter-output.txt")
	v1r := requireSuccess(t, v1, "web-01")
	if !strings.Contains(v1r.Results[0].Details["stdout"], "Zester Developer") {
		t.Fatalf("v1 expected 'Zester Developer', got %q", v1r.Results[0].Details["stdout"])
	}

	// Step 2: Overwrite greeter.star with v2 code in the peel container.
	v2Code := `def hello(id, config):
    name = config.get("name", "world")
    msg = "Hot-reloaded v2: Hello, %s!" % name
    file_write("/tmp/greeter-output.txt", msg + "\n", 0o644)
    return {"changed": True, "diff": "v2 greeted %s" % name, "details": {"name": name}}

def hello_check(id, config):
    return {"needs_change": True}
`
	writeCmd := []string{
		"sh", "-c",
		"cat > /data/states-cache/dynamic-test/_modules/greeter.star << 'STAREOF'\n" + v2Code + "STAREOF",
	}
	execInContainer(t, "web-01", writeCmd)

	// Step 3: Remove old output so the next apply writes fresh.
	execInContainer(t, "web-01", []string{"rm", "-f", "/tmp/greeter-output.txt"})

	// Step 4: Ensure mtime is strictly newer (filesystem timestamp resolution).
	time.Sleep(1 * time.Second)

	// Step 5: Apply again via job mode — Loader detects newer mtime, re-parses the module.
	results2 := execCLIJob(t, "web-01", "state.apply", "dynamic-test")
	requireSuccess(t, results2, "web-01")

	// Step 6: Verify v2 output.
	v2out := execCLI(t, "web-01", "cmd.run", "cat /tmp/greeter-output.txt")
	v2r := requireSuccess(t, v2out, "web-01")
	if !strings.Contains(v2r.Results[0].Details["stdout"], "Hot-reloaded v2") {
		t.Errorf("v2 expected 'Hot-reloaded v2', got %q", v2r.Results[0].Details["stdout"])
	}

	// Step 7: Restore original greeter.star on cleanup.
	t.Cleanup(func() {
		origCode := `def hello(id, config):
    """A formula-specific module that only exists inside dynamic-test/_modules/."""
    name = config.get("name", "world")
    msg = "Hello, %s! From a dynamically loaded Starlark module." % name
    file_write("/tmp/greeter-output.txt", msg + "\n", 0o644)
    log.info("greeter.hello: wrote greeting for %s" % name)
    return {"changed": True, "diff": "greeted %s" % name, "details": {"name": name}}

def hello_check(id, config):
    if file_exists("/tmp/greeter-output.txt"):
        return {"needs_change": False}
    return {"needs_change": True, "diff": "greeting not yet written"}
`
		restoreCmd := []string{
			"sh", "-c",
			"cat > /data/states-cache/dynamic-test/_modules/greeter.star << 'STAREOF'\n" + origCode + "STAREOF",
		}
		ctx := context.Background()
		container, err := stack.ServiceContainer(ctx, "web-01")
		if err != nil {
			return // best effort
		}
		//nolint:errcheck
		container.Exec(ctx, restoreCmd, tcexec.Multiplexed())
	})
}

// ---------------------------------------------------------------------------
// Tests 15-16: file.managed Template Rendering (context, defaults, facts)
// ---------------------------------------------------------------------------

func TestFileManagedTemplate_Direct(t *testing.T) {
	cleanupFile(t, "web-01", "/tmp/template-test.txt")

	results := execCLI(t, "web-01", "state.apply", "template-test")
	r := requireSuccess(t, results, "web-01")

	if len(r.Results) == 0 {
		t.Fatal("no results returned")
	}
	if !r.Results[0].Changed {
		t.Error("expected changed=true for new file creation")
	}

	// Verify rendered content: context overrides defaults for app_port,
	// defaults provides log_level, and facts.os.family is rendered.
	verify := execCLI(t, "web-01", "cmd.run", "cat /tmp/template-test.txt")
	vr := requireSuccess(t, verify, "web-01")
	stdout := vr.Results[0].Details["stdout"]

	if !strings.Contains(stdout, "app_port=8080") {
		t.Errorf("expected app_port=8080 (from context), got %q", stdout)
	}
	if !strings.Contains(stdout, "log_level=info") {
		t.Errorf("expected log_level=info (from defaults), got %q", stdout)
	}
	// Peels run Ubuntu 24.04, so os.family should be "debian"
	if !strings.Contains(stdout, "os_family=debian") {
		t.Errorf("expected os_family=debian (from facts), got %q", stdout)
	}
}

func TestFileManagedTemplate_Job(t *testing.T) {
	cleanupFile(t, "web-02", "/tmp/template-test.txt")

	results := execCLIJob(t, "web-02", "state.apply", "template-test")
	r := requireSuccess(t, results, "web-02")

	if len(r.Results) == 0 {
		t.Fatal("no results returned")
	}
	if !r.Results[0].Changed {
		t.Error("expected changed=true for new file creation")
	}

	verify := execCLI(t, "web-02", "cmd.run", "cat /tmp/template-test.txt")
	vr := requireSuccess(t, verify, "web-02")
	stdout := vr.Results[0].Details["stdout"]

	if !strings.Contains(stdout, "app_port=8080") {
		t.Errorf("expected app_port=8080 (from context), got %q", stdout)
	}
	if !strings.Contains(stdout, "log_level=info") {
		t.Errorf("expected log_level=info (from defaults), got %q", stdout)
	}
	if !strings.Contains(stdout, "os_family=debian") {
		t.Errorf("expected os_family=debian (from facts), got %q", stdout)
	}
}

func TestJobListShowsOwner(t *testing.T) {
	// Create a job via the normal dispatch path so it gets an owner (master ID).
	execCLIJob(t, "web-01", "test.ping")

	// Run "zester job list" to get the raw text output.
	output := execInContainer(t, "admin", []string{"zester", "job", "list"})

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected header + at least one row, got %d lines: %s", len(lines), output)
	}

	// Header must contain OWNER.
	if !strings.Contains(lines[0], "OWNER") {
		t.Errorf("header missing OWNER column: %s", lines[0])
	}

	// At least one data row should have a non-"-" owner (the master ID).
	foundOwner := false
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[4] != "-" {
			foundOwner = true
			break
		}
	}
	if !foundOwner {
		t.Errorf("no job row has a non-empty owner; output:\n%s", output)
	}
}

// connectNATS connects to NATS from the test host by extracting admin creds
// from the admin container and using the NATS service's mapped port.
func connectNATS(t *testing.T) *nats.Conn {
	t.Helper()
	ctx := context.Background()

	// Extract admin creds and NATS CA from the admin container.
	credsContent := execInContainer(t, "admin", []string{"cat", "/data/auth/admin.creds"})
	caContent := execInContainer(t, "admin", []string{"cat", "/data/auth/nats-ca.crt"})

	credsFile := filepath.Join(t.TempDir(), "admin.creds")
	if err := os.WriteFile(credsFile, []byte(credsContent), 0600); err != nil {
		t.Fatalf("write creds file: %v", err)
	}
	caFile := filepath.Join(t.TempDir(), "nats-ca.crt")
	if err := os.WriteFile(caFile, []byte(caContent), 0600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}

	// Get the NATS container's host-mapped port.
	natsContainer, err := stack.ServiceContainer(ctx, "nats")
	if err != nil {
		t.Fatalf("get nats container: %v", err)
	}
	host, err := natsContainer.Host(ctx)
	if err != nil {
		t.Fatalf("get nats host: %v", err)
	}
	mappedPort, err := natsContainer.MappedPort(ctx, "4222/tcp")
	if err != nil {
		t.Fatalf("get nats mapped port: %v", err)
	}

	url := fmt.Sprintf("tls://%s:%s", host, mappedPort.Port())
	nc, err := nats.Connect(url,
		nats.UserCredentials(credsFile),
		nats.RootCAs(caFile),
		nats.SkipHostLookup(),
		nats.NoReconnect(),
	)
	if err != nil {
		t.Fatalf("connect to NATS: %v", err)
	}
	t.Cleanup(func() { nc.Close() })
	return nc
}

// patchSettingsManifest rewrites the _manifest entry for key to carry the
// SHA-256 of content, mirroring what the master's publisher does on every
// publish batch. This is the contract for out-of-band publishers: the peel
// resolver verifies every file it loads against the manifest (torn-read
// protection), so a test that simulates a settings change with a bare kv.Put
// must also patch the manifest BEFORE bumping _revision — otherwise the
// resolver rejects the batch as a hash mismatch and keeps serving its
// last-known-good settings.
func patchSettingsManifest(ctx context.Context, kv bus.KV, key string, content []byte) error {
	var manifest []settings.ManifestEntry
	if err := bus.KVGet(ctx, kv, settings.ManifestKey, &manifest); err != nil {
		return fmt.Errorf("get settings manifest: %w", err)
	}
	sum := sha256.Sum256(content)
	found := false
	for i := range manifest {
		if manifest[i].Key == key {
			manifest[i].SHA256 = hex.EncodeToString(sum[:])
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("settings manifest has no entry for %q", key)
	}
	if _, err := bus.KVPut(ctx, kv, settings.ManifestKey, manifest); err != nil {
		return fmt.Errorf("put settings manifest: %w", err)
	}
	return nil
}

// waitForSetting polls settings.get on web-01 until the key reports the
// expected value — covering the master publish, the peel's revision watch +
// debounced re-resolve, and template rendering end to end.
func waitForSetting(t *testing.T, key, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		results := execCLI(t, "web-01", "settings.get", key)
		if len(results) == 1 && results[0].Success && len(results[0].Results) > 0 {
			last = results[0].Results[0].Details["result"]
			if last == want {
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("setting %q = %q, want %q within %s", key, last, want, timeout)
}

// TestSettingsWatchReResolve verifies the live settings-edit flow end to
// end: an edit to the master's on-disk settings tree is published by the
// lease holder's file watcher (interval as backstop), and peels pick it up
// via the revision watch + debounced re-resolve. This is THE operator flow —
// out-of-band KV writes are no longer meaningful input (the master actively
// heals them back to disk truth; see TestSettingsOutOfBandKVTamperHealed).
func TestSettingsWatchReResolve(t *testing.T) {
	// 1. Verify initial state: timezone should be "UTC".
	waitForSetting(t, "timezone", "UTC", 30*time.Second)

	// 2. Edit the settings file on the master's disk — the shared
	// settings-data volume is visible to whichever master holds the lease.
	execInContainer(t, "master", []string{"sed", "-i",
		"s|timezone: UTC|timezone: America/New_York|", "/data/settings/common/base.zy"})
	t.Cleanup(func() {
		execInContainer(t, "master", []string{"sed", "-i",
			"s|timezone: America/New_York|timezone: UTC|", "/data/settings/common/base.zy"})
		waitForSetting(t, "timezone", "UTC", 60*time.Second)
	})

	// 3. Watcher publish (~1s; 30s interval backstop) + peel re-resolve
	// (debounce 2s + jitter up to 5s).
	waitForSetting(t, "timezone", "America/New_York", 60*time.Second)
}

// TestSettingsOutOfBandKVTamperHealed replaces the old KV-direct revision
// test, whose premise is obsolete: the master's disk tree is now the source
// of truth, and the lease holder's republish interval actively HEALS
// out-of-band writes to the settings-files bucket. A tampered file — even a
// full fake publish with a patched manifest and a revision bump — must be
// reverted to disk truth within a republish tick, and peels must converge
// back. (The peel-side revision/manifest verification the old test covered
// remains pinned by unit tests: resolve_manifest_test, cache_manifest_test.)
func TestSettingsOutOfBandKVTamperHealed(t *testing.T) {
	// 1. Verify initial state: timezone should be "UTC".
	waitForSetting(t, "timezone", "UTC", 30*time.Second)

	// 2. Tamper with the bucket directly, mimicking a complete publish:
	// file key + patched manifest + revision bump.
	nc := connectNATS(t)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("create jetstream context: %v", err)
	}
	ctx := context.Background()
	kv, err := bus.NewJS(js).KeyValue(ctx, "settings-files")
	if err != nil {
		t.Fatalf("get settings-files KV: %v", err)
	}
	entry, err := kv.Get(ctx, "common/base.zy")
	if err != nil {
		t.Fatalf("get common/base.zy: %v", err)
	}
	original := string(entry.Value())
	modified := strings.ReplaceAll(original, "timezone: UTC", "timezone: America/Chicago")
	if modified == original {
		t.Fatal("replacement had no effect — timezone: UTC not found in common/base.zy")
	}
	if _, err := kv.Put(ctx, "common/base.zy", []byte(modified)); err != nil {
		t.Fatalf("put tampered common/base.zy: %v", err)
	}
	if err := patchSettingsManifest(ctx, kv, "common/base.zy", []byte(modified)); err != nil {
		t.Fatalf("patch settings manifest: %v", err)
	}
	if _, err := kv.Put(ctx, "_revision", []byte("31337")); err != nil {
		t.Fatalf("bump _revision: %v", err)
	}

	// 3. The lease holder's hash-gated republish detects the manifest drift
	// from its on-disk tree within one interval (30s) and republishes disk
	// truth. Poll the BUCKET content for the heal — the peel-visible value
	// is UTC almost the whole time (the tamper is only transiently visible),
	// so it cannot signal when the heal landed.
	deadline := time.Now().Add(120 * time.Second)
	healed := false
	for time.Now().Before(deadline) {
		entry, err := kv.Get(ctx, "common/base.zy")
		if err == nil && string(entry.Value()) == original {
			healed = true
			break
		}
		time.Sleep(3 * time.Second)
	}
	if !healed {
		t.Fatal("tampered settings file key was not healed back to the on-disk content within 120s")
	}

	// 4. And the fleet-visible value has converged back (covers the case
	// where the peel transiently resolved the tampered content).
	waitForSetting(t, "timezone", "UTC", 60*time.Second)
}

// TestEncryptedSettingsCrossReference verifies that encrypted values can be
// referenced in other settings files via {{ settings.X }} and that the
// cross-file reference resolves to the decrypted value, not the placeholder.
func TestEncryptedSettingsCrossReference(t *testing.T) {
	// 1. Verify api_secret is decrypted correctly.
	results := execCLI(t, "web-01", "settings.get", "api_secret")
	r := requireSuccess(t, results, "web-01")

	if len(r.Results) == 0 {
		t.Fatal("no results returned for api_secret")
	}
	if got := r.Results[0].Details["result"]; got != "zester-test-secret" {
		t.Errorf("api_secret = %q, want zester-test-secret", got)
	}

	// 2. Verify cross-file reference resolved to decrypted value.
	results2 := execCLI(t, "web-01", "settings.get", "api_header")
	r2 := requireSuccess(t, results2, "web-01")

	if len(r2.Results) == 0 {
		t.Fatal("no results returned for api_header")
	}
	if got := r2.Results[0].Details["result"]; got != "Bearer zester-test-secret" {
		t.Errorf("api_header = %q, want \"Bearer zester-test-secret\"", got)
	}
}
