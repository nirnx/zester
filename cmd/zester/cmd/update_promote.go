package cmd

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/update"
)

var updatePromoteCmd = &cobra.Command{
	Use:   "promote",
	Short: "Promote a published version (never expires; auto-rollout target)",
	Long: `Promote marks a published version as a promoted release:
  - it never expires (the master GC skips it), and
  - masters with auto-rollout enabled (the default; see 'zester update auto')
    roll the fleet to the latest promoted version automatically.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPromoteMutation(cmd, func(m *update.Manifest) {
			m.Promoted = true
		}, "promoted")
	},
}

var updateDemoteCmd = &cobra.Command{
	Use:   "demote",
	Short: "Demote a promoted version (expiry resumes)",
	RunE: func(cmd *cobra.Command, args []string) error {
		ttl, _ := cmd.Flags().GetDuration("ttl")
		return runPromoteMutation(cmd, func(m *update.Manifest) {
			m.Promoted = false
			if ttl == 0 {
				m.ExpiresAtUnix = update.TTLNever
			} else {
				m.ExpiresAtUnix = time.Now().Add(ttl).Unix()
			}
		}, "demoted")
	},
}

var updateSetTTLCmd = &cobra.Command{
	Use:   "set-ttl",
	Short: "Set a published version's expiry",
	RunE: func(cmd *cobra.Command, args []string) error {
		ttl, _ := cmd.Flags().GetDuration("ttl")
		return runPromoteMutation(cmd, func(m *update.Manifest) {
			if m.Promoted {
				return // guarded below; Mutate still runs, keep it a no-op
			}
			if ttl == 0 {
				m.ExpiresAtUnix = update.TTLNever
			} else {
				m.ExpiresAtUnix = time.Now().Add(ttl).Unix()
			}
		}, "ttl updated")
	},
}

func init() {
	for _, c := range []*cobra.Command{updatePromoteCmd, updateDemoteCmd, updateSetTTLCmd} {
		c.Flags().String("component", "", "Component: peel or master (required)")
		c.Flags().String("version", "", "Version string (required)")
		c.Flags().String("goos", runtime.GOOS, "Target OS")
		c.Flags().String("goarch", runtime.GOARCH, "Target architecture")
		c.MarkFlagRequired("component")
		c.MarkFlagRequired("version")
	}
	updateDemoteCmd.Flags().Duration("ttl", update.DefaultBinaryTTL, "New lifetime from now (0 = never expires)")
	updateSetTTLCmd.Flags().Duration("ttl", update.DefaultBinaryTTL, "New lifetime from now (0 = never expires)")
	updateSetTTLCmd.MarkFlagRequired("ttl")
}

func runPromoteMutation(cmd *cobra.Command, fn func(*update.Manifest), verb string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	component, _ := cmd.Flags().GetString("component")
	version, _ := cmd.Flags().GetString("version")
	goos, _ := cmd.Flags().GetString("goos")
	goarch, _ := cmd.Flags().GetString("goarch")

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(ctx)

	manifestKV, err := bus.GetBucket(ctx, client.JetStream(), bus.BucketUpdateManifests)
	if err != nil {
		return fmt.Errorf("open manifest bucket: %w", err)
	}
	store := update.NewManifestStore(manifestKV)

	// set-ttl on a promoted version is a footgun: the TTL would silently do
	// nothing (promotion overrides expiry). Demand an explicit demote first.
	if cmd.Name() == "set-ttl" {
		cur, err := store.Get(ctx, component, goos, goarch, version)
		if err != nil {
			return err
		}
		if cur.Promoted {
			return fmt.Errorf("%s %s is promoted and never expires; run 'zester update demote --ttl <d>' to resume expiry", component, version)
		}
	}

	m, err := store.Mutate(ctx, component, goos, goarch, version, fn)
	if err != nil {
		return err
	}

	fmt.Printf("%s %s (%s/%s): %s\n", component, version, goos, goarch, verb)
	if exp, expires := m.Expiry(); expires {
		fmt.Printf("  Expires: %s\n", exp.Local().Format("2006-01-02 15:04"))
	} else {
		fmt.Printf("  Expires: never\n")
	}
	if m.Promoted {
		fmt.Printf("  Auto-rollout: masters with the auto switch on (default) will roll the fleet to this version\n")
	}
	return nil
}
