package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sha256hex returns the hex-encoded SHA-256 digest of data.
func sha256hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// setupHandlerTest creates a Handler wired to an in-memory BinaryStore and a
// SlotManager rooted in t.TempDir(). The Supervisor is configured to run
// /bin/sleep so Start() succeeds, but the health URL points to an unreachable
// port so health checks always fail (driving auto-rollback in soak tests).
func setupHandlerTest(t *testing.T) (*Handler, *fakeObjectStore) {
	t.Helper()
	return setupHandlerTestURLs(t, "http://127.0.0.1:19999/healthz" /* unreachable */, "")
}

// setupHandlerTestURLs is setupHandlerTest with explicit liveness (healthURL)
// and readiness (readyURL) endpoints for the supervisor.
func setupHandlerTestURLs(t *testing.T, healthURL, readyURL string) (*Handler, *fakeObjectStore) {
	t.Helper()
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "child")
	if err := os.WriteFile(binPath, []byte("current-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	slots := NewSlotManager(binPath)

	supervisor := NewSupervisor(SupervisorConfig{
		BinPath:        "/bin/sleep",
		Args:           []string{"3600"},
		HealthURL:      healthURL,
		ReadyURL:       readyURL,
		HealthTimeout:  50 * time.Millisecond,
		HealthInterval: 50 * time.Millisecond,
		HealthRetries:  1,
		Stdout:         io.Discard,
		Stderr:         io.Discard,
	})

	objStore := newFakeObjectStore()
	binaries := NewBinaryStore(objStore)

	h := NewHandler(HandlerConfig{
		ID:         "test-01",
		Component:  "peel",
		SoakTime:   200 * time.Millisecond,
		Slots:      slots,
		Supervisor: supervisor,
		Binaries:   binaries,
	})

	// Always stop the supervisor (and any child process) when the test ends.
	t.Cleanup(func() {
		_ = supervisor.Stop()
	})

	return h, objStore
}

// uploadBinary puts data into the fake object store and returns the key and
// its SHA-256 hex digest.
func uploadBinary(t *testing.T, objStore *fakeObjectStore, key string, data []byte) string {
	t.Helper()
	hash := sha256hex(data)
	if _, err := objStore.PutBytes(context.Background(), key, data); err != nil {
		t.Fatalf("uploadBinary: %v", err)
	}
	return hash
}

// prepareBinary uploads a binary and calls handlePrepare, failing the test on
// any error response. Returns the hash.
func prepareBinary(t *testing.T, h *Handler, objStore *fakeObjectStore) string {
	t.Helper()
	data := []byte("new-binary-v2")
	hash := uploadBinary(t, objStore, "peel/linux/amd64/v2.0.0", data)
	cmd := UpdateCommand{
		Command:   CmdPrepare,
		ObjectKey: "peel/linux/amd64/v2.0.0",
		SHA256:    hash,
		Version:   "v2.0.0",
	}
	resp := h.handlePrepare(cmd)
	if resp.Status != "staged" {
		t.Fatalf("prepare failed: %s", resp.Error)
	}
	return hash
}

// TestHandler_State_InitiallyIdle verifies the handler starts in StateIdle.
func TestHandler_State_InitiallyIdle(t *testing.T) {
	h, _ := setupHandlerTest(t)
	if got := h.State(); got != StateIdle {
		t.Errorf("initial state: got %q, want %q", got, StateIdle)
	}
}

// TestHandler_Prepare_Success verifies a valid prepare transitions to StateStaged.
func TestHandler_Prepare_Success(t *testing.T) {
	h, objStore := setupHandlerTest(t)
	data := []byte("new-binary-v2")
	hash := uploadBinary(t, objStore, "peel/linux/amd64/v2.0.0", data)

	cmd := UpdateCommand{
		Command:   CmdPrepare,
		ObjectKey: "peel/linux/amd64/v2.0.0",
		SHA256:    hash,
		Version:   "v2.0.0",
	}
	resp := h.handlePrepare(cmd)

	if resp.Status != "staged" {
		t.Errorf("status: got %q, want %q; error: %s", resp.Status, "staged", resp.Error)
	}
	if h.State() != StateStaged {
		t.Errorf("state: got %q, want %q", h.State(), StateStaged)
	}
	if _, err := os.Stat(h.config.Slots.stagingPath()); os.IsNotExist(err) {
		t.Error("staging file does not exist after prepare")
	}
}

// TestHandler_Prepare_InvalidState verifies prepare is rejected outside idle/confirmed.
func TestHandler_Prepare_InvalidState(t *testing.T) {
	h, _ := setupHandlerTest(t)

	h.mu.Lock()
	h.state = StateSoaking
	h.mu.Unlock()

	cmd := UpdateCommand{Command: CmdPrepare, ObjectKey: "key", SHA256: "abc", Version: "v1"}
	resp := h.handlePrepare(cmd)

	if resp.Status != "error" {
		t.Errorf("expected error status, got %q", resp.Status)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
	// State should be unchanged
	if h.State() != StateSoaking {
		t.Errorf("state changed unexpectedly to %q", h.State())
	}
}

// TestHandler_Prepare_BadHash verifies a hash mismatch returns an error and
// resets state to idle.
func TestHandler_Prepare_BadHash(t *testing.T) {
	h, objStore := setupHandlerTest(t)
	data := []byte("new-binary-v2")
	uploadBinary(t, objStore, "peel/linux/amd64/v2.0.0", data)

	cmd := UpdateCommand{
		Command:   CmdPrepare,
		ObjectKey: "peel/linux/amd64/v2.0.0",
		SHA256:    "0000000000000000000000000000000000000000000000000000000000000000",
		Version:   "v2.0.0",
	}
	resp := h.handlePrepare(cmd)

	if resp.Status != "error" {
		t.Errorf("expected error status, got %q", resp.Status)
	}
	if h.State() != StateIdle {
		t.Errorf("state after bad hash: got %q, want %q", h.State(), StateIdle)
	}
}

// TestHandler_Apply_RequiresStaged verifies apply is rejected when not in staged state.
func TestHandler_Apply_RequiresStaged(t *testing.T) {
	h, _ := setupHandlerTest(t)

	resp := h.handleApply(UpdateCommand{Command: CmdApply})

	if resp.Status != "error" {
		t.Errorf("expected error status, got %q", resp.Status)
	}
	if h.State() != StateIdle {
		t.Errorf("state: got %q, want %q", h.State(), StateIdle)
	}
}

// TestHandler_Apply_Success verifies apply swaps the binary and enters soaking.
// The soak period will auto-rollback (health URL unreachable) — that's fine
// for this test since we only check the immediate post-apply state.
func TestHandler_Apply_Success(t *testing.T) {
	h, objStore := setupHandlerTest(t)
	data := []byte("new-binary-v2")
	hash := prepareBinary(t, h, objStore)
	_ = hash

	resp := h.handleApply(UpdateCommand{Command: CmdApply})

	if resp.Status != "applying" {
		t.Errorf("status: got %q, want %q; error: %s", resp.Status, "applying", resp.Error)
	}

	// State is set to StateSoaking synchronously before returning.
	if got := h.State(); got != StateSoaking {
		t.Errorf("state after apply: got %q, want %q", got, StateSoaking)
	}

	// The binary should have been swapped — current file now contains new data.
	current, err := os.ReadFile(h.config.Slots.currentPath())
	if err != nil {
		t.Fatalf("read current binary: %v", err)
	}
	if string(current) != string(data) {
		t.Errorf("current binary content: got %q, want %q", current, data)
	}

	// Wait for soakPeriod auto-rollback (health check fails → rollback → idle).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.State() == StateIdle {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestHandler_Confirm_RequiresSoaking verifies confirm is rejected outside soaking.
func TestHandler_Confirm_RequiresSoaking(t *testing.T) {
	h, _ := setupHandlerTest(t)

	// From idle
	resp := h.handleConfirm()
	if resp.Status != "error" {
		t.Errorf("idle: expected error status, got %q", resp.Status)
	}

	// From staged
	h.mu.Lock()
	h.state = StateStaged
	h.mu.Unlock()

	resp = h.handleConfirm()
	if resp.Status != "error" {
		t.Errorf("staged: expected error status, got %q", resp.Status)
	}
}

// TestHandler_Confirm_Success verifies confirm from soaking transitions to confirmed.
func TestHandler_Confirm_Success(t *testing.T) {
	h, _ := setupHandlerTest(t)

	// Force into soaking state directly (internal test privilege).
	h.mu.Lock()
	h.state = StateSoaking
	h.pendingVersion = "v2.0.0"
	h.mu.Unlock()

	resp := h.handleConfirm()

	if resp.Status != "confirmed" {
		t.Errorf("status: got %q, want %q; error: %s", resp.Status, "confirmed", resp.Error)
	}
	if h.State() != StateConfirmed {
		t.Errorf("state: got %q, want %q", h.State(), StateConfirmed)
	}
	if resp.Version != "v2.0.0" {
		t.Errorf("version: got %q, want %q", resp.Version, "v2.0.0")
	}
}

// TestHandler_Rollback_FromSoaking verifies handleRollback returns rolled_back
// when called in soaking state and transitions to idle. The binary swap is
// verified by staging + applying and then checking the rollback effect.
// To avoid a race with the soakPeriod auto-rollback goroutine, we force the
// handler into soaking state directly (internal test privilege) after setting
// up the slots via a real prepare+apply cycle on a stopped supervisor.
func TestHandler_Rollback_FromSoaking(t *testing.T) {
	h, objStore := setupHandlerTest(t)
	origContent := []byte("current-binary")

	// Stage the new binary and manually apply the slot swap (bypass supervisor).
	data := []byte("new-binary-v2")
	hash := uploadBinary(t, objStore, "peel/linux/amd64/v2.0.0", data)
	if err := h.config.Slots.Stage(data, hash); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := h.config.Slots.Apply(); err != nil {
		t.Fatalf("apply slots: %v", err)
	}

	// Force state to soaking without actually running soakPeriod.
	h.mu.Lock()
	h.state = StateSoaking
	h.pendingVersion = "v2.0.0"
	h.pendingHash = hash
	h.mu.Unlock()

	rollResp := h.handleRollback()
	if rollResp.Status != "rolled_back" {
		t.Errorf("rollback status: got %q, want %q; error: %s", rollResp.Status, "rolled_back", rollResp.Error)
	}

	// Wait for rollback() to complete (it's synchronous inside handleRollback).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.State() == StateIdle {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if h.State() != StateIdle {
		t.Errorf("state after rollback: got %q, want %q", h.State(), StateIdle)
	}

	// rollback() calls Supervisor.Start() (starts /bin/sleep) so t.Cleanup stops it.

	current, err := os.ReadFile(h.config.Slots.currentPath())
	if err != nil {
		t.Fatalf("read current binary after rollback: %v", err)
	}
	if string(current) != string(origContent) {
		t.Errorf("current binary after rollback: got %q, want %q", current, origContent)
	}
}

// TestHandler_Rollback_FromStaged verifies rollback from staged returns to idle.
func TestHandler_Rollback_FromStaged(t *testing.T) {
	h, objStore := setupHandlerTest(t)
	prepareBinary(t, h, objStore)

	if h.State() != StateStaged {
		t.Fatalf("precondition: state should be staged, got %q", h.State())
	}

	resp := h.handleRollback()

	if resp.Status != "rolled_back" {
		t.Errorf("status: got %q, want %q; error: %s", resp.Status, "rolled_back", resp.Error)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if h.State() == StateIdle {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if h.State() != StateIdle {
		t.Errorf("state: got %q, want %q", h.State(), StateIdle)
	}
}

// TestHandler_Rollback_InvalidState verifies rollback is rejected from idle.
func TestHandler_Rollback_InvalidState(t *testing.T) {
	h, _ := setupHandlerTest(t)

	resp := h.handleRollback()

	if resp.Status != "error" {
		t.Errorf("expected error status, got %q", resp.Status)
	}
	if h.State() != StateIdle {
		t.Errorf("state changed unexpectedly: got %q", h.State())
	}
}

// TestHandler_Status verifies handleStatus returns the current state and uptime.
func TestHandler_Status(t *testing.T) {
	h, _ := setupHandlerTest(t)

	resp := h.handleStatus()

	if resp.Status != "ok" {
		t.Errorf("status: got %q, want %q", resp.Status, "ok")
	}
	if resp.State == "" {
		t.Error("State field should not be empty")
	}
	if resp.Uptime == "" {
		t.Error("Uptime field should not be empty")
	}
}

// TestHandler_Soak_ReadyFailureRollsBackWhileLivenessOK verifies the soak
// phase polls readiness, not liveness: a child that stays alive (healthz 200)
// but is functionally dead (readyz 503, e.g. NATS-disconnected) must fail
// soak and auto-rollback. WaitForHealthy — a liveness decision — passes.
func TestHandler_Soak_ReadyFailureRollsBackWhileLivenessOK(t *testing.T) {
	healthy := statusServer(t, http.StatusOK, "ok")
	notReady := statusServer(t, http.StatusServiceUnavailable, "down")

	h, objStore := setupHandlerTestURLs(t, healthy.URL, notReady.URL)
	prepareBinary(t, h, objStore)

	resp := h.handleApply(UpdateCommand{Command: CmdApply})
	if resp.Status != "applying" {
		t.Fatalf("apply status: got %q, want %q; error: %s", resp.Status, "applying", resp.Error)
	}

	// WaitForHealthy passes (liveness OK), soak starts, the readiness ticker
	// fails, and the handler auto-rolls back to idle.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.State() == StateIdle {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.State() != StateIdle {
		t.Fatalf("state after ready-failing soak: got %q, want %q (auto-rollback)", h.State(), StateIdle)
	}

	// Liveness stayed OK the whole time — the rollback was driven purely by
	// the readiness signal.
	if err := h.config.Supervisor.CheckHealth(context.Background()); err != nil {
		t.Errorf("CheckHealth() should still pass after rollback: %v", err)
	}

	// The binary was rolled back to the original content.
	current, err := os.ReadFile(h.config.Slots.currentPath())
	if err != nil {
		t.Fatalf("read current binary after rollback: %v", err)
	}
	if string(current) != "current-binary" {
		t.Errorf("current binary after rollback: got %q, want %q", current, "current-binary")
	}
}

// TestHandler_Soak_ReadySuccessNoRollback verifies that when readiness stays
// healthy, the soak period completes without rollback and the handler remains
// in soaking (awaiting controller confirm).
func TestHandler_Soak_ReadySuccessNoRollback(t *testing.T) {
	healthy := statusServer(t, http.StatusOK, "ok")

	h, objStore := setupHandlerTestURLs(t, healthy.URL, healthy.URL)
	prepareBinary(t, h, objStore)

	resp := h.handleApply(UpdateCommand{Command: CmdApply})
	if resp.Status != "applying" {
		t.Fatalf("apply status: got %q, want %q; error: %s", resp.Status, "applying", resp.Error)
	}

	// SoakTime is 200ms; wait well past it. A rollback would move the state
	// to idle — it must stay soaking.
	time.Sleep(600 * time.Millisecond)
	if got := h.State(); got != StateSoaking {
		t.Errorf("state after healthy soak: got %q, want %q (no rollback)", got, StateSoaking)
	}
}

// TestHandler_ConfirmDeadline_Defaults verifies the effective confirm-deadline
// computation: 3× SoakTime floored at MinConfirmDeadline, with an explicit
// ConfirmDeadline overriding both.
func TestHandler_ConfirmDeadline_Defaults(t *testing.T) {
	h := NewHandler(HandlerConfig{SoakTime: time.Second})
	if got := h.confirmDeadline(); got != MinConfirmDeadline {
		t.Errorf("short soak: got %v, want floor %v", got, MinConfirmDeadline)
	}

	h = NewHandler(HandlerConfig{SoakTime: 3 * time.Minute})
	if got := h.confirmDeadline(); got != 9*time.Minute {
		t.Errorf("long soak: got %v, want %v", got, 9*time.Minute)
	}

	h = NewHandler(HandlerConfig{SoakTime: time.Second, ConfirmDeadline: 42 * time.Second})
	if got := h.confirmDeadline(); got != 42*time.Second {
		t.Errorf("explicit: got %v, want %v", got, 42*time.Second)
	}
}

// TestHandler_ConfirmDeadline_AutoRollback verifies finding 31 (watchdog
// side): a node left in StateSoaking with neither confirm nor rollback from
// the controller auto-rolls back once the confirm-deadline expires, instead
// of running an unconfirmed binary (and rejecting future updates) forever.
func TestHandler_ConfirmDeadline_AutoRollback(t *testing.T) {
	healthy := statusServer(t, http.StatusOK, "ok")

	h, objStore := setupHandlerTestURLs(t, healthy.URL, healthy.URL)
	h.config.ConfirmDeadline = 400 * time.Millisecond // SoakTime is 200ms

	prepareBinary(t, h, objStore)
	resp := h.handleApply(UpdateCommand{Command: CmdApply})
	if resp.Status != "applying" {
		t.Fatalf("apply status: got %q, want %q; error: %s", resp.Status, "applying", resp.Error)
	}

	// Soak passes (readiness healthy), no confirm arrives, the deadline
	// fires, and the handler auto-rolls back to idle.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.State() == StateIdle {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.State() != StateIdle {
		t.Fatalf("state after confirm-deadline: got %q, want %q (auto-rollback)", h.State(), StateIdle)
	}

	current, err := os.ReadFile(h.config.Slots.currentPath())
	if err != nil {
		t.Fatalf("read current binary after rollback: %v", err)
	}
	if string(current) != "current-binary" {
		t.Errorf("current binary after deadline rollback: got %q, want %q", current, "current-binary")
	}
}

// TestHandler_ConfirmDeadline_ConfirmCancels verifies that a confirm arriving
// before the deadline cancels the auto-rollback.
func TestHandler_ConfirmDeadline_ConfirmCancels(t *testing.T) {
	healthy := statusServer(t, http.StatusOK, "ok")

	h, objStore := setupHandlerTestURLs(t, healthy.URL, healthy.URL)
	h.config.ConfirmDeadline = 800 * time.Millisecond // SoakTime is 200ms

	prepareBinary(t, h, objStore)
	resp := h.handleApply(UpdateCommand{Command: CmdApply})
	if resp.Status != "applying" {
		t.Fatalf("apply status: got %q, want %q; error: %s", resp.Status, "applying", resp.Error)
	}

	// Let the soak window pass, then confirm before the deadline.
	time.Sleep(400 * time.Millisecond)
	if got := h.State(); got != StateSoaking {
		t.Fatalf("precondition: got %q, want %q", got, StateSoaking)
	}
	confirmResp := h.handleConfirm()
	if confirmResp.Status != "confirmed" {
		t.Fatalf("confirm status: got %q, want %q; error: %s", confirmResp.Status, "confirmed", confirmResp.Error)
	}

	// Wait past the original deadline: no rollback may fire.
	time.Sleep(800 * time.Millisecond)
	if got := h.State(); got != StateConfirmed {
		t.Errorf("state after confirm: got %q, want %q (no deadline rollback)", got, StateConfirmed)
	}

	current, err := os.ReadFile(h.config.Slots.currentPath())
	if err != nil {
		t.Fatalf("read current binary: %v", err)
	}
	if string(current) != "new-binary-v2" {
		t.Errorf("current binary after confirm: got %q, want %q", current, "new-binary-v2")
	}
}
