// Command zester-peel is the agent running on managed nodes. main owns flag
// parsing, config loading, logging setup, and signal handling; the daemon
// runtime itself lives in internal/peeld (Agent).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nirnx/zester/internal/config"
	"github.com/nirnx/zester/internal/logging"
	"github.com/nirnx/zester/internal/peeld"
)

func main() {
	defaults := config.PeelDefaults()
	fs, configFile, err := peelFlags(&defaults)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	_ = fs.Parse(os.Args[1:]) // flag.ExitOnError: Parse exits on bad input

	cfg, err := config.LoadPeel(*configFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: load config:", err)
		os.Exit(1)
	}

	// CLI flags override config file values (only flags explicitly set by
	// the user, per fs.Visit — an explicitly empty value still overrides).
	if err := config.ApplyVisited(fs, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	logger, err := logging.Setup(os.Stdout, "peel", cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	// Resolve the peel id: an explicit configured id, else the identity
	// pinned at <auth_dir>/node-id, else the machine hostname (`hostname -f`),
	// sanitized into a valid NATS subject token. An FQDN like
	// "web01.example.com" becomes "web01_example_com".
	id, err := config.ResolveNodeID(cfg.ID, cfg.AuthDir, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	cfg.ID = id
	logger = logger.With("peel_id", cfg.ID)
	slog.SetDefault(logger)

	// Signal handling: on SIGINT/SIGTERM, log the shutdown (same message as
	// before the peeld extraction) and cancel the context, which unblocks
	// Agent.Run so its deferred subsystem teardown runs.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("received signal, shutting down", "peel", cfg.ID, "signal", sig)
		cancel()
	}()

	if err := peeld.New(cfg, logger).Run(ctx); err != nil {
		logger.Error("peel fatal", "peel", cfg.ID, "error", err)
		os.Exit(1)
	}
}

// peelFlags builds the zester-peel flag set: the hand-registered --config
// flag plus one flag per `flag:"..."`-tagged PeelConfig field, with defaults
// taken from the passed struct. Extracted from main for flag-parity testing.
func peelFlags(defaults *config.PeelConfig) (*flag.FlagSet, *string, error) {
	fs := flag.NewFlagSet("zester-peel", flag.ExitOnError)
	configFile := fs.String("config", "", "Path to YAML config file (default: /etc/zester/peel.yaml)")
	if err := config.BindFlags(fs, defaults); err != nil {
		return nil, nil, fmt.Errorf("register flags: %w", err)
	}
	return fs, configFile, nil
}
