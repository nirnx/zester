package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
)

var enrollRevokeCmd = &cobra.Command{
	Use:   "revoke <enrollment-id>",
	Short: "Revoke an enrollment's credentials",
	Args:  cobra.ExactArgs(1),
	RunE:  runEnrollRevoke,
}

func init() {
	enrollRevokeCmd.Flags().String("reason", "", "Reason for revocation")
}

func runEnrollRevoke(cmd *cobra.Command, args []string) error {
	reason, _ := cmd.Flags().GetString("reason")

	rec, err := runEnrollAdmin(cmd, bus.SubjectAdminEnrollRevoke, "revoke", args[0], reason)
	if err != nil {
		return err
	}

	fmt.Printf("Enrollment %s revoked (peel: %s)\n", rec.ID, rec.PeelID)
	return nil
}
