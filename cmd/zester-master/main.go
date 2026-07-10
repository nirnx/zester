// Command zester-master dispatches jobs, compiles settings, and coordinates
// the fleet. main owns flag parsing, config loading, logging setup, and
// signal handling; the daemon runtime itself lives in internal/masterd
// (Daemon).
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
	"github.com/nirnx/zester/internal/masterd"
	"github.com/nirnx/zester/internal/version"
)

func main() {
	fs := flag.CommandLine
	configFile, showVersion, err := setupMasterFlags(fs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zester-master:", err)
		os.Exit(1)
	}
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String("zester-master"))
		return
	}
	// A daemon must never boot because of a mistyped probe: positional args
	// carry no meaning here.
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "zester-master: unexpected argument %q (flags only; see --help, or --version)\n", fs.Arg(0))
		os.Exit(2)
	}

	cfg, err := loadMasterConfig(fs, *configFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zester-master: load config:", err)
		os.Exit(1)
	}

	logger, err := logging.Setup(os.Stdout, "master", cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zester-master:", err)
		os.Exit(1)
	}
	slog.SetDefault(logger)

	d := masterd.New(cfg, logger)

	// Signal handling: on SIGINT/SIGTERM, log the shutdown (same message and
	// master_id attribute as before the masterd extraction — d.Logger() is
	// the master_id-annotated logger) and cancel the context, which unblocks
	// Daemon.Run so its deferred subsystem teardown runs.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		d.Logger().Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	if err := d.Run(ctx); err != nil {
		logger.Error("master fatal", "error", err)
		os.Exit(1)
	}
}

// setupMasterFlags registers the --config flag plus one flag per
// `flag:"..."`-tagged MasterDaemonConfig field (defaults come from
// config.MasterDaemonDefaults). Extracted from main so the flag-parity test
// can assert the exact flag set (names, defaults, usage).
func setupMasterFlags(fs *flag.FlagSet) (*string, *bool, error) {
	configFile := fs.String("config", "", "Path to YAML config file (default: /etc/zester/master.yaml)")
	showVersion := fs.Bool("version", false, "Print version and exit")
	defaults := config.MasterDaemonDefaults()
	if err := config.BindFlags(fs, &defaults); err != nil {
		return nil, nil, fmt.Errorf("main: bind flags: %w", err)
	}
	return configFile, showVersion, nil
}

// loadMasterConfig loads the YAML config file (or built-in defaults when no
// file exists) and overlays the flags the user explicitly set, reproducing
// the precedence flag > config file > default — including an explicitly
// empty --gitfs-remotes "" disabling GitFS over a YAML-provided list.
// Call after fs has been parsed.
func loadMasterConfig(fs *flag.FlagSet, configFile string) (*config.MasterDaemonConfig, error) {
	cfg, err := config.LoadMasterDaemon(configFile)
	if err != nil {
		return nil, err
	}
	if err := config.ApplyVisited(fs, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
