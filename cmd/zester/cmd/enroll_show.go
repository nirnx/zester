package cmd

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/enroll"
)

var enrollShowCmd = &cobra.Command{
	Use:   "show <enrollment-id>",
	Short: "Show enrollment details",
	Args:  cobra.ExactArgs(1),
	RunE:  runEnrollShow,
}

func runEnrollShow(cmd *cobra.Command, args []string) error {
	enrollmentID := args[0]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, client, err := enrollStore(ctx)
	if err != nil {
		return err
	}
	defer client.Shutdown(ctx)

	rec, err := store.Get(ctx, enrollmentID)
	if err != nil {
		return fmt.Errorf("get enrollment %s: %w", enrollmentID, err)
	}

	printEnrollRecord(os.Stdout, rec)
	return nil
}

// printEnrollRecord renders one enrollment record. Every free-text field that
// originates outside the CLI — the requester-supplied hostname, metadata and
// trusted-CA pin, the operator-typed reason/identity, and the peer address —
// goes through displayString so nothing in the record can inject terminal
// escapes or fake additional lines.
func printEnrollRecord(out io.Writer, rec *enroll.Record) {
	fmt.Fprintf(out, "Enrollment ID:  %s\n", rec.ID)
	fmt.Fprintf(out, "Peel ID:        %s\n", displayPeel(rec.PeelID))
	fmt.Fprintf(out, "State:          %s\n", rec.State)
	fmt.Fprintf(out, "Public Key:     %s\n", rec.PublicKey)
	fmt.Fprintf(out, "Hostname:       %s\n", displayString(rec.Hostname))
	if len(rec.Metadata) > 0 {
		fmt.Fprintln(out, "Metadata:")
		for _, k := range slices.Sorted(maps.Keys(rec.Metadata)) {
			fmt.Fprintf(out, "    %s: %s\n", displayString(k), displayString(rec.Metadata[k]))
		}
	}
	reportedCA := displayString(rec.TrustedCASPKI)
	if rec.TrustMismatch {
		fmt.Fprintf(out, "Trust:          MISMATCH! peel reported CA %s (not this master's root)\n", reportedCA)
		fmt.Fprintf(out, "                → possible first-contact MITM; verify the node out of band before 'enroll approve --force'\n")
	} else if rec.TrustChecked {
		fmt.Fprintf(out, "Trust:          ok (peel-reported CA matches master root: %s)\n", reportedCA)
	} else if rec.TrustedCASPKI != "" {
		fmt.Fprintf(out, "Trust:          present (unverified — external-CA master has no root to compare): %s\n", reportedCA)
	}
	fmt.Fprintf(out, "Created At:     %s\n", rec.CreatedAt.Local().Format(time.RFC3339))
	fmt.Fprintf(out, "Updated At:     %s\n", rec.UpdatedAt.Local().Format(time.RFC3339))
	if rec.DecidedBy != "" {
		fmt.Fprintf(out, "Decided By:     %s\n", displayString(rec.DecidedBy))
	}
	if rec.DecidedAt != nil {
		fmt.Fprintf(out, "Decided At:     %s\n", rec.DecidedAt.Local().Format(time.RFC3339))
	}
	if rec.RejectReason != "" {
		fmt.Fprintf(out, "Reject Reason:  %s\n", displayString(rec.RejectReason))
	}
	if rec.IssuedAt != nil {
		fmt.Fprintf(out, "Issued At:      %s\n", rec.IssuedAt.Local().Format(time.RFC3339))
	}
	if rec.ExpiresAt != nil {
		fmt.Fprintf(out, "Expires At:     %s\n", rec.ExpiresAt.Local().Format(time.RFC3339))
	}
	if rec.RemoteAddr != "" {
		fmt.Fprintf(out, "Remote Addr:    %s\n", displayString(rec.RemoteAddr))
	}
}
