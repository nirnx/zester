package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/update"
)

var updateAbortCmd = &cobra.Command{
	Use:   "abort <rollout-id>",
	Short: "Abort an in-progress rollout",
	Args:  cobra.ExactArgs(1),
	RunE:  runUpdateAbort,
}

func runUpdateAbort(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rolloutID := args[0]

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(ctx)

	req := update.RolloutAbortRequest{RolloutID: rolloutID}
	var resp update.RolloutAbortResponse
	if err := client.Request(ctx, bus.SubjectUpdateRolloutAbort, &req, &resp); err != nil {
		return fmt.Errorf("abort request: %w", err)
	}
	if resp.Error != "" {
		return fmt.Errorf("abort: %s", resp.Error)
	}

	fmt.Printf("Rollout %s aborted. Already-updated nodes are NOT rolled back.\n", rolloutID)
	return nil
}
