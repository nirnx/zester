package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/internal/version"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the Zester CLI version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("zester %s (commit: %s, built: %s)\n",
			version.Version, version.GitCommit, version.BuildDate)
	},
}
