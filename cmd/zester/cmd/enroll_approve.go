package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/enroll"
)

var enrollApproveCmd = &cobra.Command{
	Use:   "approve [enrollment-id]",
	Short: "Approve a pending enrollment",
	Long: `Approve a pending enrollment.

Target it by enrollment ID, by peel ID (--peel, so you never have to copy the
enrollment KSUID), or approve every pending record at once (--all-pending).
Exactly one of the three must be given.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runEnrollApprove,
}

func init() {
	enrollApproveCmd.Flags().Bool("force", false,
		"approve a trust-mismatched record (possible first-contact MITM) — use only after verifying the node out of band")
	enrollApproveCmd.Flags().String("peel", "",
		"approve the current enrollment for this peel ID (no need to copy the enrollment KSUID)")
	enrollApproveCmd.Flags().Bool("all-pending", false,
		"approve every pending enrollment")
}

func runEnrollApprove(cmd *cobra.Command, args []string) error {
	peelID, _ := cmd.Flags().GetString("peel")
	allPending, _ := cmd.Flags().GetBool("all-pending")

	ids, err := resolveApproveTargets(args, peelID, allPending)
	if err != nil {
		return err
	}

	var firstErr error
	for _, id := range ids {
		rec, err := runEnrollAdmin(cmd, bus.SubjectAdminEnrollApprove, "approve", id, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "approve %s: %v\n", id, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		fmt.Printf("Enrollment %s approved (peel: %s)\n", rec.ID, displayPeel(rec.PeelID))
	}
	return firstErr
}

// resolveApproveTargets turns the CLI's selector (a positional enrollment ID,
// --peel, or --all-pending — exactly one) into the list of enrollment IDs to
// approve. The --peel and --all-pending forms read the enrollment KV directly
// (the same read path as `enroll list`/`show`) with the operator's own creds.
func resolveApproveTargets(args []string, peelID string, allPending bool) ([]string, error) {
	selectors := 0
	if len(args) == 1 {
		selectors++
	}
	if peelID != "" {
		selectors++
	}
	if allPending {
		selectors++
	}
	switch {
	case selectors == 0:
		return nil, fmt.Errorf("specify an enrollment ID, --peel <peel-id>, or --all-pending")
	case selectors > 1:
		return nil, fmt.Errorf("specify only one of: enrollment ID, --peel, --all-pending")
	}

	if len(args) == 1 {
		return []string{args[0]}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), enrollAdminTimeout)
	defer cancel()
	store, client, err := enrollStore(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Shutdown(ctx)

	if peelID != "" {
		// Accept the natural dotted-hostname form: ids are stored sanitized
		// ('.' -> '_'), and a dotted id can never exist, so normalizing the
		// lookup key only ever finds the record the operator meant.
		lookupID := enroll.SanitizeGlobDots(peelID)
		rec, err := store.FindByPeelID(ctx, lookupID)
		if err != nil {
			return nil, fmt.Errorf("look up peel %q: %w", peelID, err)
		}
		if rec == nil {
			return nil, fmt.Errorf("no enrollment found for peel %q", peelID)
		}
		return []string{rec.ID}, nil
	}

	pending := enroll.StatePending
	recs, err := store.List(ctx, &pending)
	if err != nil {
		return nil, fmt.Errorf("list pending enrollments: %w", err)
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("no pending enrollments to approve")
	}
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// currentUsername returns the current OS username for audit trails.
// It delegates to the shared currentOperator helper (see identity.go).
func currentUsername() string {
	return currentOperator()
}
