package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/update"
)

var updateRolloutsCmd = &cobra.Command{
	Use:   "rollouts",
	Short: "List rollouts (newest first)",
	RunE:  runUpdateRollouts,
}

func runUpdateRollouts(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(ctx)

	rolloutKV, err := bus.GetBucket(ctx, client.JetStream(), bus.BucketUpdateRollouts)
	if err != nil {
		return fmt.Errorf("open rollout bucket: %w", err)
	}

	states, err := update.NewRolloutStore(rolloutKV).List(ctx)
	if err != nil {
		return err
	}
	if len(states) == 0 {
		fmt.Println("No rollouts recorded.")
		return nil
	}

	sort.Slice(states, func(i, j int) bool { return states[i].StartedAt.After(states[j].StartedAt) })

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tCOMPONENT\tVERSION\tSTATE\tBATCH\tFAILED\tSTARTED\tFINISHED")
	for _, s := range states {
		finished := "-"
		if !s.FinishedAt.IsZero() {
			finished = s.FinishedAt.Local().Format("01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d/%d\t%d/%d\t%s\t%s\n",
			s.ID, s.Config.Component, s.Config.Version, s.State,
			s.CurrentBatch+1, len(s.Batches),
			s.FailedCount, s.Config.MaxFailed,
			s.StartedAt.Local().Format("01-02 15:04"), finished)
	}
	w.Flush()
	return nil
}
