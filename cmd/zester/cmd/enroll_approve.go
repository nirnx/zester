package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ptorbus/zester/pkg/bus"
)

var enrollApproveCmd = &cobra.Command{
	Use:   "approve <enrollment-id>",
	Short: "Approve a pending enrollment",
	Args:  cobra.ExactArgs(1),
	RunE:  runEnrollApprove,
}

func runEnrollApprove(cmd *cobra.Command, args []string) error {
	rec, err := runEnrollAdmin(cmd, bus.SubjectAdminEnrollApprove, "approve", args[0], "")
	if err != nil {
		return err
	}

	fmt.Printf("Enrollment %s approved (peel: %s)\n", rec.ID, rec.PeelID)
	return nil
}

// currentUsername returns the current OS username for audit trails.
// It delegates to the shared currentOperator helper (see identity.go).
func currentUsername() string {
	return currentOperator()
}
