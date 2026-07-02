package cmd

import "github.com/spf13/cobra"

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Manage binary updates and rollouts",
	Long: `Manage binary updates and rollouts for zester components.

Publish binaries, start rollouts, monitor progress, and manage versions.`,
}

func init() {
	updateCmd.AddCommand(updatePublishCmd)
	updateCmd.AddCommand(updateRolloutCmd)
	updateCmd.AddCommand(updateStatusCmd)
	updateCmd.AddCommand(updateAbortCmd)
	updateCmd.AddCommand(updateRollbackCmd)
	updateCmd.AddCommand(updateVersionsCmd)
}
