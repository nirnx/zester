package cmd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/ca"
)

// The `zester ca` verbs are deliberately OFFLINE: pure local file I/O, no
// NATS, no master. CA creation must work before any bus exists (the
// external NATS server needs its certificate first), so these run on the
// master host — typically as root — during initial provisioning.

var caDir string

var caCmd = &cobra.Command{
	Use:   "ca",
	Short: "Manage the embedded certificate authority (offline)",
	Long: `Manage Zester's embedded certificate authority.

All ca subcommands are offline: they operate on local files only and need
no NATS connection. Typical bootstrap on the first master host:

  zester ca init --dir /var/lib/zester/auth/ca
  zester ca issue nats-server --dns nats.example.com --out /etc/nats/tls
  # install the issued cert/key on the NATS host, then start zester-master

'zester ca init' prints the root SPKI pin — distribute it to peels as
enroll_ca_pin for verified first-contact enrollment.`,
}

var caInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate the root + signing-intermediate hierarchy",
	RunE: func(cmd *cobra.Command, args []string) error {
		org, _ := cmd.Flags().GetString("org")
		cn, _ := cmd.Flags().GetString("cn")
		rootValidity, _ := cmd.Flags().GetDuration("root-validity")

		if ca.Exists(caDir) {
			return fmt.Errorf("CA material already exists in %s (refusing to overwrite)", caDir)
		}
		authority, err := ca.Generate(ca.Config{
			Organization: org,
			CommonName:   cn,
			RootValidity: rootValidity,
		})
		if err != nil {
			return err
		}
		if err := authority.Save(caDir); err != nil {
			return err
		}

		fmt.Printf("Embedded CA initialized in %s\n\n", caDir)
		fmt.Printf("  Root subject:  %s\n", authority.Root.Subject)
		fmt.Printf("  Valid until:   %s\n", authority.Root.NotAfter.Format(time.RFC3339))
		fmt.Printf("  Fingerprint:   %s\n", authority.Fingerprint())
		fmt.Printf("  SPKI pin:      %s\n\n", authority.RootSPKIPin())
		fmt.Println("Next steps:")
		fmt.Println("  1. zester ca issue nats-server --dns <every name peels dial> --out <dir>")
		fmt.Println("     and install the cert/key on the NATS server host(s).")
		fmt.Println("  2. Distribute the SPKI pin to peels (enroll_ca_pin in peel.yaml).")
		fmt.Println("  3. Multi-master: replicate this CA directory to every master,")
		fmt.Println("     exactly like account.seed.")
		return nil
	},
}

var caFingerprintCmd = &cobra.Command{
	Use:   "fingerprint",
	Short: "Print the root SPKI pin (one line, for provisioning templates)",
	RunE: func(cmd *cobra.Command, args []string) error {
		authority, err := ca.Load(caDir)
		if err != nil {
			return err
		}
		fmt.Println(authority.RootSPKIPin())
		return nil
	},
}

var caPrintCmd = &cobra.Command{
	Use:   "print",
	Short: "Show CA details and the PEM trust bundle",
	RunE: func(cmd *cobra.Command, args []string) error {
		authority, err := ca.Load(caDir)
		if err != nil {
			return err
		}
		fmt.Printf("Root:          %s\n", authority.Root.Subject)
		fmt.Printf("  Valid:       %s – %s\n", authority.Root.NotBefore.Format(time.RFC3339), authority.Root.NotAfter.Format(time.RFC3339))
		fmt.Printf("  Fingerprint: %s\n", authority.Fingerprint())
		fmt.Printf("  SPKI pin:    %s\n", authority.RootSPKIPin())
		fmt.Printf("Intermediate:  %s\n", authority.Intermediate.Subject)
		fmt.Printf("  Valid:       %s – %s\n", authority.Intermediate.NotBefore.Format(time.RFC3339), authority.Intermediate.NotAfter.Format(time.RFC3339))
		if authority.RootKey == nil {
			fmt.Println("Root key:      offline (not present in the CA directory)")
		}
		fmt.Printf("\n%s", authority.Bundle())
		return nil
	},
}

var caIssueCmd = &cobra.Command{
	Use:   "issue <profile>",
	Short: "Issue a server certificate (profiles: nats-server, enroll)",
	Long: `Issue a TLS server certificate signed by the embedded CA.

Profiles:
  nats-server  certificate for the external NATS server; SANs must cover
               EVERY name or IP peels dial (nats_url / nats_advertise_urls,
               plus localhost for a colocated watchdog)
  enroll       certificate for the master's enrollment HTTPS listener
               (normally self-issued by the master in embedded mode; this
               verb is the manual/offline variant)`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		profile := args[0]
		dnsNames, _ := cmd.Flags().GetStringSlice("dns")
		ipStrs, _ := cmd.Flags().GetStringSlice("ip")
		outDir, _ := cmd.Flags().GetString("out")
		validity, _ := cmd.Flags().GetDuration("validity")

		var cn, certName, keyName string
		switch profile {
		case "nats-server":
			cn = "nats"
			certName, keyName = "nats-server.crt", "nats-server.key"
			if len(dnsNames) == 0 && len(ipStrs) == 0 {
				return fmt.Errorf("nats-server needs at least one --dns or --ip SAN (every name peels dial)")
			}
		case "enroll":
			cn = "zester-master"
			certName, keyName = "enroll.crt", "enroll.key"
			if len(dnsNames) == 0 && len(ipStrs) == 0 {
				host, _ := os.Hostname()
				dnsNames = []string{host, "localhost"}
			}
		default:
			return fmt.Errorf("unknown profile %q (want nats-server or enroll)", profile)
		}

		var ips []net.IP
		for _, s := range ipStrs {
			ip := net.ParseIP(strings.TrimSpace(s))
			if ip == nil {
				return fmt.Errorf("invalid --ip %q", s)
			}
			ips = append(ips, ip)
		}

		authority, err := ca.Load(caDir)
		if err != nil {
			return err
		}
		leaf, err := authority.IssueServer(cn, dnsNames, ips, validity)
		if err != nil {
			return err
		}

		if err := os.MkdirAll(outDir, 0755); err != nil {
			return err
		}
		certPath := filepath.Join(outDir, certName)
		keyPath := filepath.Join(outDir, keyName)
		if err := os.WriteFile(certPath, leaf.CertPEM, 0644); err != nil {
			return err
		}
		if err := os.WriteFile(keyPath, leaf.KeyPEM, 0600); err != nil {
			return err
		}

		fmt.Printf("Issued %s certificate (valid until %s)\n", profile, leaf.Cert.NotAfter.Format(time.RFC3339))
		fmt.Printf("  Certificate: %s (leaf + intermediate chain)\n", certPath)
		fmt.Printf("  Private key: %s\n", keyPath)
		fmt.Printf("  SANs:        dns=%v ip=%v\n", leaf.Cert.DNSNames, leaf.Cert.IPAddresses)
		if profile == "nats-server" {
			fmt.Println("\nInstall on the NATS host, point the tls{} block at the files, then:")
			fmt.Println("  nats-server --signal reload   # hitless for live connections")
		}
		return nil
	},
}

func init() {
	caCmd.PersistentFlags().StringVar(&caDir, "dir", "/var/lib/zester/auth/ca", "CA directory")

	caInitCmd.Flags().String("org", "Zester", "organization name on the CA subjects")
	caInitCmd.Flags().String("cn", "", "root common name (default: '<org> Root CA')")
	caInitCmd.Flags().Duration("root-validity", ca.DefaultRootValidity, "root certificate validity")

	caIssueCmd.Flags().StringSlice("dns", nil, "DNS SANs (repeatable / comma-separated)")
	caIssueCmd.Flags().StringSlice("ip", nil, "IP SANs (repeatable / comma-separated)")
	caIssueCmd.Flags().String("out", ".", "output directory for the cert/key pair")
	caIssueCmd.Flags().Duration("validity", 365*24*time.Hour, "certificate validity")

	caCmd.AddCommand(caInitCmd, caFingerprintCmd, caPrintCmd, caIssueCmd)
	rootCmd.AddCommand(caCmd)
}
