package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/job"
)

var jobCmd = &cobra.Command{
	Use:   "job",
	Short: "Manage jobs",
}

var jobListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent jobs",
	RunE:  runJobList,
}

var jobShowCmd = &cobra.Command{
	Use:   "show <jid>",
	Short: "Show details for a job",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobShow,
}

var jobActiveCmd = &cobra.Command{
	Use:   "active",
	Short: "List currently running jobs",
	RunE:  runJobActive,
}

var jobKillCmd = &cobra.Command{
	Use:   "kill <jid>",
	Short: "Cancel a running job",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobKill,
}

func init() {
	jobCmd.AddCommand(jobListCmd)
	jobCmd.AddCommand(jobShowCmd)
	jobCmd.AddCommand(jobActiveCmd)
	jobCmd.AddCommand(jobKillCmd)
}

func runJobList(cmd *cobra.Command, args []string) error {
	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	kv, err := bus.GetBucket(ctx, client.JetStream(), bus.BucketJobs)
	if err != nil {
		return fmt.Errorf("get jobs bucket: %w", err)
	}

	keys, err := listKVKeys(ctx, kv)
	if err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}
	// The jobs bucket also holds "active.<jid>" index keys (orphan-scanner
	// index, pkg/job/active.go); those are not job records.
	keys = filterJobRecordKeys(keys)

	if len(keys) == 0 {
		fmt.Println("No jobs found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "JID\tFUNCTION\tTARGET\tSTATE\tUSER\tOWNER")
	for _, key := range keys {
		var job map[string]any
		if err := bus.KVGet(ctx, kv, key, &job); err != nil {
			continue
		}
		fun := mapStr(job, "function")
		tgt := mapTargets(job, "targets")
		state := mapStr(job, "status")
		usr := mapStr(job, "user")
		owner := mapStr(job, "owner")
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", key, fun, tgt, state, usr, owner)
	}
	w.Flush()
	return nil
}

func runJobShow(cmd *cobra.Command, args []string) error {
	jid := args[0]

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	kv, err := bus.GetBucket(ctx, client.JetStream(), bus.BucketJobs)
	if err != nil {
		return fmt.Errorf("get jobs bucket: %w", err)
	}

	var job map[string]any
	if err := bus.KVGet(ctx, kv, jid, &job); err != nil {
		return fmt.Errorf("get job %s: %w", jid, err)
	}

	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))

	// Show returns if available.
	retKV, err := bus.GetBucket(ctx, client.JetStream(), bus.BucketJobReturns)
	if err != nil {
		return nil // returns bucket may not exist yet
	}

	returns := fetchJobReturns(ctx, retKV, jid)
	if len(returns) == 0 {
		return nil // no returns yet
	}

	fmt.Println("\nReturns:")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PEEL\tSTATUS\tSUCCESS\tDURATION")
	for _, ret := range returns {
		peelID := mapStr(ret, "peel_id")
		success := mapStr(ret, "success")
		dur := mapStr(ret, "duration")
		// Synthetic unreachable returns (master fast path) are called out
		// explicitly — a delivery failure, not an execution failure.
		status := "failed"
		if unreach, _ := ret["unreachable"].(bool); unreach {
			status = "unreachable"
		} else if success == "true" {
			status = "success"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", displayPeel(peelID), status, success, dur)
	}
	w.Flush()
	return nil
}

func runJobActive(cmd *cobra.Command, args []string) error {
	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	kv, err := bus.GetBucket(ctx, client.JetStream(), bus.BucketJobs)
	if err != nil {
		return fmt.Errorf("get jobs bucket: %w", err)
	}

	// Enumerate only the "active.<jid>" index keys (written at claim time,
	// deleted on terminal transitions — pkg/job/active.go) instead of
	// scanning the whole 7-day jobs bucket.
	activeKeys, err := bus.ListKeysWithPrefix(ctx, kv, job.ActiveJobKeyPrefix+bus.WildcardMany)
	if err != nil {
		return fmt.Errorf("list active jobs: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "JID\tFUNCTION\tTARGETS\tSTATUS\tUSER\tOWNER")
	found := false
	for _, activeKey := range activeKeys {
		jid := strings.TrimPrefix(activeKey, job.ActiveJobKeyPrefix)
		var rec map[string]any
		if err := bus.KVGet(ctx, kv, jid, &rec); err != nil {
			continue // stale index entry; the orphan scanner self-heals it
		}
		status := mapStr(rec, "status")
		if !jobStatusActive(status) {
			continue // index entry outlived a terminal transition
		}
		found = true
		fun := mapStr(rec, "function")
		tgt := mapTargets(rec, "targets")
		usr := mapStr(rec, "user")
		owner := mapStr(rec, "owner")
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", jid, fun, tgt, status, usr, owner)
	}
	w.Flush()
	if !found {
		fmt.Println("No active jobs.")
	}
	return nil
}

// fetchJobReturns reads a job's returns from the job-returns bucket.
// Returns are stored exclusively under per-peel keys ("<jid>.<peelID>"),
// written by the master's watcher and by the scheduled-result consumer
// (mirroring job.Manager.GetReturns); an empty prefix result means the job
// has no returns. Keys are listed sorted, so output ordering is
// deterministic by peel ID.
func fetchJobReturns(ctx context.Context, retKV bus.KV, jid string) []map[string]any {
	var returns []map[string]any
	if keys, err := bus.ListKeysWithPrefix(ctx, retKV, jid); err == nil {
		for _, key := range keys {
			var ret map[string]any
			if err := bus.KVGet(ctx, retKV, key, &ret); err != nil {
				continue
			}
			returns = append(returns, ret)
		}
	}
	return returns
}

// filterJobRecordKeys drops active-index keys ("active.<jid>",
// job.ActiveJobKeyPrefix) from a jobs-bucket key listing, leaving only real
// job record keys.
func filterJobRecordKeys(keys []string) []string {
	out := keys[:0]
	for _, k := range keys {
		if strings.HasPrefix(k, job.ActiveJobKeyPrefix) {
			continue
		}
		out = append(out, k)
	}
	return out
}

// jobStatusActive reports whether a job status string is non-terminal.
func jobStatusActive(status string) bool {
	switch status {
	case "running", "pending", "claimed":
		return true
	}
	return false
}

func runJobKill(cmd *cobra.Command, args []string) error {
	jid := args[0]

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(context.Background())

	cancelSubject := bus.JobCancelSubject(jid)
	if err := client.Publish(cancelSubject, map[string]string{"action": "cancel"}); err != nil {
		return fmt.Errorf("publish cancel for job %s: %w", jid, err)
	}

	fmt.Printf("Cancel signal sent for job %s\n", jid)
	return nil
}

func mapStr(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return "-"
}

// mapTargets renders a decoded job record's target list with peel ids in
// display form (interior '_' -> '.'), keeping the same "[a b]" shape %v would
// print. Falls back to mapStr for missing or unexpected shapes.
func mapTargets(m map[string]any, key string) string {
	switch list := m[key].(type) {
	case []any:
		parts := make([]string, len(list))
		for i, e := range list {
			if s, ok := e.(string); ok {
				parts[i] = displayPeel(s)
			} else {
				parts[i] = fmt.Sprintf("%v", e)
			}
		}
		return "[" + strings.Join(parts, " ") + "]"
	case []string:
		return "[" + strings.Join(displayPeels(list), " ") + "]"
	}
	return mapStr(m, key)
}
