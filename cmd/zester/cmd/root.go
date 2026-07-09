package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/internal/config"
)

var (
	cfgFile string
	cfg     *config.Config
)

var rootCmd = &cobra.Command{
	Use:   "zester [target] [module.function] [args...]",
	Short: "Zester CLI - manage your infrastructure",
	Long: `Zester is a SaltStack alternative built in pure Go, powered by NATS JetStream and nkeys.

Execute modules on targeted peels:
  zester '*' test.ping
  zester 'web-01' cmd.run 'hostname'
  zester 'web-*' state.apply webserver --timeout 5m
  zester 'web-01' facts.get os.family --direct

Manage infrastructure:
  zester job list
  zester peel list
  zester enroll approve <id>`,
	Args:          cobra.ArbitraryArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// Set RunE and PersistentPreRunE in init() to avoid initialization cycle:
	// rootCmd → runExec → connectClient → masterURLs → rootCmd.
	rootCmd.RunE = runExec
	rootCmd.PersistentPreRunE = persistentPreRun

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: /etc/zester/master.yaml or ~/.zester/config.yaml)")
	rootCmd.PersistentFlags().StringSlice("master", nil, "master NATS URL(s) (overrides config)")
	rootCmd.PersistentFlags().String("creds", "", "path to NATS credentials file (overrides config)")
	rootCmd.PersistentFlags().String("nats-ca", "", "CA certificate for NATS TLS verification (overrides config; also honors NATS_CA_FILE)")
	rootCmd.PersistentFlags().Duration("timeout", 60*time.Second, "execution timeout")
	rootCmd.PersistentFlags().String("format", "text", "output format: text, json, yaml")
	rootCmd.PersistentFlags().Bool("no-color", false, "disable colored output")
	rootCmd.PersistentFlags().Bool("direct", false, "bypass master, send directly to peels via request/reply")
	rootCmd.PersistentFlags().Bool("test", false, "dry run: report what would change without applying (Salt test=True)")

	rootCmd.AddCommand(peelCmd)
	rootCmd.AddCommand(kvCmd)
	rootCmd.AddCommand(jobCmd)
	rootCmd.AddCommand(basketCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(versionCmd)
}

func persistentPreRun(cmd *cobra.Command, args []string) error {
	// Skip config loading for commands that don't need it.
	if cmd.Name() == "version" || cmd.Name() == "help" {
		return nil
	}

	// In direct mode with --master and --creds flags, config file is optional.
	direct, _ := cmd.Root().PersistentFlags().GetBool("direct")
	if direct {
		masterFlag, _ := cmd.Root().PersistentFlags().GetStringSlice("master")
		credsFlag, _ := cmd.Root().PersistentFlags().GetString("creds")
		if len(masterFlag) > 0 || credsFlag != "" {
			// Try to load config but don't fail if missing.
			cfg, _ = config.Load(cfgFile)
			return nil
		}
	}

	var err error
	cfg, err = config.Load(cfgFile)
	if err != nil {
		// Config is optional when the connection can be sourced inline — a
		// URL from --master or NATS_URL, creds from --creds, trust from
		// --nats-ca / NATS_CA_FILE. This lets the CLI run on a peel-only box
		// with no /etc/zester or ~/.zester config file.
		if hasInlineConnection() {
			cfg = nil
			return nil
		}
		return fmt.Errorf("load config: %w", err)
	}
	return nil
}

// hasInlineConnection reports whether a NATS URL is available without a config
// file (via --master or NATS_URL); creds and CA can then come from flags/env.
func hasInlineConnection() bool {
	if urls, _ := rootCmd.PersistentFlags().GetStringSlice("master"); len(urls) > 0 {
		return true
	}
	return os.Getenv("NATS_URL") != ""
}

// masterURLs returns the NATS URLs to connect to.
// Priority: --master flag > NATS_URL env var > config file > localhost default.
func masterURLs() []string {
	if urls, _ := rootCmd.Flags().GetStringSlice("master"); len(urls) > 0 {
		return urls
	}
	if envURL := os.Getenv("NATS_URL"); envURL != "" {
		return strings.Split(envURL, ",")
	}
	if cfg != nil && len(cfg.Master.URLs) > 0 {
		return cfg.Master.URLs
	}
	return []string{"nats://localhost:4222"}
}

// exitError prints an error and exits.
func exitError(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}
