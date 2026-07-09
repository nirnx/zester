package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/auth"
)

// `zester auth lint` decodes one or more NATS .creds files and checks their
// JetStream grants for the access-pattern gaps that cause silent runtime
// permission denials — the class that shipped twice (the update-status key and
// the object-store flow-control subject). Offline: pure local file I/O.
var authLintCmd = &cobra.Command{
	Use:   "lint <creds-file>...",
	Short: "Check NATS creds for missing JetStream access grants (offline)",
	Long: `Decode NATS .creds files and flag JetStream access-pattern gaps.

Grants are hand-enumerated, and JetStream needs companion subjects that are
easy to miss because the client library (not our code) mints them at runtime —
e.g. an object-store download or KV watch opens an ordered consumer that must
publish flow-control acks to $JS.FC.<stream>.>. lint keys off the grants the
creds DO carry (consumer-create on a stream) and reports the companion grants
that access pattern requires. Exits non-zero if any error-level gap is found.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runAuthLint,
}

func runAuthLint(cmd *cobra.Command, args []string) error {
	hadError := false
	for _, path := range args {
		findings, err := auth.LintCredsFile(path)
		if err != nil {
			return fmt.Errorf("lint %s: %w", path, err)
		}
		fmt.Printf("%s:\n", path)
		if len(findings) == 0 {
			fmt.Println("  OK — no JetStream grant gaps found")
			continue
		}
		for _, f := range findings {
			fmt.Printf("  [%s] %s: %s\n", f.Severity, f.Rule, f.Message)
			if f.Severity == auth.LintError {
				hadError = true
			}
		}
	}
	if hadError {
		return fmt.Errorf("auth lint: one or more creds have error-level grant gaps")
	}
	return nil
}
