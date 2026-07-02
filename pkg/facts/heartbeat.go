package facts

import "time"

// Heartbeat is the per-peel liveness record written to the peel-heartbeat KV
// bucket (bus.BucketPeelHeartbeat, 30s TTL). The peel daemon Puts it under
// its own peel ID every 10 seconds once NATS is connected; consumers (the
// CLI's `zester peel list`, the master's fleet views) treat a missing or
// expired key as "offline". It lives in pkg/facts — not pkg/bus — because it
// is peel-presence data alongside the facts types, and both the CLI and
// masterd already import this package to decode peel state.
//
// Schema evolution is additive-only (see pkg/proto): new fields must carry
// `,omitempty` so mixed-version fleets keep decoding each other's beats.
type Heartbeat struct {
	// TS is the wall-clock time the beat was written (UTC).
	TS time.Time `msgpack:"ts"`

	// Version is the peel's binary version (internal/version.Version).
	Version string `msgpack:"version,omitempty"`

	// Protocol is the peel's wire-protocol generation
	// (proto.ProtocolVersion) for mixed-version fleet visibility.
	Protocol int `msgpack:"protocol,omitempty"`
}
