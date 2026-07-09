package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
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

	fmt.Printf("Enrollment ID:  %s\n", rec.ID)
	fmt.Printf("Peel ID:        %s\n", displayPeel(rec.PeelID))
	fmt.Printf("State:          %s\n", rec.State)
	fmt.Printf("Public Key:     %s\n", rec.PublicKey)
	fmt.Printf("Hostname:       %s\n", rec.Hostname)
	if rec.TrustMismatch {
		fmt.Printf("Trust:          MISMATCH! peel reported CA %s (not this master's root)\n", rec.TrustedCASPKI)
		fmt.Printf("                → possible first-contact MITM; verify the node out of band before 'enroll approve --force'\n")
	} else if rec.TrustChecked {
		fmt.Printf("Trust:          ok (peel-reported CA matches master root: %s)\n", rec.TrustedCASPKI)
	} else if rec.TrustedCASPKI != "" {
		fmt.Printf("Trust:          present (unverified — external-CA master has no root to compare): %s\n", rec.TrustedCASPKI)
	}
	fmt.Printf("Created At:     %s\n", rec.CreatedAt.Local().Format(time.RFC3339))
	fmt.Printf("Updated At:     %s\n", rec.UpdatedAt.Local().Format(time.RFC3339))
	if rec.DecidedBy != "" {
		fmt.Printf("Decided By:     %s\n", rec.DecidedBy)
	}
	if rec.DecidedAt != nil {
		fmt.Printf("Decided At:     %s\n", rec.DecidedAt.Local().Format(time.RFC3339))
	}
	if rec.RejectReason != "" {
		fmt.Printf("Reject Reason:  %s\n", rec.RejectReason)
	}
	if rec.IssuedAt != nil {
		fmt.Printf("Issued At:      %s\n", rec.IssuedAt.Local().Format(time.RFC3339))
	}
	if rec.ExpiresAt != nil {
		fmt.Printf("Expires At:     %s\n", rec.ExpiresAt.Local().Format(time.RFC3339))
	}
	if rec.RemoteAddr != "" {
		fmt.Printf("Remote Addr:    %s\n", rec.RemoteAddr)
	}

	return nil
}
