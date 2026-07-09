package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
)

var enrollRejectCmd = &cobra.Command{
	Use:   "reject <enrollment-id>",
	Short: "Reject a pending enrollment",
	Args:  cobra.ExactArgs(1),
	RunE:  runEnrollReject,
}

func init() {
	enrollRejectCmd.Flags().String("reason", "", "Reason for rejection")
}

func runEnrollReject(cmd *cobra.Command, args []string) error {
	reason, _ := cmd.Flags().GetString("reason")

	rec, err := runEnrollAdmin(cmd, bus.SubjectAdminEnrollReject, "reject", args[0], reason)
	if err != nil {
		return err
	}

	fmt.Printf("Enrollment %s rejected (peel: %s)\n", rec.ID, displayPeel(rec.PeelID))
	return nil
}
