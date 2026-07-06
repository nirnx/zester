package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/target"
	"github.com/nirnx/zester/pkg/update"
)

var updateRollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "Force rollback nodes to previous binary",
	RunE:  runUpdateRollback,
}

func init() {
	updateRollbackCmd.Flags().String("component", "", "Component type: peel or master (required)")
	updateRollbackCmd.Flags().String("target", "*", "Target expression")
	updateRollbackCmd.MarkFlagRequired("component")
}

func runUpdateRollback(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	component, _ := cmd.Flags().GetString("component")
	targetExpr, _ := cmd.Flags().GetString("target")

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(ctx)

	js := client.JetStream()
	statusKV, err := bus.GetBucket(ctx, js, bus.BucketUpdateStatus)
	if err != nil {
		return fmt.Errorf("open status bucket: %w", err)
	}

	statuses, err := update.ListNodeStatuses(ctx, statusKV, component)
	if err != nil {
		return fmt.Errorf("list statuses: %w", err)
	}

	tt := target.DetectType(targetExpr)
	matcher, err := target.NewMatcher(targetExpr, tt)
	if err != nil {
		return fmt.Errorf("parse target expression: %w", err)
	}

	var nodeIDs []string
	for _, s := range statuses {
		if matcher.Match(s.ID, nil) {
			nodeIDs = append(nodeIDs, s.ID)
		}
	}

	if len(nodeIDs) == 0 {
		return fmt.Errorf("no nodes found matching target %q", targetExpr)
	}

	rollbackCmd := &update.UpdateCommand{Command: update.CmdRollback}
	for _, id := range nodeIDs {
		var resp update.UpdateResponse
		subject := bus.UpdateCmdSubject(id)
		if err := client.Request(ctx, subject, rollbackCmd, &resp); err != nil {
			fmt.Printf("  %s: ERROR: %v\n", id, err)
			continue
		}
		if resp.Error != "" {
			fmt.Printf("  %s: FAILED: %s\n", id, resp.Error)
		} else {
			fmt.Printf("  %s: %s\n", id, resp.Status)
		}
	}

	return nil
}
