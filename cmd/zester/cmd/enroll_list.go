package cmd

import (
	"context"
	"fmt"
	"io"
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

	printEnrollList(os.Stdout, records)
	return nil
}

// enrollTrustLabel is the TRUST column value for an enrollment record.
func enrollTrustLabel(rec *enroll.Record) string {
	switch {
	case rec.TrustMismatch:
		return "MISMATCH!"
	case rec.TrustChecked:
		return "ok"
	case rec.TrustedCASPKI != "":
		return "unverified"
	default:
		return "-"
	}
}

// dash renders an empty table cell as "-".
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// printEnrollList renders the enrollment table. HOSTNAME is supplied by the
// unauthenticated enrollment request, so it (like every free-text cell) goes
// through displayString — a crafted hostname cannot inject terminal escapes
// or spoof a table row. SOURCE is the TCP peer address the request came from,
// so a squat attempt from an unexpected network is visible at a glance next
// to the hostname it claims.
func printEnrollList(out io.Writer, records []*enroll.Record) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPEEL ID\tHOSTNAME\tSOURCE\tSTATE\tTRUST\tCREATED")
	for _, rec := range records {
		created := rec.CreatedAt.Local().Format("2006-01-02 15:04:05")
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			rec.ID,
			displayPeel(rec.PeelID),
			dash(displayString(rec.Hostname)),
			dash(displayString(rec.RemoteAddr)),
			rec.State,
			enrollTrustLabel(rec),
			created,
		)
	}
	w.Flush()
}
