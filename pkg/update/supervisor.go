package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// maxConsecutiveFails is the number of consecutive failed restarts after
// which AutoRestart switches from exponential backoff to the degraded
// slow-retry tier (see SupervisorConfig.DegradedRetryInterval).
const maxConsecutiveFails = 10

// SupervisorConfig configures the child process supervisor.
type SupervisorConfig struct {
	BinPath   string   // Path to child binary
	Args      []string // Arguments to pass to child
	HealthURL string   // Health (liveness) endpoint URL (default: http://127.0.0.1:9090/healthz)
	// ReadyURL is the readiness endpoint probed by CheckReady (e.g.
	// http://127.0.0.1:9090/readyz). It backs the update soak phase, so a
	// child that is alive but functionally dead (NATS-disconnected) fails
	// soak and triggers rollback. Empty means CheckReady falls back to
	// CheckHealth (back-compat: readiness == liveness). Restart/liveness
	// decisions (AutoRestart, WaitForHealthy) always use HealthURL — a NATS
	// outage must never cause the watchdog to kill a healthy child.
	ReadyURL       string
	HealthTimeout  time.Duration // Per-check timeout (default: 5s)
	HealthInterval time.Duration // Check interval (default: 10s)
	HealthRetries  int           // Failures before unhealthy (default: 3)
	// DegradedRetryInterval is how often AutoRestart keeps attempting a
	// restart after maxConsecutiveFails consecutive failures. Degraded mode
	// is a slow-retry tier, not terminal: a child that comes back up clears
	// it (default: 10m).
	DegradedRetryInterval time.Duration
	Stdout                io.Writer // Child stdout (default: os.Stdout)
	Stderr                io.Writer // Child stderr (default: os.Stderr)
	Logger                *slog.Logger
}

// Supervisor manages a child process lifecycle.
type Supervisor struct {
	config SupervisorConfig
	logger *slog.Logger

	mu        sync.Mutex
	cmd       *exec.Cmd
	running   bool
	startedAt time.Time
	pid       int

	// generation increments on every successful Start. Each child's Wait
	// goroutine captures its generation and only clears running if it still
	// matches — a stale goroutine from a previous child must not clobber the
	// state of a newer one.
	generation uint64

	// pauseCount > 0 suspends AutoRestart while another owner (the update
	// handler's apply/rollback sequence) manages the child lifecycle.
	// A counter (not a bool) so overlapping Pause/Resume pairs nest safely.
	pauseCount int

	// Restart backoff state
	consecutiveFails int
	degraded         bool

	// stabilityWindow is how long a restarted child must stay up before
	// AutoRestart resets the failure counter (and clears degraded state).
	// Defaults to 30s; shrunk in tests.
	stabilityWindow time.Duration

	httpClient *http.Client
}

// NewSupervisor creates a new Supervisor with defaults applied.
func NewSupervisor(cfg SupervisorConfig) *Supervisor {
	if cfg.HealthURL == "" {
		cfg.HealthURL = "http://127.0.0.1:9090/healthz"
	}
	if cfg.HealthTimeout == 0 {
		cfg.HealthTimeout = 5 * time.Second
	}
	if cfg.HealthInterval == 0 {
		cfg.HealthInterval = 10 * time.Second
	}
	if cfg.HealthRetries == 0 {
		cfg.HealthRetries = 3
	}
	if cfg.DegradedRetryInterval == 0 {
		cfg.DegradedRetryInterval = 10 * time.Minute
	}
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Supervisor{
		config:          cfg,
		logger:          cfg.Logger,
		stabilityWindow: 30 * time.Second,
		httpClient: &http.Client{
			Timeout: cfg.HealthTimeout,
		},
	}
}

// Start launches the child process. If a child is already running, Start is
// a no-op (returns nil) — it never spawns a second child.
func (s *Supervisor) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startLocked()
}

// startLocked launches the child process. Caller must hold s.mu.
func (s *Supervisor) startLocked() error {
	if s.running {
		s.logger.Warn("update: start requested but child already running", "pid", s.pid)
		return nil
	}

	cmd := exec.Command(s.config.BinPath, s.config.Args...)
	cmd.Stdout = s.config.Stdout
	cmd.Stderr = s.config.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("update: start child process: %w", err)
	}

	s.generation++
	gen := s.generation
	s.cmd = cmd
	s.pid = cmd.Process.Pid
	s.startedAt = time.Now()
	s.running = true

	go func() {
		_ = cmd.Wait()
		s.childExited(gen)
	}()

	return nil
}

// childExited clears the running state for the child of the given generation.
// A stale Wait goroutine (from an older child) is a no-op.
func (s *Supervisor) childExited(gen uint64) {
	s.mu.Lock()
	if s.generation == gen {
		s.running = false
	}
	s.mu.Unlock()
}

// Stop sends SIGTERM to the child process, waiting up to 10s before SIGKILL.
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	proc := s.cmd.Process
	s.mu.Unlock()

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		// Process may have exited between the running check and signal.
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return fmt.Errorf("update: send SIGTERM: %w", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		running := s.running
		s.mu.Unlock()
		if !running {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running {
		// Ignore ErrProcessDone — process may have been reaped between check and signal.
		// Don't set running=false here; let the Wait goroutine be the sole owner of that transition.
		if err := proc.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("update: send SIGKILL: %w", err)
		}
		// Wait for the Wait goroutine to observe the exit so a subsequent
		// Start() doesn't no-op against a dying child. SIGKILL cannot be
		// caught, so this resolves quickly.
		killDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(killDeadline) {
			s.mu.Lock()
			running := s.running
			s.mu.Unlock()
			if !running {
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		return fmt.Errorf("update: child did not exit after SIGKILL")
	}

	return nil
}

// Restart stops and restarts the child process.
func (s *Supervisor) Restart() error {
	if err := s.Stop(); err != nil {
		return err
	}
	return s.Start()
}

// IsRunning reports whether the child process is currently running.
func (s *Supervisor) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Pause suspends AutoRestart: while paused, AutoRestart neither counts
// failures nor restarts the child. Callers (the update handler) pair it with
// Resume around Stop/swap/Start sequences they own. Start/Stop/Restart are
// unaffected by pause.
func (s *Supervisor) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pauseCount++
}

// Resume lifts a previous Pause. Extra Resume calls are ignored.
func (s *Supervisor) Resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pauseCount > 0 {
		s.pauseCount--
	}
}

// IsPaused reports whether AutoRestart is currently suspended.
func (s *Supervisor) IsPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pauseCount > 0
}

// PID returns the current child process PID.
func (s *Supervisor) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pid
}

// StartedAt returns the time the child process was started.
func (s *Supervisor) StartedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startedAt
}

// HealthInterval returns the configured health check interval.
func (s *Supervisor) HealthInterval() time.Duration {
	return s.config.HealthInterval
}

// HealthRetries returns the configured number of health check retries.
func (s *Supervisor) HealthRetries() int {
	return s.config.HealthRetries
}

// Uptime returns the duration since the child process started, or 0 if not running.
func (s *Supervisor) Uptime() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return 0
	}
	return time.Since(s.startedAt)
}

// CheckHealth performs a single liveness check against HealthURL. It backs
// every restart decision (AutoRestart monitoring, WaitForHealthy after a
// start) and must stay a pure liveness signal.
func (s *Supervisor) CheckHealth(ctx context.Context) error {
	return s.checkEndpoint(ctx, s.config.HealthURL)
}

// CheckReady performs a single readiness probe against ReadyURL. When
// ReadyURL is unset it falls back to CheckHealth (back-compat: readiness and
// liveness share the same endpoint). Used by the update handler's soak phase
// so a functionally dead child fails soak — never by restart monitoring.
func (s *Supervisor) CheckReady(ctx context.Context) error {
	if s.config.ReadyURL == "" {
		return s.CheckHealth(ctx)
	}
	return s.checkEndpoint(ctx, s.config.ReadyURL)
}

// checkEndpoint probes url, requiring HTTP 200 and a JSON body whose "status"
// field is "ok" or "degraded" (both /healthz and internal/health's Checker
// satisfy it; degraded = working but impaired, not probe-fatal).
func (s *Supervisor) checkEndpoint(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("update: create health request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("update: health check request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update: health check returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("update: read health response: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("update: parse health response: %w", err)
	}

	status, _ := result["status"].(string)
	// "degraded" (from internal/health's Checker) means working but
	// impaired — served with HTTP 200 and not a reason to fail soak or
	// restart the child. Only a non-OK/non-degraded status fails the probe.
	if !strings.EqualFold(status, "ok") && !strings.EqualFold(status, "degraded") {
		return fmt.Errorf("update: health status %q", status)
	}

	return nil
}

// WaitForHealthy polls CheckHealth until healthy or retries exhausted.
func (s *Supervisor) WaitForHealthy(ctx context.Context) error {
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("update: wait for healthy: %w", ctx.Err())
		default:
		}

		if err := s.CheckHealth(ctx); err != nil {
			failures++
			if failures >= s.config.HealthRetries {
				return fmt.Errorf("update: health check failed after %d retries: %w", failures, err)
			}
			s.logger.Warn("update: health check failed", "attempt", failures, "err", err)
		} else {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("update: wait for healthy: %w", ctx.Err())
		case <-time.After(s.config.HealthInterval):
		}
	}
}

// AutoRestart monitors the child process and restarts it on unexpected exit.
// It runs until ctx is cancelled. After maxConsecutiveFails consecutive failed
// restarts it enters a degraded slow-retry tier: the degraded flag is set
// (visible via IsDegraded and fleet status) and restart attempts continue
// every DegradedRetryInterval instead of stopping. A child that starts and
// survives the stability window clears degraded state, resets the failure
// counter/backoff, and resumes normal monitoring.
func (s *Supervisor) AutoRestart(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// While paused, another owner (update apply/rollback) manages the
		// child — a stopped child is expected, not a failure.
		if s.IsRunning() || s.IsPaused() {
			time.Sleep(time.Second)
			continue
		}

		s.mu.Lock()
		s.consecutiveFails++
		fails := s.consecutiveFails
		s.mu.Unlock()

		var wait time.Duration
		if fails >= maxConsecutiveFails {
			// Degraded slow-retry tier: never give up, but back way off so a
			// persistently broken child doesn't burn cycles. Cleared below
			// once a restarted child survives the stability window.
			s.mu.Lock()
			alreadyDegraded := s.degraded
			s.degraded = true
			s.mu.Unlock()
			if !alreadyDegraded {
				s.logger.Error("update: child process degraded after repeated failures, entering slow retry",
					"consecutive_fails", fails, "retry_interval", s.config.DegradedRetryInterval)
			}
			wait = s.config.DegradedRetryInterval
		} else {
			wait = time.Duration(1<<uint(fails)) * time.Second
			if wait > 60*time.Second {
				wait = 60 * time.Second
			}
			s.logger.Warn("update: child exited unexpectedly, restarting", "consecutive_fails", fails, "backoff", wait)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		// Re-check pause/running atomically with the start: a Pause taken
		// during the backoff sleep must win, and startLocked's running guard
		// prevents a double child if something else started one meanwhile.
		s.mu.Lock()
		if s.pauseCount > 0 {
			s.mu.Unlock()
			continue
		}
		err := s.startLocked()
		s.mu.Unlock()
		if err != nil {
			s.logger.Error("update: failed to restart child", "err", err)
			continue
		}

		// If the child survives the stability window, reset failure counters
		// and clear degraded state.
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.stabilityWindow):
		}

		if s.IsRunning() {
			s.mu.Lock()
			wasDegraded := s.degraded
			s.consecutiveFails = 0
			s.degraded = false
			s.mu.Unlock()
			if wasDegraded {
				s.logger.Info("update: child recovered, leaving degraded state")
			}
		}
	}
}

// HealthVersion queries the health endpoint and extracts the "version" field.
// Returns "unknown" on any error or if the field is absent.
func (s *Supervisor) HealthVersion(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.config.HealthURL, nil)
	if err != nil {
		return "unknown"
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "unknown"
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "unknown"
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return "unknown"
	}

	v, ok := result["version"].(string)
	if !ok || v == "" {
		return "unknown"
	}
	return v
}

// IsDegraded reports whether the supervisor is in the degraded slow-retry
// tier: repeated restarts failed and AutoRestart is now retrying every
// DegradedRetryInterval. It clears automatically once a restarted child
// survives the stability window.
func (s *Supervisor) IsDegraded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.degraded
}
