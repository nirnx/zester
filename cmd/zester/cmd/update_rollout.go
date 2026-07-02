package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/update"
)

var updateRolloutCmd = &cobra.Command{
	Use:   "rollout",
	Short: "Start a new rollout",
	RunE:  runUpdateRollout,
}

func init() {
	updateRolloutCmd.Flags().String("component", "", "Component type: peel or master (required)")
	updateRolloutCmd.Flags().String("version", "", "Target version (required)")
	updateRolloutCmd.Flags().String("target", "*", "Target expression")
	updateRolloutCmd.Flags().Int("batch-size", 1, "Nodes per batch")
	updateRolloutCmd.Flags().Duration("soak-time", 60*time.Second, "Per-node soak period")
	updateRolloutCmd.Flags().Duration("batch-pause", 30*time.Second, "Pause between batches")
	updateRolloutCmd.Flags().Int("max-failed", 1, "Abort after N failures")
	updateRolloutCmd.Flags().Bool("dry-run", false, "Preview without executing")
	updateRolloutCmd.MarkFlagRequired("component")
	updateRolloutCmd.MarkFlagRequired("version")
}

func runUpdateRollout(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	component, _ := cmd.Flags().GetString("component")
	if component != "peel" && component != "master" {
		return fmt.Errorf("component must be 'peel' or 'master', got %q", component)
	}
	version, _ := cmd.Flags().GetString("version")
	target, _ := cmd.Flags().GetString("target")
	batchSize, _ := cmd.Flags().GetInt("batch-size")
	soakTime, _ := cmd.Flags().GetDuration("soak-time")
	batchPause, _ := cmd.Flags().GetDuration("batch-pause")
	maxFailed, _ := cmd.Flags().GetInt("max-failed")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(ctx)

	req := update.RolloutStartRequest{
		Config: update.RolloutConfig{
			Version:    version,
			Component:  component,
			Target:     target,
			BatchSize:  batchSize,
			SoakTime:   soakTime,
			BatchPause: batchPause,
			MaxFailed:  maxFailed,
			DryRun:     dryRun,
		},
	}

	var resp update.RolloutStartResponse
	if err := client.Request(ctx, bus.SubjectUpdateRolloutStart, &req, &resp); err != nil {
		return fmt.Errorf("rollout request: %w", err)
	}
	if resp.Error != "" {
		return fmt.Errorf("rollout: %s", resp.Error)
	}

	if dryRun {
		fmt.Printf("Dry run: would roll out %s %s to %d nodes in %d batches\n",
			component, version, resp.Nodes, resp.Batches)
		if resp.State != nil {
			for i, batch := range resp.State.Batches {
				fmt.Printf("  Batch %d: %v\n", i+1, batch)
			}
		}
		return nil
	}

	fmt.Printf("Rollout started: %s\n", resp.ID)
	fmt.Printf("  Component: %s\n", component)
	fmt.Printf("  Version:   %s\n", version)
	fmt.Printf("  Nodes:     %d\n", resp.Nodes)
	fmt.Printf("  Batches:   %d\n", resp.Batches)

	return nil
}
