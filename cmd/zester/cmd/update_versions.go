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

var updateVersionsCmd = &cobra.Command{
	Use:   "versions",
	Short: "List published versions",
	RunE:  runUpdateVersions,
}

func init() {
	updateVersionsCmd.Flags().String("component", "peel", "Component to list versions for")
}

func runUpdateVersions(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	component, _ := cmd.Flags().GetString("component")

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

	mStore := update.NewManifestStore(manifestKV)
	manifests, err := mStore.ListByComponent(ctx, component)
	if err != nil {
		return err
	}

	if len(manifests) == 0 {
		fmt.Printf("No published versions for %s.\n", component)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VERSION\tOS\tARCH\tSIZE\tSHA256\tPUBLISHED")
	for _, m := range manifests {
		published := m.Published.Local().Format("2006-01-02 15:04")
		shortHash := m.SHA256
		if len(shortHash) > 12 {
			shortHash = shortHash[:12]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", m.Version, m.GOOS, m.GOARCH, m.Size, shortHash, published)
	}
	w.Flush()

	return nil
}
