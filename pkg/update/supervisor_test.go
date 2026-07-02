package update

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testSupervisor creates a Supervisor with Stdout/Stderr discarded to avoid
// holding test runner pipes open (which causes "Test I/O incomplete" failures).
func testSupervisor(cfg SupervisorConfig) *Supervisor {
	cfg.Stdout = io.Discard
	cfg.Stderr = io.Discard
	return NewSupervisor(cfg)
}

func TestSupervisor_StartStop(t *testing.T) {
	sup := testSupervisor(SupervisorConfig{
		BinPath: "/bin/sleep",
		Args:    []string{"10"},
	})
	if err := sup.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer sup.Stop()

	if !sup.IsRunning() {
		t.Error("IsRunning() should be true after Start()")
	}
	if sup.PID() <= 0 {
		t.Errorf("PID() = %d, want > 0", sup.PID())
	}
	if sup.Uptime() <= 0 {
		t.Error("Uptime() should be > 0 after Start()")
	}

	if err := sup.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
	if sup.IsRunning() {
		t.Error("IsRunning() should be false after Stop()")
	}
}

func TestSupervisor_Restart(t *testing.T) {
	sup := testSupervisor(SupervisorConfig{
		BinPath: "/bin/sleep",
		Args:    []string{"10"},
	})
	if err := sup.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer sup.Stop()

	oldPID := sup.PID()

	if err := sup.Restart(); err != nil {
		t.Fatalf("Restart() error: %v", err)
	}

	if !sup.IsRunning() {
		t.Error("IsRunning() should be true after Restart()")
	}
	if sup.PID() == oldPID {
		t.Errorf("PID() = %d, want != %d (new process)", sup.PID(), oldPID)
	}
}

func TestSupervisor_StopNotRunning(t *testing.T) {
	sup := testSupervisor(SupervisorConfig{
		BinPath: "/bin/sleep",
	})
	if err := sup.Stop(); err != nil {
		t.Errorf("Stop() on non-started supervisor returned error: %v", err)
	}
}

func TestSupervisor_StartedAt(t *testing.T) {
	before := time.Now()
	sup := testSupervisor(SupervisorConfig{
		BinPath: "/bin/sleep",
		Args:    []string{"10"},
	})
	if err := sup.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer sup.Stop()

	startedAt := sup.StartedAt()
	after := time.Now()

	if startedAt.Before(before) {
		t.Errorf("StartedAt() = %v, want >= %v", startedAt, before)
	}
	if startedAt.After(after) {
		t.Errorf("StartedAt() = %v, want <= %v", startedAt, after)
	}
}

func TestSupervisor_CheckHealth_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer srv.Close()

	sup := testSupervisor(SupervisorConfig{
		BinPath:       "/bin/sleep",
		HealthURL:     srv.URL,
		HealthTimeout: 100 * time.Millisecond,
	})

	ctx := context.Background()
	if err := sup.CheckHealth(ctx); err != nil {
		t.Errorf("CheckHealth() unexpected error: %v", err)
	}
}

func TestSupervisor_CheckHealth_Unhealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "degraded"})
	}))
	defer srv.Close()

	sup := testSupervisor(SupervisorConfig{
		BinPath:       "/bin/sleep",
		HealthURL:     srv.URL,
		HealthTimeout: 100 * time.Millisecond,
	})

	ctx := context.Background()
	if err := sup.CheckHealth(ctx); err == nil {
		t.Error("CheckHealth() expected error for 503 response")
	}
}

func TestSupervisor_CheckHealth_ServerDown(t *testing.T) {
	sup := testSupervisor(SupervisorConfig{
		BinPath:       "/bin/sleep",
		HealthURL:     "http://127.0.0.1:1/healthz",
		HealthTimeout: 100 * time.Millisecond,
	})

	ctx := context.Background()
	if err := sup.CheckHealth(ctx); err == nil {
		t.Error("CheckHealth() expected error for unreachable server")
	}
}

func TestSupervisor_WaitForHealthy_ImmediateSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer srv.Close()

	sup := testSupervisor(SupervisorConfig{
		BinPath:        "/bin/sleep",
		HealthURL:      srv.URL,
		HealthTimeout:  100 * time.Millisecond,
		HealthInterval: 50 * time.Millisecond,
		HealthRetries:  3,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := sup.WaitForHealthy(ctx); err != nil {
		t.Errorf("WaitForHealthy() unexpected error: %v", err)
	}
}

func TestSupervisor_WaitForHealthy_RetriesExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "degraded"})
	}))
	defer srv.Close()

	sup := testSupervisor(SupervisorConfig{
		BinPath:        "/bin/sleep",
		HealthURL:      srv.URL,
		HealthTimeout:  100 * time.Millisecond,
		HealthInterval: 50 * time.Millisecond,
		HealthRetries:  2,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sup.WaitForHealthy(ctx); err == nil {
		t.Error("WaitForHealthy() expected error after retries exhausted")
	}
}

// statusServer returns an httptest server responding with the given HTTP
// code and JSON status body. Closed via t.Cleanup.
func statusServer(t *testing.T, code int, status string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSupervisor_CheckReady_FallsBackToCheckHealth verifies that with an
// empty ReadyURL, CheckReady probes HealthURL (back-compat).
func TestSupervisor_CheckReady_FallsBackToCheckHealth(t *testing.T) {
	healthy := statusServer(t, http.StatusOK, "ok")

	sup := testSupervisor(SupervisorConfig{
		BinPath:       "/bin/sleep",
		HealthURL:     healthy.URL,
		HealthTimeout: 100 * time.Millisecond,
	})
	if err := sup.CheckReady(context.Background()); err != nil {
		t.Errorf("CheckReady() with empty ReadyURL should fall back to healthy HealthURL, got: %v", err)
	}

	// And the fallback must observe HealthURL failures too.
	down := testSupervisor(SupervisorConfig{
		BinPath:       "/bin/sleep",
		HealthURL:     "http://127.0.0.1:1/healthz",
		HealthTimeout: 100 * time.Millisecond,
	})
	if err := down.CheckReady(context.Background()); err == nil {
		t.Error("CheckReady() with empty ReadyURL should fail when HealthURL is unreachable")
	}
}

// TestSupervisor_CheckReady_ProbesReadyURL verifies CheckReady uses ReadyURL
// when set, independently of HealthURL: liveness can pass while readiness
// fails (the NATS-disconnected child case) and vice versa.
func TestSupervisor_CheckReady_ProbesReadyURL(t *testing.T) {
	healthy := statusServer(t, http.StatusOK, "ok")
	notReady := statusServer(t, http.StatusServiceUnavailable, "down")

	// Alive but not ready: CheckHealth OK, CheckReady fails.
	sup := testSupervisor(SupervisorConfig{
		BinPath:       "/bin/sleep",
		HealthURL:     healthy.URL,
		ReadyURL:      notReady.URL,
		HealthTimeout: 100 * time.Millisecond,
	})
	if err := sup.CheckHealth(context.Background()); err != nil {
		t.Errorf("CheckHealth() should pass (liveness OK): %v", err)
	}
	if err := sup.CheckReady(context.Background()); err == nil {
		t.Error("CheckReady() should fail against a 503 ReadyURL")
	}

	// Ready even though HealthURL is unreachable: CheckReady must not touch
	// HealthURL when ReadyURL is set.
	sup2 := testSupervisor(SupervisorConfig{
		BinPath:       "/bin/sleep",
		HealthURL:     "http://127.0.0.1:1/healthz",
		ReadyURL:      healthy.URL,
		HealthTimeout: 100 * time.Millisecond,
	})
	if err := sup2.CheckReady(context.Background()); err != nil {
		t.Errorf("CheckReady() should probe ReadyURL only, got: %v", err)
	}
}

func TestSupervisor_IsDegraded_InitiallyFalse(t *testing.T) {
	sup := testSupervisor(SupervisorConfig{
		BinPath: "/bin/sleep",
	})
	if sup.IsDegraded() {
		t.Error("IsDegraded() should be false for a new supervisor")
	}
}

func TestAutoRestart_RestartsOnExit(t *testing.T) {
	sup := testSupervisor(SupervisorConfig{
		BinPath: "/usr/bin/true",
	})
	// Start the process — it exits immediately.
	if err := sup.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer sup.Stop()

	// Wait for the process to exit.
	deadline := time.Now().Add(2 * time.Second)
	for sup.IsRunning() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if sup.IsRunning() {
		t.Fatal("child should have exited")
	}
	oldPID := sup.PID()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run AutoRestart in a goroutine — it will detect exit, backoff ~2s, then restart.
	done := make(chan struct{})
	go func() {
		sup.AutoRestart(ctx)
		close(done)
	}()

	// Wait for the PID to change (restart happened).
	deadline = time.Now().Add(8 * time.Second)
	restarted := false
	for time.Now().Before(deadline) {
		if sup.PID() != oldPID {
			restarted = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancel()
	<-done

	if !restarted {
		t.Error("AutoRestart did not restart the child process (PID unchanged)")
	}
}

// preseedFails puts the supervisor one failure away from the degraded
// threshold so tests don't wait through exponential backoff.
func preseedFails(sup *Supervisor) {
	sup.mu.Lock()
	sup.consecutiveFails = maxConsecutiveFails - 1
	sup.mu.Unlock()
}

func failCount(sup *Supervisor) int {
	sup.mu.Lock()
	defer sup.mu.Unlock()
	return sup.consecutiveFails
}

func waitForDegraded(t *testing.T, sup *Supervisor) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !sup.IsDegraded() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !sup.IsDegraded() {
		t.Fatal("IsDegraded() never became true after repeated failures")
	}
}

func TestAutoRestart_DegradedKeepsRetrying(t *testing.T) {
	sup := testSupervisor(SupervisorConfig{
		BinPath:               "/nonexistent/binary",
		DegradedRetryInterval: 50 * time.Millisecond,
	})
	sup.stabilityWindow = 50 * time.Millisecond
	preseedFails(sup)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sup.AutoRestart(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("AutoRestart did not return after context cancellation")
		}
	})

	waitForDegraded(t, sup)

	// Degraded is a slow-retry tier, not terminal: the loop must keep
	// attempting restarts. Each failed slow-retry iteration increments the
	// consecutive-fail counter past the threshold — observing that proves at
	// least one retry attempt happened after degraded was set.
	deadline := time.Now().Add(5 * time.Second)
	retried := false
	for time.Now().Before(deadline) {
		if failCount(sup) > maxConsecutiveFails {
			retried = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !retried {
		t.Error("AutoRestart did not attempt a slow retry after entering degraded state")
	}

	select {
	case <-done:
		t.Error("AutoRestart returned while degraded — degraded must not be terminal")
	default:
	}
	if !sup.IsDegraded() {
		t.Error("IsDegraded() should remain true while retries keep failing")
	}
}

func TestAutoRestart_DegradedRecoversWhenChildComesBack(t *testing.T) {
	sup := testSupervisor(SupervisorConfig{
		BinPath:               "/nonexistent/binary",
		DegradedRetryInterval: 50 * time.Millisecond,
	})
	sup.stabilityWindow = 100 * time.Millisecond
	preseedFails(sup)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sup.AutoRestart(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("AutoRestart did not return after context cancellation")
		}
		_ = sup.Stop()
	})

	waitForDegraded(t, sup)

	// "Fix" the underlying issue: point at a binary that stays up. config is
	// read under s.mu by startLocked, so mutate under the same lock.
	sup.mu.Lock()
	sup.config.BinPath = "/bin/sleep"
	sup.config.Args = []string{"60"}
	sup.mu.Unlock()

	// The slow-retry tier should start the child, see it survive the
	// stability window, clear degraded, and reset the failure counter.
	deadline := time.Now().Add(5 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		if !sup.IsDegraded() && sup.IsRunning() {
			recovered = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !recovered {
		t.Fatal("supervisor did not recover from degraded state after child became startable")
	}
	if fails := failCount(sup); fails != 0 {
		t.Errorf("consecutiveFails = %d after recovery, want 0", fails)
	}
}

func TestAutoRestart_CtxCancelDuringDegradedSleep(t *testing.T) {
	sup := testSupervisor(SupervisorConfig{
		BinPath:               "/nonexistent/binary",
		DegradedRetryInterval: time.Hour, // ensure we're parked in the degraded sleep
	})
	preseedFails(sup)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		sup.AutoRestart(ctx)
		close(done)
	}()

	waitForDegraded(t, sup)
	// Give the loop a moment to enter the hour-long degraded sleep.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// exited cleanly during the degraded sleep — good.
	case <-time.After(2 * time.Second):
		t.Fatal("AutoRestart did not return when ctx was cancelled during degraded sleep")
	}
}

func TestAutoRestart_CancelledByContext(t *testing.T) {
	sup := testSupervisor(SupervisorConfig{
		BinPath: "/bin/sleep",
		Args:    []string{"60"},
	})
	if err := sup.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer sup.Stop()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		sup.AutoRestart(ctx)
		close(done)
	}()

	// Let AutoRestart loop at least once (it sleeps 1s when running).
	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// AutoRestart exited — good.
	case <-time.After(5 * time.Second):
		t.Fatal("AutoRestart did not return after context cancellation")
	}

	// Process should still be running (AutoRestart doesn't stop it on ctx cancel).
	if !sup.IsRunning() {
		t.Error("child process should still be running after context cancellation")
	}
}

func TestSupervisor_StartWhileRunning_NoOp(t *testing.T) {
	sup := testSupervisor(SupervisorConfig{
		BinPath: "/bin/sleep",
		Args:    []string{"60"},
	})
	if err := sup.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer sup.Stop()

	pid := sup.PID()

	// Second Start must not spawn a second child.
	if err := sup.Start(); err != nil {
		t.Errorf("Start() on running supervisor returned error: %v", err)
	}
	if !sup.IsRunning() {
		t.Error("IsRunning() should still be true")
	}
	if sup.PID() != pid {
		t.Errorf("PID changed after second Start: got %d, want %d (no new process)", sup.PID(), pid)
	}
}

func TestSupervisor_StaleWaitDoesNotClearRunning(t *testing.T) {
	sup := testSupervisor(SupervisorConfig{
		BinPath: "/bin/sleep",
		Args:    []string{"60"},
	})
	if err := sup.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer sup.Stop()

	sup.mu.Lock()
	gen := sup.generation
	sup.mu.Unlock()

	// Simulate a stale Wait goroutine from a previous child reporting exit.
	sup.childExited(gen - 1)

	if !sup.IsRunning() {
		t.Error("stale childExited must not clear running state of the current child")
	}
}

func TestAutoRestart_PausedDoesNotStart(t *testing.T) {
	sup := testSupervisor(SupervisorConfig{
		BinPath: "/bin/sleep",
		Args:    []string{"60"},
	})
	if err := sup.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer sup.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		sup.AutoRestart(ctx)
		close(done)
	}()

	// Simulate the update handler owning the lifecycle: pause, then stop the
	// child (as apply does before swapping the binary).
	sup.Pause()
	if err := sup.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	// Give AutoRestart several loop iterations — it must not restart the
	// child or count failures while paused.
	time.Sleep(2500 * time.Millisecond)

	if sup.IsRunning() {
		t.Fatal("AutoRestart restarted the child while paused")
	}
	sup.mu.Lock()
	fails := sup.consecutiveFails
	sup.mu.Unlock()
	if fails != 0 {
		t.Errorf("consecutiveFails = %d while paused, want 0", fails)
	}

	// Handler completes its sequence: start the new child, then resume.
	if err := sup.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	pid := sup.PID()
	sup.Resume()

	// AutoRestart resumes monitoring a healthy child — no double-start.
	time.Sleep(1500 * time.Millisecond)
	if !sup.IsRunning() {
		t.Error("child should still be running after resume")
	}
	if sup.PID() != pid {
		t.Errorf("PID changed after resume: got %d, want %d (AutoRestart double-started)", sup.PID(), pid)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AutoRestart did not return after context cancellation")
	}
}

func TestNewSupervisor_Defaults(t *testing.T) {
	sup := NewSupervisor(SupervisorConfig{
		BinPath: "/bin/sleep",
	})

	if sup.config.HealthURL != "http://127.0.0.1:9090/healthz" {
		t.Errorf("HealthURL = %q, want %q", sup.config.HealthURL, "http://127.0.0.1:9090/healthz")
	}
	if sup.config.HealthTimeout != 5*time.Second {
		t.Errorf("HealthTimeout = %v, want 5s", sup.config.HealthTimeout)
	}
	if sup.config.HealthInterval != 10*time.Second {
		t.Errorf("HealthInterval = %v, want 10s", sup.config.HealthInterval)
	}
	if sup.config.HealthRetries != 3 {
		t.Errorf("HealthRetries = %d, want 3", sup.config.HealthRetries)
	}
	if sup.config.DegradedRetryInterval != 10*time.Minute {
		t.Errorf("DegradedRetryInterval = %v, want 10m", sup.config.DegradedRetryInterval)
	}
	if sup.stabilityWindow != 30*time.Second {
		t.Errorf("stabilityWindow = %v, want 30s", sup.stabilityWindow)
	}
}
