package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/update"
)

var updateAutoCmd = &cobra.Command{
	Use:   "auto [on|off|status]",
	Short: "Fleet-wide switch for promoted-version auto-rollouts",
	Long: `Auto-rollout: masters watch for PROMOTED versions and automatically start
a rollout (batched, soaked, auto-rollback) when live nodes lag behind the
latest promoted version. The switch is fleet-wide, stored in NATS, and ON by
default — safe, because nothing rolls until a version is explicitly promoted.
Masters also honor a local update_auto_rollout config knob.`,
	Args:      cobra.MaximumNArgs(1),
	ValidArgs: []string{"on", "off", "status"},
	RunE:      runUpdateAuto,
}

func runUpdateAuto(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	action := "status"
	if len(args) == 1 {
		action = args[0]
	}

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(ctx)

	manifestKV, err := bus.GetBucket(ctx, client.JetStream(), bus.BucketUpdateManifests)
	if err != nil {
		return fmt.Errorf("open manifest bucket: %w", err)
	}

	switch action {
	case "on", "off":
		s := update.AutoSwitch{
			Enabled:       action == "on",
			UpdatedBy:     currentUsername(),
			UpdatedAtUnix: time.Now().Unix(),
		}
		if err := update.SaveAutoSwitch(ctx, manifestKV, s); err != nil {
			return err
		}
		fmt.Printf("Auto-rollout switched %s (fleet-wide; masters pick it up within their check interval)\n", action)
		return nil
	case "status":
		s, err := update.LoadAutoSwitch(ctx, manifestKV)
		if err != nil {
			return err
		}
		state := "OFF"
		if s.Enabled {
			state = "ON"
		}
		fmt.Printf("Auto-rollout: %s", state)
		if s.UpdatedBy != "" {
			fmt.Printf(" (set by %s at %s)", s.UpdatedBy, time.Unix(s.UpdatedAtUnix, 0).Local().Format("2006-01-02 15:04"))
		} else if s.Enabled {
			fmt.Printf(" (default — never explicitly set)")
		}
		fmt.Println()
		return nil
	default:
		return fmt.Errorf("unknown action %q (want on, off, or status)", action)
	}
}
