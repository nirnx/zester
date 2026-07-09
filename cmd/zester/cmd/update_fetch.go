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

// `zester update fetch` downloads a published binary from the update-binaries
// object store and (optionally) writes it to disk, verifying its SHA-256. It
// is the standalone, non-mutating counterpart to a rollout's internal download
// step — useful to verify a published binary is retrievable, to pre-stage one,
// and (with `--creds <peel.creds>`) to check that PEEL credentials can complete
// the flow-controlled object-store download that self-update depends on.
var updateFetchCmd = &cobra.Command{
	Use:   "fetch",
	Short: "Download a published update binary from the object store",
	RunE:  runUpdateFetch,
}

func init() {
	updateFetchCmd.Flags().String("component", "", "Component type: peel or master (with --version; resolves the object key via the manifest)")
	updateFetchCmd.Flags().String("version", "", "Version string, e.g., v0.5.0 (with --component)")
	updateFetchCmd.Flags().String("goos", runtime.GOOS, "Target OS")
	updateFetchCmd.Flags().String("goarch", runtime.GOARCH, "Target architecture")
	updateFetchCmd.Flags().String("object-key", "", "Download this object key directly, skipping the manifest lookup (needs --sha256)")
	updateFetchCmd.Flags().String("sha256", "", "Expected SHA-256 of the object (required with --object-key)")
	updateFetchCmd.Flags().String("out", "", "Write the downloaded binary here (default: verify only, discard)")
}

func runUpdateFetch(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	component, _ := cmd.Flags().GetString("component")
	version, _ := cmd.Flags().GetString("version")
	goos, _ := cmd.Flags().GetString("goos")
	goarch, _ := cmd.Flags().GetString("goarch")
	objectKey, _ := cmd.Flags().GetString("object-key")
	sha, _ := cmd.Flags().GetString("sha256")
	out, _ := cmd.Flags().GetString("out")

	// Two modes: resolve the object key from the manifest (operator
	// convenience; needs the manifest-bucket handle), or download an
	// explicit object key + SHA directly (the watchdog's own path — it gets
	// key and hash from the master's update command and never reads the
	// manifest bucket, so this mode works under least-privilege peel creds).
	if objectKey == "" {
		if component != "peel" && component != "master" {
			return fmt.Errorf("provide --object-key + --sha256, or --component (peel|master) + --version")
		}
		if version == "" {
			return fmt.Errorf("--version is required with --component")
		}
	} else if sha == "" {
		return fmt.Errorf("--sha256 is required with --object-key")
	}

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(ctx)

	js := client.JetStream()

	if objectKey == "" {
		// The manifest bucket is admin/master-scoped; peel creds cannot open
		// it and the request just hangs (no responder) until the deadline.
		// Bound it tightly and point the operator at the direct path — which
		// is exactly what the watchdog uses (it gets the key + SHA from the
		// master's command and never touches the manifest bucket).
		mctx, mcancel := context.WithTimeout(ctx, 10*time.Second)
		defer mcancel()
		manifestKV, err := bus.GetBucket(mctx, js, bus.BucketUpdateManifests)
		if err != nil {
			return fmt.Errorf("open manifest bucket (credentials may lack manifest access — use --object-key <key> --sha256 <hash> to download directly, as the watchdog does): %w", err)
		}
		manifest, err := update.NewManifestStore(manifestKV).Get(mctx, component, goos, goarch, version)
		if err != nil {
			return fmt.Errorf("no published %s %s for %s/%s: %w", component, version, goos, goarch, err)
		}
		objectKey, sha = manifest.ObjectKey, manifest.SHA256
	}

	objStore, err := js.ObjectStore(ctx, bus.ObjectBucketUpdateBinaries)
	if err != nil {
		return fmt.Errorf("open object store: %w", err)
	}

	// The download uses a flow-controlled ordered consumer; verification is
	// against the expected SHA-256.
	data, err := update.NewBinaryStore(objStore).Download(ctx, objectKey, sha)
	if err != nil {
		return fmt.Errorf("download %s: %w", objectKey, err)
	}

	if out != "" {
		if err := os.WriteFile(out, data, 0o755); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}
	}

	fmt.Printf("Fetched %s (verified)\n", objectKey)
	fmt.Printf("  SHA-256: %s\n", sha)
	fmt.Printf("  Size:    %d bytes\n", len(data))
	if out != "" {
		fmt.Printf("  Written: %s\n", out)
	}
	return nil
}
