package cmd

import (
	"fmt"
	"os"

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

	resp, err := runEnrollAdminResp(cmd, bus.SubjectAdminEnrollRevoke, "revoke", args[0], reason)
	if err != nil {
		return err
	}
	rec := resp.Record

	fmt.Printf("Enrollment %s revoked (peel: %s)\n", rec.ID, displayPeel(rec.PeelID))
	if resp.Warning != "" {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", resp.Warning)
	} else {
		fmt.Println("NATS credentials revoked: the peel's connection is closed and its JWT is refused on reconnect.")
	}
	return nil
}
