// Package job provides job lifecycle management for Zester.
// Jobs are dispatched to targeted peels via NATS, tracked with KSUID-based
// job IDs, and their returns are collected and aggregated.
package job

import (
	"strconv"
	"time"

	"github.com/segmentio/ksuid"

	"github.com/ptorbus/zester/pkg/proto"
)

// NewJID generates a new K-Sorted Unique ID for a job.
// KSUIDs are time-ordered and globally unique, making them ideal for
// chronological job tracking without coordination.
func NewJID() string {
	return ksuid.New().String()
}

// defaultJobTimeout is the fallback timeout applied when a job specifies
// none. Used both for the watcher's timer and for computing the dispatch
// deadline in NewJob.
const defaultJobTimeout = 60 * time.Second

// Status represents the lifecycle state of a job.
type Status string

const (
	StatusPending  Status = "pending"
	StatusClaimed  Status = "claimed"
	StatusRunning  Status = "running"
	StatusCanceled Status = "canceled"
	StatusComplete Status = "complete"
	StatusPartial  Status = "partial"
	StatusTimeout  Status = "timeout"
	StatusFailed   Status = "failed"
)

// Job represents a unit of work dispatched to one or more peels.
type Job struct {
	// JID is the unique job identifier (KSUID).
	JID string `msgpack:"jid"`

	// Function is the command or state to execute (e.g., "cmd.run", "state.apply").
	Function string `msgpack:"function"`

	// Args contains the arguments for the function.
	Args map[string]any `msgpack:"args"`

	// Targets is the list of peel IDs that should execute this job.
	Targets []string `msgpack:"targets"`

	// Timeout is the maximum duration to wait for all returns.
	Timeout time.Duration `msgpack:"timeout"`

	// Status is the current lifecycle status.
	Status Status `msgpack:"status"`

	// Created is when the job was dispatched.
	Created time.Time `msgpack:"created"`

	// Updated is when the job status was last modified.
	Updated time.Time `msgpack:"updated"`

	// User is the identity that initiated the job.
	User string `msgpack:"user,omitempty"`

	// Owner is the master instance ID that claimed this job.
	Owner string `msgpack:"owner,omitempty"`

	// Epoch is the KV revision at the time of ownership claim.
	// Used as a fencing token to prevent stale reclaims.
	Epoch uint64 `msgpack:"epoch,omitempty"`

	// Deadline is the wall-clock time by which all returns must be
	// collected (Created + Timeout). Recovered watchers honor the
	// remaining budget instead of restarting the full timeout. NewJob and
	// Manager.Dispatch always set it; a zero value (only producible by a
	// hand-crafted KV record) is defensively derived via
	// normalizeDeadline before use.
	Deadline time.Time `msgpack:"deadline,omitempty"`

	// ReclaimCount is the number of times this job has been reclaimed
	// from a dead master. Bounded by maxReclaims to stop ownership
	// ping-pong; beyond that the job is finalized as failed.
	ReclaimCount int `msgpack:"reclaim_count,omitempty"`

	// StateID carries the bare positional argument from the CLI (the
	// state identifier / conventional "name", e.g. `zester '*'
	// pkg.version nginx` -> "nginx"). Dispatch forwards it as
	// ExecRequest.ID so job-mode runs behave like --direct runs; empty
	// when the operator passed no positional argument.
	StateID string `msgpack:"state_id,omitempty"`

	// TargetExpr is the original targeting expression the operator wrote
	// (e.g. "G@os:ubuntu and web*"), recorded for auditing alongside the
	// resolved Targets list. Populated by the CLI/API at dispatch time;
	// pkg/job itself never interprets it. Empty when the dispatcher did
	// not record one (e.g. scheduler-synthetic jobs).
	TargetExpr string `msgpack:"target_expr,omitempty"`

	// ReturnCount is the number of per-peel returns that had been
	// collected when the job was finalized. Set by finalizeJob; 0 on
	// in-flight records. The per-peel keys in the job-returns bucket
	// ("{jid}.{peelID}") are the sole store for the return payloads — the
	// job record only carries these summary counts (a single aggregated
	// value would cap a job's total returns at the ~1MB NATS payload
	// limit).
	ReturnCount int `msgpack:"return_count,omitempty"`

	// SuccessCount is the number of collected returns that reported
	// success (Success && no Error) at finalize time. See ReturnCount.
	SuccessCount int `msgpack:"success_count,omitempty"`

	// V is the creator's proto.ProtocolVersion. 0 means the field was
	// never set (a hand-crafted record) and must be treated as
	// compatible (schema evolution is additive-only).
	V int `msgpack:"v,omitempty"`

	// Metadata holds arbitrary key-value data for the job.
	Metadata map[string]string `msgpack:"metadata,omitempty"`
}

// NewJob creates a new job with a fresh JID and pending status.
// The dispatch deadline is set to Created + timeout (or the default
// timeout when none is given) so recovered watchers can honor the
// remaining time budget after a master failover.
func NewJob(function string, args map[string]any, targets []string, timeout time.Duration) *Job {
	now := time.Now().UTC()
	effective := timeout
	if effective <= 0 {
		effective = defaultJobTimeout
	}
	return &Job{
		JID:      NewJID(),
		Function: function,
		Args:     args,
		Targets:  targets,
		Timeout:  timeout,
		Status:   StatusPending,
		Created:  now,
		Updated:  now,
		Deadline: now.Add(effective),
		V:        proto.ProtocolVersion,
	}
}

// normalizeDeadline defensively derives a missing Deadline. NewJob and
// Manager.Dispatch always set one, so only a hand-crafted KV record can
// carry a zero deadline; derive it as Created + Timeout (with the
// default-timeout fallback, anchored at now when Created is also unset)
// so watchers and reclaim can rely on a single, always-set deadline path.
func normalizeDeadline(j *Job) {
	if !j.Deadline.IsZero() {
		return
	}
	effective := j.Timeout
	if effective <= 0 {
		effective = defaultJobTimeout
	}
	base := j.Created
	if base.IsZero() {
		base = time.Now().UTC()
	}
	j.Deadline = base.Add(effective)
}

// MetadataReactorDepth is the Job.Metadata key through which the reactor
// threads its reaction chain depth into dispatched ExecRequests: the reactor
// stamps the parent event's depth+1 as a base-10 int string, and every
// ExecRequest built from the job carries it as proto.ExecRequest.ReactorDepth
// (see execRequestForJob). A conventional metadata entry, not schema —
// absent or non-integer values mean depth 0 (not reactor-spawned).
const MetadataReactorDepth = "reactor_depth"

// execRequestForJob builds the wire ExecRequest for one dispatch of j.
// All publish sites (Dispatch, the claimed-job reclaim re-dispatch, and the
// watcher's silent-target re-send) go through this single constructor so
// every copy of the request carries identical identity, fencing, and reactor
// provenance.
func execRequestForJob(j *Job) proto.ExecRequest {
	return proto.ExecRequest{
		JID:          j.JID,
		Module:       j.Function,
		ID:           j.StateID,
		Args:         j.Args,
		Epoch:        j.Epoch,
		V:            proto.ProtocolVersion,
		ReactorDepth: reactorDepth(j),
	}
}

// reactorDepth extracts the reactor chain depth from j.Metadata
// (MetadataReactorDepth). Absent, non-integer, or negative values yield 0:
// a malformed depth must degrade to "not reactor-spawned", never block a
// dispatch.
func reactorDepth(j *Job) int {
	raw, ok := j.Metadata[MetadataReactorDepth]
	if !ok {
		return 0
	}
	depth, err := strconv.Atoi(raw)
	if err != nil || depth < 0 {
		return 0
	}
	return depth
}

// TargetCount returns the number of targeted peels.
func (j *Job) TargetCount() int {
	return len(j.Targets)
}

// IsTerminal returns true if the job is in a final state.
func (j *Job) IsTerminal() bool {
	switch j.Status {
	case StatusComplete, StatusPartial, StatusTimeout, StatusFailed, StatusCanceled:
		return true
	}
	return false
}

// Return represents the execution result from a single peel.
type Return struct {
	// JID is the job this return belongs to.
	JID string `msgpack:"jid"`

	// PeelID identifies which peel produced this return.
	PeelID string `msgpack:"peel_id"`

	// Success is true if the function executed without error.
	Success bool `msgpack:"success"`

	// ReturnData contains the function's output data.
	ReturnData any `msgpack:"return_data"`

	// Error is set when the function failed.
	Error string `msgpack:"error,omitempty"`

	// Duration is how long execution took on the peel.
	Duration time.Duration `msgpack:"duration"`

	// Timestamp is when the return was generated.
	Timestamp time.Time `msgpack:"timestamp"`
}

// Ack represents a peel's acknowledgment that it received a job dispatch.
// Peels publish it (MessagePack-encoded) on bus.JobAckSubject(jid, peelID)
// as soon as they ACCEPT an ExecRequest — before execution starts, so a
// slow job still acks promptly. The dispatch watcher uses acks to
// distinguish "received but still executing" from "never received": acked
// peels are exempt from the ack-window silent-target re-dispatch
// (Watcher.redispatchSilent), which prevents double execution of jobs
// that run longer than the ack window.
type Ack struct {
	// JID is the job being acknowledged.
	JID string `msgpack:"jid"`

	// PeelID identifies which peel is acknowledging.
	PeelID string `msgpack:"peel_id"`

	// Timestamp is when the ack was sent.
	Timestamp time.Time `msgpack:"timestamp"`
}

// Event represents a job lifecycle event for the event stream.
type Event struct {
	// JID is the job this event belongs to.
	JID string `msgpack:"jid"`

	// Type is the event type (dispatched, acked, returned, completed, timeout).
	Type string `msgpack:"type"`

	// PeelID is set for peel-specific events.
	PeelID string `msgpack:"peel_id,omitempty"`

	// Data contains event-specific payload.
	Data any `msgpack:"data,omitempty"`

	// Timestamp is when the event occurred.
	Timestamp time.Time `msgpack:"timestamp"`
}

// MasterHeartbeat is the payload stored in the master-heartbeat KV bucket.
type MasterHeartbeat struct {
	// MasterID uniquely identifies this master instance.
	MasterID string `msgpack:"master_id"`

	// ActiveJobs is the list of JIDs currently watched by this master.
	ActiveJobs []string `msgpack:"active_jobs"`

	// Timestamp is when this heartbeat was generated.
	Timestamp time.Time `msgpack:"timestamp"`
}

// Event type constants.
const (
	EventDispatched = "dispatched"
	EventAcked      = "acked"
	EventReturned   = "returned"
	EventCompleted  = "completed"
	EventCanceled   = "canceled"
	EventTimeout    = "timeout"
	EventFailed     = "failed"
)
