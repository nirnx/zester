package cmd

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/update"
)

var updateStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show update status",
	RunE:  runUpdateStatus,
}

func init() {
	updateStatusCmd.Flags().String("rollout", "", "Show specific rollout progress")
	updateStatusCmd.Flags().String("component", "", "Component type: peel or master (required)")
	updateStatusCmd.MarkFlagRequired("component")
}

func runUpdateStatus(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rolloutID, _ := cmd.Flags().GetString("rollout")
	component, _ := cmd.Flags().GetString("component")

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(ctx)

	js := client.JetStream()

	if rolloutID != "" {
		rolloutKV, err := bus.GetBucket(ctx, js, bus.BucketUpdateRollouts)
		if err != nil {
			return fmt.Errorf("open rollout bucket: %w", err)
		}
		rStore := update.NewRolloutStore(rolloutKV)
		state, err := rStore.Get(ctx, rolloutID)
		if err != nil {
			return fmt.Errorf("get rollout: %w", err)
		}

		fmt.Printf("Rollout: %s\n", state.ID)
		fmt.Printf("  State:     %s\n", state.State)
		fmt.Printf("  Component: %s\n", state.Config.Component)
		fmt.Printf("  Version:   %s\n", state.Config.Version)
		fmt.Printf("  Batch:     %d/%d\n", state.CurrentBatch+1, len(state.Batches))
		fmt.Printf("  Failed:    %d/%d\n", state.FailedCount, state.Config.MaxFailed)
		fmt.Printf("  Started:   %s\n", state.StartedAt.Local().Format(time.RFC3339))
		if !state.FinishedAt.IsZero() {
			fmt.Printf("  Finished:  %s\n", state.FinishedAt.Local().Format(time.RFC3339))
		}
		if state.Error != "" {
			fmt.Printf("  Error:     %s\n", state.Error)
		}

		if len(state.NodeResults) > 0 {
			fmt.Println("\nNode Results:")
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NODE\tSTATUS\tERROR\tUPDATED")
			for id, nr := range state.NodeResults {
				errMsg := nr.Error
				if errMsg == "" {
					errMsg = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", id, nr.Status, errMsg, nr.Updated.Local().Format("15:04:05"))
			}
			w.Flush()
		}
		return nil
	}

	statusKV, err := bus.GetBucket(ctx, js, bus.BucketUpdateStatus)
	if err != nil {
		return fmt.Errorf("open status bucket: %w", err)
	}

	statuses, err := update.ListNodeStatuses(ctx, statusKV, component)
	if err != nil {
		return fmt.Errorf("list statuses: %w", err)
	}

	if len(statuses) == 0 {
		fmt.Printf("No %s nodes reporting status.\n", component)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tVERSION\tSTATE\tPID\tUPTIME\tUPDATED\tDEGRADED\tPROTO")
	for _, s := range statuses {
		updated := s.UpdatedAt.Local().Format("15:04:05")
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			s.ID, s.Version, s.State, s.ChildPID, s.ChildUptime, updated,
			yesNo(s.Degraded), protocolCol(s.Protocol))
	}
	w.Flush()

	return nil
}

// yesNo renders a boolean table cell.
func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// protocolCol renders a NodeStatus.Protocol table cell; 0 means the node's
// watchdog predates protocol reporting (legacy), shown as "-".
func protocolCol(p int) string {
	if p <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", p)
}
