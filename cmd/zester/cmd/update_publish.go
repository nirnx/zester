package cmd

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/update"
)

var updatePublishCmd = &cobra.Command{
	Use:   "publish <binary-path>",
	Short: "Publish a binary to the update store",
	Args:  cobra.ExactArgs(1),
	RunE:  runUpdatePublish,
}

func init() {
	updatePublishCmd.Flags().String("component", "", "Component type: peel or master (required)")
	updatePublishCmd.Flags().String("version", "", "Version string, e.g., v0.5.0 (required)")
	updatePublishCmd.Flags().String("goos", runtime.GOOS, "Target OS")
	updatePublishCmd.Flags().String("goarch", runtime.GOARCH, "Target architecture")
	updatePublishCmd.MarkFlagRequired("component")
	updatePublishCmd.MarkFlagRequired("version")
}

func runUpdatePublish(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	binaryPath := args[0]
	component, _ := cmd.Flags().GetString("component")
	version, _ := cmd.Flags().GetString("version")
	goos, _ := cmd.Flags().GetString("goos")
	goarch, _ := cmd.Flags().GetString("goarch")

	if component != "peel" && component != "master" {
		return fmt.Errorf("component must be 'peel' or 'master', got %q", component)
	}

	data, err := os.ReadFile(binaryPath)
	if err != nil {
		return fmt.Errorf("read binary: %w", err)
	}

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(ctx)

	js := client.JetStream()

	objStore, err := js.ObjectStore(ctx, bus.ObjectBucketUpdateBinaries)
	if err != nil {
		return fmt.Errorf("open object store: %w", err)
	}

	binStore := update.NewBinaryStore(objStore)
	objectKey := update.ObjectKeyFor(component, goos, goarch, version)
	digest, err := binStore.Upload(ctx, objectKey, data)
	if err != nil {
		return err
	}

	manifestKV, err := bus.GetBucket(ctx, js, bus.BucketUpdateManifests)
	if err != nil {
		return fmt.Errorf("open manifest bucket: %w", err)
	}

	manifest := &update.Manifest{
		Version:   version,
		Component: component,
		GOOS:      goos,
		GOARCH:    goarch,
		SHA256:    digest,
		Size:      int64(len(data)),
		ObjectKey: objectKey,
		Published: time.Now(),
		Publisher: "cli",
	}

	mStore := update.NewManifestStore(manifestKV)
	if err := mStore.Publish(ctx, manifest); err != nil {
		return err
	}

	fmt.Printf("Published %s %s (%s/%s)\n", component, version, goos, goarch)
	fmt.Printf("  SHA-256: %s\n", digest)
	fmt.Printf("  Size:    %d bytes\n", len(data))
	fmt.Printf("  Key:     %s\n", objectKey)

	return nil
}
