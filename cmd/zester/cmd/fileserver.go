package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/fileserver"
)

// fileserverUpdateTimeout leaves headroom for three directory walks plus the
// KV puts of a changed tree on the master side (the service's own budget is
// 30s).
const fileserverUpdateTimeout = 35 * time.Second

var fileserverCmd = &cobra.Command{
	Use:   "fileserver",
	Short: "Master file distribution (settings, states, reactor rules)",
}

var fileserverUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Republish the master's on-disk settings/state/reactor files now",
	Long: `Ask the publisher-lease-holding master to re-walk its on-disk settings,
state, and reactor file trees and publish them to KV immediately — the
"I just edited a file, push it now" path (Salt's fileserver.update).

Edits also flow automatically: the lease holder watches the directories
(files_watch) and republishes on an interval (files_republish_interval), so
this command is for skipping even that short wait. Publishes are hash-gated:
an unchanged tree reports changed=false and writes nothing. --force bypasses
the gate and rewrites every key (heals a tampered or torn KV bucket).`,
	RunE: runFileserverUpdate,
}

var fileserverStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show which master holds the publisher lease (where to edit files)",
	Long: `Identify the master currently holding the publisher lease — the ONE box
whose on-disk settings/state/reactor edits are published to the fleet.
Standby masters mirror published truth into their dirs and overwrite local
edits, so always edit on the box this command names.`,
	RunE: runFileserverStatus,
}

func init() {
	rootCmd.AddCommand(fileserverCmd)
	fileserverUpdateCmd.Flags().Bool("force", false,
		"rewrite every file key and bump revisions even when nothing changed")
	fileserverCmd.AddCommand(fileserverUpdateCmd)
	fileserverCmd.AddCommand(fileserverStatusCmd)
}

func runFileserverStatus(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := connectClient()
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
	defer client.Shutdown(context.Background())

	var resp fileserver.StatusResponse
	if err := client.Request(ctx, bus.SubjectAdminFileserverStatus, &fileserver.StatusRequest{}, &resp); err != nil {
		if errors.Is(err, nats.ErrNoResponders) || errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("fileserver status: no publisher-lease holder answered — is a zester-master running?: %w", err)
		}
		return fmt.Errorf("fileserver status: %w", err)
	}

	fmt.Printf("Publisher lease holder:\n")
	fmt.Printf("  Hostname:  %s   <- edit settings/state/reactor files on this box\n", resp.Hostname)
	fmt.Printf("  Master ID: %s\n", resp.Master)
	if resp.SinceUnix > 0 {
		since := time.Unix(resp.SinceUnix, 0).UTC()
		fmt.Printf("  Since:     %s (%s)\n", since.Format(time.RFC3339), time.Since(since).Round(time.Second))
	}
	return nil
}

func runFileserverUpdate(cmd *cobra.Command, args []string) error {
	force, _ := cmd.Flags().GetBool("force")

	ctx, cancel := context.WithTimeout(context.Background(), fileserverUpdateTimeout)
	defer cancel()

	client, err := connectClient()
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
	defer client.Shutdown(context.Background())

	req := fileserver.UpdateRequest{Force: force}
	var resp fileserver.UpdateResponse
	if err := client.Request(ctx, bus.SubjectAdminFileserverUpdate, &req, &resp); err != nil {
		if errors.Is(err, nats.ErrNoResponders) || errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("fileserver update: no publisher-lease holder answered — is a zester-master running?: %w", err)
		}
		return fmt.Errorf("fileserver update: %w", err)
	}

	fmt.Printf("Republish by master %s:\n", resp.Master)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SET\tFILES\tCHANGED\tNOTE")
	for _, s := range resp.Sets {
		note := "-"
		switch {
		case s.Err != "":
			note = "ERROR: " + s.Err
		case s.Skipped != "":
			note = "skipped: " + s.Skipped
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", s.Name, s.Files, yesNo(s.Changed), note)
	}
	w.Flush()

	for _, s := range resp.Sets {
		if s.Err != "" {
			return fmt.Errorf("fileserver update: %s publish failed: %s", s.Name, s.Err)
		}
	}
	return nil
}
