package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/auth"
)

// The `zester nats-auth init` verb generates the NATS operator/account/user
// JWT hierarchy and a matching nats-server.conf — the trust plane that the
// external NATS server and the zester daemons authenticate against. Together
// with `zester ca` (the TLS plane) it makes a bare-metal bootstrap fully
// CLI-driven: no hand-written nsc scripts, no separate bootstrap tool.
//
// Like `zester ca`, this is OFFLINE: pure local file I/O, no NATS connection.

var authDir string

var authCmd = &cobra.Command{
	Use:   "nats-auth",
	Short: "Bootstrap and audit the external NATS server's JWT auth (operator/account/user) — offline",
	Long: `Manage the external NATS server's JWT authentication material.

'zester nats-auth init' generates the operator + account + system-account JWT
hierarchy, the master, admin and system-account user credentials, the operator
signing key the master re-signs the account JWT with when peel credentials are
revoked, and a nats-server.conf that trusts them (MEMORY resolver, JetStream
enabled). Run once on the first master host during provisioning, alongside
'zester ca init'.

'zester nats-auth lint' checks a .creds file for JetStream grant gaps and
oversized JWTs offline.`,
}

var authInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate the operator/account/user JWT hierarchy + nats-server.conf",
	RunE: func(cmd *cobra.Command, args []string) error {
		natsConf, _ := cmd.Flags().GetString("nats-conf")
		storeDir, _ := cmd.Flags().GetString("jetstream-store")
		certFile, _ := cmd.Flags().GetString("nats-cert")
		keyFile, _ := cmd.Flags().GetString("nats-key")
		force, _ := cmd.Flags().GetBool("force")

		// Refuse to clobber an existing hierarchy unless --force: account.seed
		// is the fleet trust root (it signs every peel JWT) — regenerating it
		// invalidates every issued credential.
		seedPath := filepath.Join(authDir, auth.FileAccountSeed)
		if _, err := os.Stat(seedPath); err == nil && !force {
			return fmt.Errorf("auth material already exists in %s (%s present); pass --force to regenerate (invalidates all issued credentials)", authDir, auth.FileAccountSeed)
		}

		h, err := auth.GenerateHierarchy(auth.HierarchyOptions{})
		if err != nil {
			return err
		}
		// Operator/account JWTs, the account seed (master reloads it), the
		// operator signing seed (master re-signs the account JWT with it on
		// revocation), master.creds / admin.creds / sys.creds.
		if err := h.WriteFiles(authDir); err != nil {
			return err
		}

		// nats-server.conf: operator mode, MEMORY resolver preloading the
		// account + system account, JetStream enabled, TLS pointing at the
		// (separately, via 'zester ca') issued NATS server cert.
		conf := h.NATSServerConf(auth.NATSServerConfOptions{
			CertFile:        certFile,
			KeyFile:         keyFile,
			StoreDir:        storeDir,
			OperatorJWTPath: filepath.Join(authDir, auth.FileOperatorJWT),
		})
		if err := os.WriteFile(natsConf, []byte(conf), 0644); err != nil {
			return fmt.Errorf("write nats-server.conf: %w", err)
		}

		fmt.Printf("NATS auth hierarchy initialized in %s\n\n", authDir)
		fmt.Printf("  %s / %s / %s\n", auth.FileOperatorJWT, auth.FileAccountJWT, auth.FileAccountSeed)
		fmt.Printf("  %s  (operator signing key — the master re-signs the account JWT with it to revoke peel credentials)\n", auth.FileOperatorSigningSeed)
		fmt.Printf("  %s  (zester-master)\n", auth.FileMasterCreds)
		fmt.Printf("  %s   (operator CLI)\n", auth.FileAdminCreds)
		fmt.Printf("  %s     (zester-master, system account — pushes credential revocations to nats-server)\n", auth.FileSysCreds)
		fmt.Printf("  nats-server.conf → %s\n\n", natsConf)
		fmt.Println("Next steps:")
		fmt.Println("  1. zester ca init && zester ca issue nats-server --dns <name> --out <tls-dir>")
		fmt.Println("     (the nats-server.conf tls{} block above must point at that cert/key).")
		fmt.Printf("  2. Start nats-server with -c %s\n", natsConf)
		fmt.Printf("  3. Multi-master: replicate this auth dir (esp. %s, %s, %s) to every master.\n",
			auth.FileAccountSeed, auth.FileOperatorSigningSeed, auth.FileSysCreds)
		return nil
	},
}

func init() {
	authCmd.PersistentFlags().StringVar(&authDir, "dir", "/var/lib/zester/auth", "auth directory")
	authInitCmd.Flags().String("nats-conf", "/etc/nats/nats-server.conf", "path to write the generated nats-server.conf")
	authInitCmd.Flags().String("jetstream-store", "/var/lib/nats/jetstream", "JetStream store_dir in the generated config")
	authInitCmd.Flags().String("nats-cert", "/var/lib/zester/auth/nats-server.crt", "NATS server certificate path referenced by the generated config")
	authInitCmd.Flags().String("nats-key", "/var/lib/zester/auth/nats-server.key", "NATS server key path referenced by the generated config")
	authInitCmd.Flags().Bool("force", false, "regenerate even if auth material exists (invalidates all issued credentials)")

	authCmd.AddCommand(authInitCmd)
	authCmd.AddCommand(authLintCmd)
	rootCmd.AddCommand(authCmd)
}
