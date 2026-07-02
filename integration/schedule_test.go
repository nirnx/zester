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

// TestSchedule_ReturnJob verifies that the peel-side scheduler fires a module
// at a configured interval with return_job: true, and that the job actually
// appears in "zester job list" with the schedule metadata.
func TestSchedule_ReturnJob(t *testing.T) {
	const peel = "web-01"

	// Write peel.yaml with a short-interval schedule entry that creates real jobs.
	// The peel loads /etc/zester/peel.yaml by default when --config is not set.
	// CLI flags (--id, --master-url, etc.) override the file fields, but
	// Schedule has no CLI flag so it only comes from the file.
	peelYAML := `schedule:
  sched_test:
    module: cmd.run
    args:
      command: "echo scheduled-ok"
    interval: 5s
    run_on_start: true
    return_job: true
`
	ctx := context.Background()
	container, err := stack.ServiceContainer(ctx, peel)
	if err != nil {
		t.Fatalf("get container %s: %v", peel, err)
	}

	// Write config file into the peel container.
	exitCode, output, err := containerExecRaw(ctx, container, []string{
		"sh", "-c", "cat > /etc/zester/peel.yaml << 'EOCFG'\n" + peelYAML + "EOCFG",
	})
	if err != nil || exitCode != 0 {
		t.Fatalf("write peel.yaml: exit=%d err=%v output=%s", exitCode, err, output)
	}

	// Register cleanup: remove config and restart to restore original state.
	t.Cleanup(func() {
		cctx := context.Background()
		c, err := stack.ServiceContainer(cctx, peel)
		if err != nil {
			return
		}
		//nolint:errcheck
		containerExecRaw(cctx, c, []string{"rm", "-f", "/etc/zester/peel.yaml"})
		//nolint:errcheck
		containerExecRaw(cctx, c, []string{"systemctl", "restart", "zester-peel"})
		// Give peel time to reconnect to NATS.
		time.Sleep(15 * time.Second)
	})

	// Restart zester-peel so it picks up the new config.
	exitCode, output, err = containerExecRaw(ctx, container, []string{
		"systemctl", "restart", "zester-peel",
	})
	if err != nil || exitCode != 0 {
		t.Fatalf("restart zester-peel: exit=%d err=%v output=%s", exitCode, err, output)
	}

	// Wait for the scheduler to fire and create a job.
	// The schedule has run_on_start: true so it fires immediately after startup.
	// We poll "zester job list" looking for a job with "cmd.run" function
	// that has a single target of our peel and "complete" status.
	//
	// Before the scheduler starts, snapshot existing JIDs so we can detect
	// newly created scheduled jobs even if prior tests left cmd.run rows.
	baselineList := getJobList(t)
	baselineJobs := countJobListLinesFromOutput(baselineList)
	baselineJIDs := parseJobListJIDs(baselineList)

	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)

		// Check peel logs for scheduler activity. The peel publishes the
		// result on the schedule subject; the master persists the job.
		logs := getPeelLogs(t, peel)
		if !strings.Contains(logs, "schedule: result published") {
			continue
		}
		t.Log("scheduler published result (confirmed via peel logs)")

		// Verify a new scheduled job appears in "zester job list".
		jobListOutput := getJobList(t)
		currentJobs := countJobListLinesFromOutput(jobListOutput)
		if currentJobs > baselineJobs {
			t.Logf("new jobs appeared in job list: %d -> %d", baselineJobs, currentJobs)

			for _, jid := range findNewScheduledJobJIDs(jobListOutput, peel, baselineJIDs) {
				t.Logf("scheduled cmd.run job found in job list: jid=%s", jid)
				jobShow := getJobShow(t, jid)
				if strings.Contains(jobShow, `"source": "schedule"`) &&
					strings.Contains(jobShow, `"schedule": "sched_test"`) &&
					strings.Contains(jobShow, "Returns:") {
					t.Log("scheduled job show includes schedule metadata and returns")
					return
				}
			}
		}

		// Give one more cycle for eventual consistency.
		time.Sleep(3 * time.Second)
		jobListOutput = getJobList(t)
		for _, jid := range findNewScheduledJobJIDs(jobListOutput, peel, baselineJIDs) {
			jobShow := getJobShow(t, jid)
			if strings.Contains(jobShow, `"source": "schedule"`) &&
				strings.Contains(jobShow, `"schedule": "sched_test"`) &&
				strings.Contains(jobShow, "Returns:") {
				t.Log("scheduled job confirmed in job show with returns")
				return
			}
		}
	}

	// Dump logs for debugging.
	logs := getPeelLogs(t, peel)
	t.Fatalf("timed out waiting for scheduled job.\nPeel logs:\n%s", truncateLogs(logs, 3000))
}

// countJobListLines returns the number of data rows in "zester job list" output.
func countJobListLines(t *testing.T) int {
	t.Helper()
	output := getJobList(t)
	return countJobListLinesFromOutput(output)
}

func countJobListLinesFromOutput(output string) int {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) <= 1 {
		return 0 // header only or "No jobs found."
	}
	return len(lines) - 1 // subtract header
}

// getJobList runs "zester job list" in the admin container and returns the output.
func getJobList(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	adminContainer, err := stack.ServiceContainer(ctx, "admin")
	if err != nil {
		t.Logf("getJobList: get admin container: %v", err)
		return ""
	}
	_, reader, err := adminContainer.Exec(ctx,
		[]string{"zester", "job", "list"},
		tcexec.Multiplexed(),
	)
	if err != nil {
		t.Logf("getJobList: exec: %v", err)
		return ""
	}
	data, _ := io.ReadAll(reader)
	return string(data)
}

// getJobShow runs "zester job show <jid>" in the admin container and returns output.
func getJobShow(t *testing.T, jid string) string {
	t.Helper()
	ctx := context.Background()
	adminContainer, err := stack.ServiceContainer(ctx, "admin")
	if err != nil {
		t.Logf("getJobShow: get admin container: %v", err)
		return ""
	}
	_, reader, err := adminContainer.Exec(ctx,
		[]string{"zester", "job", "show", jid},
		tcexec.Multiplexed(),
	)
	if err != nil {
		t.Logf("getJobShow: exec: %v", err)
		return ""
	}
	data, _ := io.ReadAll(reader)
	return string(data)
}

// parseJobListJIDs extracts all JIDs from "zester job list" output.
func parseJobListJIDs(jobListOutput string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, line := range strings.Split(strings.TrimSpace(jobListOutput), "\n") {
		if strings.HasPrefix(line, "JID") || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			out[fields[0]] = struct{}{}
		}
	}
	return out
}

// findNewScheduledJobJIDs returns scheduled cmd.run JIDs not present in baseline.
func findNewScheduledJobJIDs(jobListOutput, peel string, baseline map[string]struct{}) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(jobListOutput), "\n") {
		if strings.HasPrefix(line, "JID") || strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(line, "cmd.run") || !strings.Contains(line, peel) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		jid := fields[0]
		if _, seen := baseline[jid]; seen {
			continue
		}
		out = append(out, jid)
	}
	return out
}

// containerExecRaw runs a command in a container and returns exit code + output.
func containerExecRaw(ctx context.Context, container interface {
	Exec(ctx context.Context, cmd []string, options ...tcexec.ProcessOption) (int, io.Reader, error)
}, cmd []string) (int, string, error) {
	exitCode, reader, err := container.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		return -1, "", err
	}
	data, _ := io.ReadAll(reader)
	return exitCode, string(data), nil
}

// getPeelLogs returns logs from a peel container's journalctl for zester-peel.
func getPeelLogs(t *testing.T, service string) string {
	t.Helper()
	ctx := context.Background()

	container, err := stack.ServiceContainer(ctx, service)
	if err != nil {
		t.Fatalf("get container %s: %v", service, err)
	}

	// Use journalctl to get zester-peel logs (systemd-based container).
	exitCode, reader, err := container.Exec(ctx, []string{
		"journalctl", "-u", "zester-peel", "--no-pager", "-n", "200",
	}, tcexec.Multiplexed())
	if err != nil {
		// Fall back to container logs if journalctl isn't available.
		rc, err := container.Logs(ctx)
		if err != nil {
			t.Fatalf("logs %s: %v", service, err)
		}
		defer rc.Close()
		data, _ := io.ReadAll(rc)
		return string(data)
	}

	data, _ := io.ReadAll(reader)
	if exitCode != 0 {
		// Fall back to container logs.
		rc, err := container.Logs(ctx)
		if err != nil {
			return string(data)
		}
		defer rc.Close()
		fallback, _ := io.ReadAll(rc)
		return string(fallback)
	}
	return string(data)
}

// truncateLogs returns at most n bytes from the end of s for log output.
func truncateLogs(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
