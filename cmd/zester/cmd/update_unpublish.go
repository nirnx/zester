package cmd

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/update"
)

var updateUnpublishCmd = &cobra.Command{
	Use:   "unpublish",
	Short: "Remove a published version (binary + manifest)",
	Long: `Unpublish deletes a version's binary from the object store and its
manifest. Refused while a non-terminal rollout references the version (an
in-flight prepare must be able to download it). Node-side rollback is
unaffected: nodes revert from their local previous-binary slot, never by
re-downloading. Promoted versions require an explicit --force.`,
	RunE: runUpdateUnpublish,
}

func init() {
	updateUnpublishCmd.Flags().String("component", "", "Component: peel or master (required)")
	updateUnpublishCmd.Flags().String("version", "", "Version string (required)")
	updateUnpublishCmd.Flags().String("goos", runtime.GOOS, "Target OS")
	updateUnpublishCmd.Flags().String("goarch", runtime.GOARCH, "Target architecture")
	updateUnpublishCmd.Flags().Bool("force", false, "Also remove a PROMOTED version")
	updateUnpublishCmd.MarkFlagRequired("component")
	updateUnpublishCmd.MarkFlagRequired("version")
}

func runUpdateUnpublish(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	component, _ := cmd.Flags().GetString("component")
	version, _ := cmd.Flags().GetString("version")
	goos, _ := cmd.Flags().GetString("goos")
	goarch, _ := cmd.Flags().GetString("goarch")
	force, _ := cmd.Flags().GetBool("force")

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(ctx)
	js := client.JetStream()

	manifestKV, err := bus.GetBucket(ctx, js, bus.BucketUpdateManifests)
	if err != nil {
		return fmt.Errorf("open manifest bucket: %w", err)
	}
	rolloutKV, err := bus.GetBucket(ctx, js, bus.BucketUpdateRollouts)
	if err != nil {
		return fmt.Errorf("open rollout bucket: %w", err)
	}
	objStore, err := js.ObjectStore(ctx, bus.ObjectBucketUpdateBinaries)
	if err != nil {
		return fmt.Errorf("open object store: %w", err)
	}

	mStore := update.NewManifestStore(manifestKV)
	m, err := mStore.Get(ctx, component, goos, goarch, version)
	if err != nil {
		if errors.Is(err, bus.ErrKeyNotFound) {
			return fmt.Errorf("no published manifest for %s %s (%s/%s) — see 'zester update versions'", component, version, goos, goarch)
		}
		return err
	}
	if m.Promoted && !force {
		return fmt.Errorf("%s %s is PROMOTED; demote it first or pass --force", component, version)
	}

	// Never yank a binary out from under an in-flight rollout.
	states, err := update.NewRolloutStore(rolloutKV).List(ctx)
	if err != nil {
		return fmt.Errorf("list rollouts: %w", err)
	}
	for _, s := range states {
		if s.Config.Component == component && s.Config.Version == version && !update.RolloutTerminal(s.State) {
			return fmt.Errorf("rollout %s (%s) is still using %s %s; abort it first", s.ID, s.State, component, version)
		}
	}

	// Object first, manifest last — a crash in between leaves a loud
	// missing-object fetch error, not an invisible orphan.
	binStore := update.NewBinaryStore(objStore)
	if err := binStore.Delete(ctx, m.ObjectKey); err != nil {
		fmt.Printf("warning: binary delete: %v (continuing — the GC's orphan sweep covers leftovers)\n", err)
	}
	if err := mStore.Delete(ctx, component, goos, goarch, version); err != nil {
		return err
	}

	fmt.Printf("Unpublished %s %s (%s/%s)\n", component, version, goos, goarch)
	return nil
}
