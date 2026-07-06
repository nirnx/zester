package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/nirnx/zester/internal/logging"
	"github.com/nirnx/zester/internal/version"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/proto"
	"github.com/nirnx/zester/pkg/update"
)

// watchdogFlags holds every CLI flag value. Registration is separated from
// main so tests can assert flag parity (names + defaults) on a fresh FlagSet.
type watchdogFlags struct {
	childBin       string
	childArgs      string
	natsURL        string
	natsCA         string
	natsCreds      string
	healthURL      string
	readyURL       string
	healthTimeout  time.Duration
	healthInterval time.Duration
	healthRetries  int
	soakTime       time.Duration
	id             string
	component      string
	logLevel       string
	logFormat      string
}

// registerFlags declares all watchdog flags on fs with their defaults.
func registerFlags(fs *flag.FlagSet) *watchdogFlags {
	f := &watchdogFlags{}
	fs.StringVar(&f.childBin, "child-bin", "", "Path to child binary (required)")
	fs.StringVar(&f.childArgs, "child-args", "", "Arguments to pass to child (space-separated)")
	fs.StringVar(&f.natsURL, "nats-url", "tls://localhost:4222", "NATS server URL")
	fs.StringVar(&f.natsCA, "nats-ca", "", "CA certificate for NATS TLS server verification")
	fs.StringVar(&f.natsCreds, "nats-creds", "", "NATS credentials file")
	fs.StringVar(&f.healthURL, "health-url", "http://127.0.0.1:9090/healthz", "Child health endpoint")
	fs.StringVar(&f.readyURL, "ready-url", "", "Child readiness endpoint polled during update soak (default: derived from --health-url by replacing the path with /readyz, so it follows the child's port)")
	fs.DurationVar(&f.healthTimeout, "health-timeout", 5*time.Second, "Health check timeout")
	fs.DurationVar(&f.healthInterval, "health-interval", 10*time.Second, "Health check interval")
	fs.IntVar(&f.healthRetries, "health-retries", 3, "Health failures before rollback")
	fs.DurationVar(&f.soakTime, "soak-time", 60*time.Second, "Post-update soak period")
	fs.StringVar(&f.id, "id", "", "Node identity (required)")
	fs.StringVar(&f.component, "component", "", `"peel" or "master" (required)`)
	fs.StringVar(&f.logLevel, "log-level", logging.DefaultLevel, fmt.Sprintf("Log level (%s)", strings.Join(logging.Levels(), "|")))
	fs.StringVar(&f.logFormat, "log-format", logging.DefaultFormat, fmt.Sprintf("Log format (%s)", strings.Join(logging.Formats(), "|")))
	return f
}

func main() {
	f := registerFlags(flag.CommandLine)
	flag.Parse()

	if f.childBin == "" {
		fmt.Fprintln(os.Stderr, "watchdog: --child-bin is required")
		os.Exit(1)
	}
	if f.id == "" {
		fmt.Fprintln(os.Stderr, "watchdog: --id is required")
		os.Exit(1)
	}
	if f.component == "" {
		fmt.Fprintln(os.Stderr, "watchdog: --component is required")
		os.Exit(1)
	}

	logger, err := logging.Setup(os.Stdout, "watchdog", f.logLevel, f.logFormat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: %v\n", err)
		os.Exit(1)
	}

	// Derive the readiness URL from the health URL when not explicitly set,
	// so overriding --health-url alone (e.g. :9091 for a master child) keeps
	// soak probing the right port instead of a hardcoded :9090 default.
	if f.readyURL == "" {
		derived, dErr := deriveReadyURL(f.healthURL)
		if dErr != nil {
			fmt.Fprintf(os.Stderr, "watchdog: derive --ready-url from --health-url: %v\n", dErr)
			os.Exit(1)
		}
		f.readyURL = derived
	}
	logger.Info("zester-watchdog starting",
		"version", version.Version,
		"git_commit", version.GitCommit,
		"id", f.id,
		"component", f.component,
	)

	// Signal-aware context from the start: a SIGTERM during any of the
	// retry loops below must still stop the supervised child rather than
	// orphaning it.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Validate the NATS configuration before starting the child so a bad
	// config fails fast with nothing to clean up.
	natsURLs := bus.NormalizeNATSURLs([]string{f.natsURL})
	if err := bus.ValidateTLSNATSURLs(natsURLs); err != nil {
		logger.Error("rejecting NATS configuration", "error", err)
		os.Exit(1)
	}
	natsTLS, err := bus.NATSClientTLS(natsURLs, f.natsCA)
	if err != nil {
		logger.Error("configure NATS TLS", "error", err)
		os.Exit(1)
	}

	slots := update.NewSlotManager(f.childBin)
	if err := slots.Recover(); err != nil {
		logger.Warn("slot recovery error", "error", err)
	}

	args := shellSplit(f.childArgs)
	supervisor := update.NewSupervisor(update.SupervisorConfig{
		BinPath:        f.childBin,
		Args:           args,
		HealthURL:      f.healthURL,
		ReadyURL:       f.readyURL,
		HealthTimeout:  f.healthTimeout,
		HealthInterval: f.healthInterval,
		HealthRetries:  f.healthRetries,
		Logger:         logger,
	})

	if err := supervisor.Start(); err != nil {
		logger.Warn("initial child start failed, AutoRestart will retry", "error", err)
	} else {
		logger.Info("child process started", "pid", supervisor.PID())
	}

	go supervisor.AutoRestart(ctx)

	// Connect to NATS — enrollment-aware
	var connectCh <-chan struct{}
	if f.natsCreds != "" {
		if _, err := os.Stat(f.natsCreds); os.IsNotExist(err) {
			logger.Info("creds file not found, entering offline mode", "path", f.natsCreds)
			connectCh = waitForCreds(ctx, f.natsCreds)
		}
	}

	stopChild := func() {
		if err := supervisor.Stop(); err != nil {
			logger.Warn("supervisor stop error", "error", err)
		}
	}

	if connectCh != nil {
		select {
		case <-ctx.Done():
			stopChild()
			return
		case <-connectCh:
			logger.Info("creds file found, connecting to NATS")
		}
	}

	// The child is already running, so NATS/JetStream setup failures are
	// retried indefinitely rather than exiting (which would orphan the
	// child). The watchdog is fully functional as a local supervisor even
	// while the update plane is unreachable.
	retryWait := func(attempt int) bool {
		wait := min(time.Duration(attempt)*time.Second, 30*time.Second)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
			return true
		}
	}

	var client *bus.Client
	for attempt := 1; ; attempt++ {
		var err error
		client, err = bus.NewClient(bus.ClientConfig{
			URLs:         natsURLs,
			Name:         "zester-watchdog-" + f.id,
			CredsFile:    f.natsCreds,
			TLS:          natsTLS,
			Logger:       logger,
			RetryConnect: true,
		})
		if err == nil {
			break
		}
		logger.Warn("NATS connect failed, retrying", "attempt", attempt, "error", err)
		if !retryWait(attempt) {
			stopChild()
			return
		}
	}
	defer func() { _ = client.Shutdown(context.Background()) }()
	logger.Info("connected to NATS", "url", f.natsURL)

	var statusKV bus.KV
	for attempt := 1; ; attempt++ {
		var err error
		statusKV, err = bus.GetBucket(ctx, client.JetStream(), bus.BucketUpdateStatus)
		if err == nil {
			break
		}
		logger.Warn("update-status KV bucket unavailable, retrying", "attempt", attempt, "error", err)
		if !retryWait(attempt) {
			stopChild()
			return
		}
	}

	var objStore jetstream.ObjectStore
	for attempt := 1; ; attempt++ {
		var err error
		objStore, err = client.JetStream().ObjectStore(ctx, bus.ObjectBucketUpdateBinaries)
		if err == nil {
			break
		}
		logger.Warn("update-binaries object store unavailable, retrying", "attempt", attempt, "error", err)
		if !retryWait(attempt) {
			stopChild()
			return
		}
	}

	binaries := update.NewBinaryStore(objStore)

	pubsub := bus.NewNATSPubSub(client.Conn())

	handler := update.NewHandler(update.HandlerConfig{
		ID:         f.id,
		Component:  f.component,
		SoakTime:   f.soakTime,
		PubSub:     pubsub,
		Slots:      slots,
		Supervisor: supervisor,
		Binaries:   binaries,
		Logger:     logger,
	})
	if err := handler.Start(); err != nil {
		logger.Error("failed to start update handler", "error", err)
		stopChild()
		os.Exit(1)
	}

	reporter := update.NewReporter(
		update.ReporterConfig{
			KV:         statusKV,
			ID:         f.id,
			Component:  f.component,
			Logger:     logger,
			DegradedFn: supervisor.IsDegraded,
			Protocol:   proto.ProtocolVersion,
		},
		func() string { return supervisor.HealthVersion(context.Background()) },
		handler.State,
		supervisor.PID,
		supervisor.Uptime,
	)
	go reporter.Run(ctx)

	<-ctx.Done()
	logger.Info("received signal, shutting down")

	if err := handler.Stop(); err != nil {
		logger.Warn("handler stop error", "error", err)
	}
	stopChild()

	logger.Info("watchdog exiting")
}

// deriveReadyURL rewrites a health URL's path to /readyz, preserving
// scheme, host, and port (e.g. http://127.0.0.1:9091/healthz ->
// http://127.0.0.1:9091/readyz).
func deriveReadyURL(healthURL string) (string, error) {
	u, err := url.Parse(healthURL)
	if err != nil {
		return "", fmt.Errorf("watchdog: parse health url %q: %w", healthURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("watchdog: health url %q has no scheme or host", healthURL)
	}
	u.Path = "/readyz"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// shellSplit splits s on whitespace, respecting single and double quotes.
// Quotes are consumed (not included in output). Returns nil for empty input.
func shellSplit(s string) []string {
	var result []string
	var current []byte
	var quote byte // 0 = not in quotes, '\'' or '"' = in that quote type
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case quote == 0 && c == ' ':
			if len(current) > 0 {
				result = append(result, string(current))
				current = current[:0]
			}
		default:
			current = append(current, c)
		}
	}
	if len(current) > 0 {
		result = append(result, string(current))
	}
	return result
}

func waitForCreds(ctx context.Context, path string) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
				if _, err := os.Stat(path); err == nil {
					return
				}
			}
		}
	}()
	return ch
}
