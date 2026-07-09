package update

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

// Handler states for the update state machine.
const (
	StateIdle        = "idle"
	StatePreparing   = "preparing"
	StateStaged      = "staged"
	StateApplying    = "applying"
	StateSoaking     = "soaking"
	StateConfirmed   = "confirmed"
	StateRollingBack = "rolling_back"
)

// Update command names sent over NATS.
const (
	CmdPrepare  = "prepare"
	CmdApply    = "apply"
	CmdConfirm  = "confirm"
	CmdRollback = "rollback"
	CmdStatus   = "status"
)

// UpdateCommand is received from the rollout controller via NATS request/reply.
type UpdateCommand struct {
	Command   string `msgpack:"command"`
	Version   string `msgpack:"version"`
	Component string `msgpack:"component"`
	SHA256    string `msgpack:"sha256"`
	ObjectKey string `msgpack:"object_key"`
}

// UpdateResponse is sent back to the rollout controller.
type UpdateResponse struct {
	Status  string `msgpack:"status"`
	Version string `msgpack:"version,omitempty"`
	Hash    string `msgpack:"hash,omitempty"`
	Error   string `msgpack:"error,omitempty"`
	State   string `msgpack:"state,omitempty"`
	Uptime  string `msgpack:"uptime,omitempty"`
}

// MinConfirmDeadline is the floor for the soak confirm-deadline: the handler
// never waits less than this for a controller confirm before auto-rolling
// back, regardless of how short the soak time is.
const MinConfirmDeadline = 5 * time.Minute

// HandlerConfig configures the update command handler.
type HandlerConfig struct {
	ID         string
	Component  string
	SoakTime   time.Duration // default: 60s
	PubSub     bus.PubSub
	Slots      *SlotManager
	Supervisor *Supervisor
	Binaries   *BinaryStore
	Logger     *slog.Logger

	// ConfirmDeadline bounds how long the handler stays in StateSoaking
	// waiting for a controller confirm (or rollback) after apply. It is
	// armed on entering StateSoaking; if it expires with neither command
	// received (orphaned rollout — e.g. the driving master died), the
	// handler auto-rolls back to the previous binary, the safe default.
	// Zero means 3× SoakTime with a MinConfirmDeadline floor.
	ConfirmDeadline time.Duration
}

// Handler processes update commands received over NATS.
type Handler struct {
	config HandlerConfig
	logger *slog.Logger

	mu    sync.Mutex
	state string

	// Tracked during update lifecycle
	pendingVersion string
	pendingHash    string

	// soakCancel stops the background soakPeriod goroutine when confirm
	// or rollback arrives from the controller.
	soakCancel context.CancelFunc

	sub bus.Subscription
}

// NewHandler creates an update command handler.
func NewHandler(cfg HandlerConfig) *Handler {
	if cfg.SoakTime == 0 {
		cfg.SoakTime = 60 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Handler{
		config: cfg,
		logger: cfg.Logger,
		state:  StateIdle,
	}
}

// Start subscribes to update commands on the node's subject.
func (h *Handler) Start() error {
	subject := bus.UpdateCmdSubject(h.config.ID)
	sub, err := h.config.PubSub.Subscribe(subject, h.handleMessage)
	if err != nil {
		return fmt.Errorf("update: subscribe to %s: %w", subject, err)
	}
	h.sub = sub
	h.logger.Info("update handler started", "subject", subject)
	return nil
}

// Stop unsubscribes from update commands.
func (h *Handler) Stop() error {
	if h.sub != nil {
		return h.sub.Unsubscribe()
	}
	return nil
}

// State returns the current handler state.
func (h *Handler) State() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

func (h *Handler) handleMessage(msg *bus.Msg) {
	var cmd UpdateCommand
	if err := bus.Decode(msg.Data, &cmd); err != nil {
		h.logger.Error("update: decode command", "error", err)
		h.respond(msg, UpdateResponse{Status: "error", Error: "decode failed"})
		return
	}

	// The command subject is id-only (zester.update.cmd.<id>), and a colocated
	// master and peel watchdog share the node id — BOTH receive every command
	// sent to it. Gate on the command's component so a peel rollout can never
	// swap the master binary (or vice versa). A mismatch is dropped WITHOUT a
	// reply: the colocated watchdog whose component matches is the responder,
	// and an error reply here would race it for the controller's reply inbox.
	if cmd.Component != "" && h.config.Component != "" && cmd.Component != h.config.Component {
		h.logger.Info("update: ignoring command for other component",
			"command", cmd.Command, "command_component", cmd.Component, "component", h.config.Component)
		return
	}
	if cmd.Component == "" {
		h.logger.Warn("update: command carries no component; processing (sender should stamp it)",
			"command", cmd.Command)
	}

	h.logger.Info("update command received", "command", cmd.Command, "version", cmd.Version)

	var resp UpdateResponse
	switch cmd.Command {
	case CmdPrepare:
		resp = h.handlePrepare(cmd)
	case CmdApply:
		resp = h.handleApply(cmd)
	case CmdConfirm:
		resp = h.handleConfirm()
	case CmdRollback:
		resp = h.handleRollback()
	case CmdStatus:
		resp = h.handleStatus()
	default:
		resp = UpdateResponse{Status: "error", Error: fmt.Sprintf("unknown command: %s", cmd.Command)}
	}

	h.respond(msg, resp)
}

func (h *Handler) handlePrepare(cmd UpdateCommand) UpdateResponse {
	h.mu.Lock()
	if h.state != StateIdle && h.state != StateConfirmed {
		state := h.state
		h.mu.Unlock()
		return UpdateResponse{Status: "error", Error: fmt.Sprintf("cannot prepare in state %s", state)}
	}
	h.state = StatePreparing
	h.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	data, err := h.config.Binaries.Download(ctx, cmd.ObjectKey, cmd.SHA256)
	if err != nil {
		h.mu.Lock()
		h.state = StateIdle
		h.mu.Unlock()
		return UpdateResponse{Status: "error", Error: fmt.Sprintf("download: %v", err)}
	}

	if err := h.config.Slots.Stage(data, cmd.SHA256); err != nil {
		h.mu.Lock()
		h.state = StateIdle
		h.mu.Unlock()
		return UpdateResponse{Status: "error", Error: fmt.Sprintf("stage: %v", err)}
	}

	h.mu.Lock()
	h.state = StateStaged
	h.pendingVersion = cmd.Version
	h.pendingHash = cmd.SHA256
	h.mu.Unlock()

	h.logger.Info("binary staged", "version", cmd.Version, "hash", cmd.SHA256)
	return UpdateResponse{Status: "staged", Hash: cmd.SHA256, Version: cmd.Version}
}

func (h *Handler) handleApply(cmd UpdateCommand) UpdateResponse {
	h.mu.Lock()
	if h.state != StateStaged {
		state := h.state
		h.mu.Unlock()
		return UpdateResponse{Status: "error", Error: fmt.Sprintf("cannot apply in state %s", state)}
	}
	h.state = StateApplying
	h.mu.Unlock()

	// Exclude the supervisor's AutoRestart loop while we own the child
	// lifecycle (stop → swap → start) — otherwise it can race in and spawn
	// a second child between our Stop and Start.
	h.config.Supervisor.Pause()
	defer h.config.Supervisor.Resume()

	if err := h.config.Supervisor.Stop(); err != nil {
		h.logger.Warn("failed to stop child cleanly", "error", err)
	}

	if err := h.config.Slots.Apply(); err != nil {
		h.mu.Lock()
		h.state = StateStaged
		h.mu.Unlock()
		// Try restarting the old binary
		_ = h.config.Supervisor.Start()
		return UpdateResponse{Status: "error", Error: fmt.Sprintf("apply: %v", err)}
	}

	if err := h.config.Supervisor.Start(); err != nil {
		h.logger.Error("failed to start new binary, rolling back", "error", err)
		_ = h.config.Slots.Rollback()
		_ = h.config.Supervisor.Start()
		h.mu.Lock()
		h.state = StateIdle
		h.mu.Unlock()
		return UpdateResponse{Status: "error", Error: fmt.Sprintf("start after apply: %v", err)}
	}

	soakCtx, soakCancel := context.WithCancel(context.Background())

	h.mu.Lock()
	h.state = StateSoaking
	h.soakCancel = soakCancel
	version := h.pendingVersion
	h.mu.Unlock()

	// Start soak period in background (state must be set first — soakPeriod
	// may call rollback() immediately on health failure, and concurrent
	// CmdStatus/CmdConfirm must see StateSoaking, not StateApplying).
	go h.soakPeriod(soakCtx)

	return UpdateResponse{Status: "applying", Version: version}
}

// confirmDeadline returns the effective confirm-deadline: the configured
// value, or 3× SoakTime floored at MinConfirmDeadline.
func (h *Handler) confirmDeadline() time.Duration {
	if h.config.ConfirmDeadline > 0 {
		return h.config.ConfirmDeadline
	}
	d := 3 * h.config.SoakTime
	if d < MinConfirmDeadline {
		d = MinConfirmDeadline
	}
	return d
}

func (h *Handler) soakPeriod(ctx context.Context) {
	h.mu.Lock()
	version := h.pendingVersion
	h.mu.Unlock()

	// Confirm-deadline (finding 31): armed on entering StateSoaking. If
	// neither confirm nor rollback arrives from the controller before it
	// fires — the rollout was orphaned, e.g. the driving master died —
	// auto-rollback rather than running an unconfirmed binary forever
	// (an unconfirmed node also rejects all future prepares).
	deadline := time.NewTimer(h.confirmDeadline())
	defer deadline.Stop()

	// Wait for child to become healthy.
	if err := h.config.Supervisor.WaitForHealthy(ctx); err != nil {
		if ctx.Err() != nil {
			return // cancelled by confirm/rollback
		}
		h.logger.Error("health check failed during soak, auto-rolling back", "error", err)
		h.rollback()
		return
	}

	// Soak period: poll readiness (not liveness) for SoakTime. CheckReady
	// validates functional health (e.g. NATS connectivity via /readyz), so a
	// child that is alive but functionally dead fails soak and rolls back.
	// It falls back to CheckHealth when no ReadyURL is configured.
	timer := time.NewTimer(h.config.SoakTime)
	defer timer.Stop()
	ticker := time.NewTicker(h.config.Supervisor.HealthInterval())
	defer ticker.Stop()

	soakFails := 0
	soakPassed := false
	for {
		select {
		case <-ctx.Done():
			return // cancelled by confirm/rollback
		case <-deadline.C:
			h.logger.Error("no confirm or rollback from controller before deadline, auto-rolling back",
				"version", version, "deadline", h.confirmDeadline())
			h.rollback()
			return
		case <-timer.C:
			h.logger.Info("soak period passed, awaiting controller confirm", "version", version)
			soakPassed = true
		case <-ticker.C:
			if soakPassed {
				continue // readiness gates only the soak window itself
			}
			if err := h.config.Supervisor.CheckReady(ctx); err != nil {
				if ctx.Err() != nil {
					return // cancelled by confirm/rollback
				}
				soakFails++
				if soakFails >= h.config.Supervisor.HealthRetries() {
					h.logger.Error("readiness check failed during soak, auto-rolling back", "error", err)
					h.rollback()
					return
				}
				h.logger.Warn("soak readiness check failed", "attempt", soakFails, "max", h.config.Supervisor.HealthRetries(), "error", err)
			} else {
				soakFails = 0
			}
		}
	}
}

func (h *Handler) handleConfirm() UpdateResponse {
	h.mu.Lock()
	if h.state != StateSoaking {
		state := h.state
		h.mu.Unlock()
		return UpdateResponse{Status: "error", Error: fmt.Sprintf("cannot confirm in state %s", state)}
	}
	h.state = StateConfirmed
	if h.soakCancel != nil {
		h.soakCancel()
		h.soakCancel = nil
	}
	version := h.pendingVersion
	h.mu.Unlock()

	_ = h.config.Slots.Confirm()
	h.logger.Info("update confirmed", "version", version)

	return UpdateResponse{Status: "confirmed", Version: version}
}

func (h *Handler) handleRollback() UpdateResponse {
	h.mu.Lock()
	oldState := h.state
	if oldState != StateSoaking && oldState != StateStaged && oldState != StateApplying {
		h.mu.Unlock()
		return UpdateResponse{Status: "error", Error: fmt.Sprintf("cannot rollback in state %s", oldState)}
	}
	h.state = StateRollingBack
	if h.soakCancel != nil {
		h.soakCancel()
		h.soakCancel = nil
	}
	h.mu.Unlock()

	h.rollback()

	return UpdateResponse{Status: "rolled_back"}
}

func (h *Handler) rollback() {
	h.mu.Lock()
	// Guard: don't rollback if state has already moved past soaking
	// (e.g., confirmed by controller or already idle after rollback).
	// StateRollingBack is allowed — handleRollback sets it before calling us.
	if h.state == StateConfirmed || h.state == StateIdle {
		h.mu.Unlock()
		return
	}
	h.state = StateRollingBack
	h.mu.Unlock()

	// Exclude AutoRestart while we own the child lifecycle (see handleApply).
	h.config.Supervisor.Pause()
	defer h.config.Supervisor.Resume()

	if err := h.config.Supervisor.Stop(); err != nil {
		h.logger.Warn("failed to stop child during rollback", "error", err)
	}

	if err := h.config.Slots.Rollback(); err != nil {
		h.logger.Error("rollback failed", "error", err)
	}

	if err := h.config.Supervisor.Start(); err != nil {
		h.logger.Error("failed to restart after rollback", "error", err)
	}

	h.mu.Lock()
	h.state = StateIdle
	h.pendingVersion = ""
	h.pendingHash = ""
	h.mu.Unlock()

	h.logger.Info("rollback completed")
}

func (h *Handler) handleStatus() UpdateResponse {
	h.mu.Lock()
	state := h.state
	version := h.pendingVersion
	h.mu.Unlock()

	return UpdateResponse{
		Status:  "ok",
		State:   state,
		Version: version,
		Uptime:  h.config.Supervisor.Uptime().Round(time.Second).String(),
	}
}

func (h *Handler) respond(msg *bus.Msg, resp UpdateResponse) {
	data, err := bus.Encode(&resp)
	if err != nil {
		h.logger.Error("update: encode response", "error", err)
		return
	}
	if err := msg.Respond(data); err != nil {
		h.logger.Error("update: send response", "error", err)
	}
}
