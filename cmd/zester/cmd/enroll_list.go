package cmd

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/enroll"
)

var enrollListCmd = &cobra.Command{
	Use:   "list",
	Short: "List enrollments",
	Long:  `List enrollment requests. By default shows only pending enrollments.`,
	RunE:  runEnrollList,
}

func init() {
	enrollListCmd.Flags().StringP("state", "s", "pending", "Filter by state (pending, approved, rejected, issued, active, revoked, all)")
}

func runEnrollList(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, client, err := enrollStore(ctx)
	if err != nil {
		return err
	}
	defer client.Shutdown(ctx)

	stateFilter, _ := cmd.Flags().GetString("state")

	var filterPtr *enroll.State
	if stateFilter != "all" {
		st, ok := enroll.ParseState(stateFilter)
		if !ok {
			return fmt.Errorf("unknown state %q; valid states: pending, approved, rejected, issued, active, revoked, all", stateFilter)
		}
		filterPtr = &st
	}

	records, err := store.List(ctx, filterPtr)
	if err != nil {
		return fmt.Errorf("list enrollments: %w", err)
	}

	if len(records) == 0 {
		fmt.Println("No enrollments found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPEEL ID\tHOSTNAME\tSTATE\tTRUST\tCREATED")
	for _, rec := range records {
		created := rec.CreatedAt.Local().Format("2006-01-02 15:04:05")
		hostname := rec.Hostname
		if hostname == "" {
			hostname = "-"
		}
		trust := "-"
		if rec.TrustMismatch {
			trust = "MISMATCH!"
		} else if rec.TrustChecked {
			trust = "ok"
		} else if rec.TrustedCASPKI != "" {
			trust = "unverified"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", rec.ID, rec.PeelID, hostname, rec.State, trust, created)
	}
	w.Flush()

	return nil
}
