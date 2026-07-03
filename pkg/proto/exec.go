package proto

import "time"

// ExecRequest is sent from the CLI to a peel via NATS request/reply
// on bus.CmdSubject(peelID). The peel executes the module and replies
// with an ExecResponse.
type ExecRequest struct {
	// V is the sender's ProtocolVersion. 0 means the message was produced
	// by a pre-versioning sender and must be treated as compatible.
	V int `msgpack:"v,omitempty"`

	// JID is the unique job identifier.
	JID string `msgpack:"jid"`

	// Module is the state module to execute (e.g. "cmd.run", "file.managed",
	// "pkg.installed"). The special value "state.apply" loads a state file.
	Module string `msgpack:"module"`

	// ID is the state identifier (e.g. a file path for file.managed,
	// or a state name for state.apply).
	ID string `msgpack:"id"`

	// Args contains module-specific parameters.
	// For cmd.run: {"command": "uptime"}
	// For file.managed: {"content": "...", "mode": "0644"}
	// For state.apply: {"state": "hello"} — loads /data/states/hello/init.zy
	Args map[string]any `msgpack:"args"`

	// Epoch is the KV revision fencing token from job ownership.
	// Peels reject requests with a stale epoch for the same JID,
	// preventing duplicate execution during master failover.
	Epoch uint64 `msgpack:"epoch,omitempty"`

	// ReactorDepth propagates the reactor's reaction chain depth through
	// the job hop: when the master's reactor dispatches a job in response
	// to an event, it records the chain depth in the job's metadata and
	// the dispatch path stamps it here, so an event.send executed BY that
	// job on the peel emits its event with Depth = ReactorDepth. This
	// keeps explicit reaction chains counted across the master→peel hop
	// and bounded by the reactor's max chain depth. 0 means the request
	// was not reactor-spawned (or predates this field) and is always
	// compatible.
	ReactorDepth int `msgpack:"rdepth,omitempty"`
}

// ExecResponse is returned from a peel to the CLI after executing a module.
type ExecResponse struct {
	// V is the sender's ProtocolVersion. 0 means the message was produced
	// by a pre-versioning sender and must be treated as compatible.
	V int `msgpack:"v,omitempty"`

	// PeelID is the responding peel's identifier.
	PeelID string `msgpack:"peel_id"`

	// JID echoes back the request's job identifier.
	JID string `msgpack:"jid"`

	// Success is true when all states executed without error.
	Success bool `msgpack:"success"`

	// Test is true when the run was a dry run (test=True): Results report
	// which states WOULD change rather than what was changed.
	Test bool `msgpack:"test,omitempty"`

	// Results holds per-state execution outcomes.
	Results []StateResult `msgpack:"results"`

	// Error is set when the peel failed before or during execution.
	Error string `msgpack:"error,omitempty"`
}

// StateResult captures the outcome of a single state execution.
// It mirrors state.StateResult but is a concrete type suitable for
// msgpack serialisation across the wire.
type StateResult struct {
	Name       string            `msgpack:"name"`
	Changed    bool              `msgpack:"changed"`
	Diff       string            `msgpack:"diff"`
	Duration   time.Duration     `msgpack:"duration"`
	Details    map[string]string `msgpack:"details"`
	Error      string            `msgpack:"error,omitempty"`
	Skipped    bool              `msgpack:"skipped,omitempty"`
	SkipReason string            `msgpack:"skip_reason,omitempty"`
}
