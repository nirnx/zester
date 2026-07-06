package update

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

// NodeStatus represents the current state of a watched node.
type NodeStatus struct {
	ID          string    `msgpack:"id"`
	Component   string    `msgpack:"component"` // "peel" or "master"
	Version     string    `msgpack:"version"`
	State       string    `msgpack:"state"` // running, updating, soak, degraded, offline
	GOOS        string    `msgpack:"goos"`
	GOARCH      string    `msgpack:"goarch"`
	UpdatedAt   time.Time `msgpack:"updated_at"`
	ChildPID    int       `msgpack:"child_pid"`
	ChildUptime string    `msgpack:"child_uptime"`
	// Degraded reports that the node's supervisor is in the degraded
	// slow-retry tier (repeated child restarts failed). Rollout targeting
	// should exclude such nodes.
	Degraded bool `msgpack:"degraded,omitempty"`
	// Protocol is the node's update protocol number, checked against
	// Manifest.MinProtocol before a rollout starts. Additive: nodes running
	// watchdogs that predate the field report 0 (legacy), which fails the
	// min-protocol check only when a manifest sets MinProtocol > 0.
	Protocol int `msgpack:"protocol,omitempty"`
}

// StatusKey returns the KV key for a node status entry.
// Format: <component>.<id>
func StatusKey(component, id string) string {
	return component + "." + id
}

// ReporterConfig configures the status reporter.
type ReporterConfig struct {
	KV        bus.KV
	ID        string
	Component string
	Interval  time.Duration // default: 30s
	Logger    *slog.Logger
	// DegradedFn optionally reports the supervisor's degraded state at
	// report time (wire it to Supervisor.IsDegraded). Nil means never
	// degraded.
	DegradedFn func() bool
	// Protocol is the node's update protocol number, stamped into every
	// NodeStatus report. Zero means legacy/unspecified (see
	// NodeStatus.Protocol for the min-protocol semantics).
	Protocol int
}

// Reporter periodically writes node status to the update-status KV bucket.
type Reporter struct {
	config    ReporterConfig
	logger    *slog.Logger
	versionFn func() string
	stateFn   func() string
	pidFn     func() int
	uptimeFn  func() time.Duration

	consecutiveFailures int
}

// NewReporter creates a status reporter. The callback functions provide
// dynamic values at report time (version from child health, state from handler).
func NewReporter(cfg ReporterConfig, versionFn func() string, stateFn func() string, pidFn func() int, uptimeFn func() time.Duration) *Reporter {
	if cfg.Interval == 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Reporter{
		config:    cfg,
		logger:    cfg.Logger,
		versionFn: versionFn,
		stateFn:   stateFn,
		pidFn:     pidFn,
		uptimeFn:  uptimeFn,
	}
}

// Run starts the periodic status reporting loop. Blocks until ctx is cancelled.
func (r *Reporter) Run(ctx context.Context) {
	r.report(ctx) // initial report
	ticker := time.NewTicker(r.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.report(ctx)
		}
	}
}

func (r *Reporter) report(ctx context.Context) {
	status := NodeStatus{
		ID:          r.config.ID,
		Component:   r.config.Component,
		Version:     r.versionFn(),
		State:       r.stateFn(),
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		UpdatedAt:   time.Now(),
		ChildPID:    r.pidFn(),
		ChildUptime: r.uptimeFn().Round(time.Second).String(),
		Protocol:    r.config.Protocol,
	}
	if r.config.DegradedFn != nil {
		status.Degraded = r.config.DegradedFn()
	}

	key := StatusKey(r.config.Component, r.config.ID)
	if _, err := bus.KVPut(ctx, r.config.KV, key, &status); err != nil {
		r.consecutiveFailures++
		switch {
		case r.consecutiveFailures <= 5:
			r.logger.Warn("failed to report status", "error", err, "consecutive", r.consecutiveFailures)
		case r.consecutiveFailures == 6:
			r.logger.Error("status reporting persistently failing, suppressing further warnings", "error", err, "consecutive", r.consecutiveFailures)
		case r.consecutiveFailures%30 == 0:
			r.logger.Error("status reporting still failing", "error", err, "consecutive", r.consecutiveFailures)
		}
	} else {
		r.consecutiveFailures = 0
	}
}

// GetNodeStatus retrieves a node's status from KV.
func GetNodeStatus(ctx context.Context, kv bus.KV, component, id string) (*NodeStatus, error) {
	var status NodeStatus
	key := StatusKey(component, id)
	if err := bus.KVGet(ctx, kv, key, &status); err != nil {
		return nil, fmt.Errorf("update: get node status %q: %w", key, err)
	}
	return &status, nil
}

// ListNodeStatuses retrieves all node statuses for a component.
func ListNodeStatuses(ctx context.Context, kv bus.KV, component string) ([]*NodeStatus, error) {
	keys, err := kv.Keys(ctx)
	if err != nil {
		if err == bus.ErrNoKeysFound {
			return nil, nil
		}
		return nil, fmt.Errorf("update: list status keys: %w", err)
	}

	prefix := component + "."
	var statuses []*NodeStatus
	for _, key := range keys {
		if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
			continue
		}
		var s NodeStatus
		if err := bus.KVGet(ctx, kv, key, &s); err != nil {
			continue
		}
		statuses = append(statuses, &s)
	}
	return statuses, nil
}
