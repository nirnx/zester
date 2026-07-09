// Package fileserver defines the wire types for the operator-triggered file
// republish (`zester fileserver update`, Salt's `fileserver.update` parity).
//
// The request is answered ONLY by the master currently holding the publisher
// lease (non-holders stay silent so the requester never races two replies);
// a no-responders timeout therefore means "no lease holder reachable".
// Msgpack, additive-only per the pkg/proto policy.
package fileserver

// UpdateRequest asks the publisher-lease holder to re-walk the on-disk
// settings/state/reactor trees and publish them to KV.
type UpdateRequest struct {
	// Force bypasses the publishers' hash-gate: every file key, manifest,
	// and _revision is rewritten even when nothing changed. The default
	// (false) publishes only sets whose manifest actually differs. Force is
	// the heal path for a bucket whose file keys were tampered with or torn
	// while its manifest stayed intact.
	Force bool `msgpack:"force,omitempty"`
}

// SetResult reports one file set's publish outcome.
type SetResult struct {
	// Name is the file set: "settings", "states", or "reactor".
	Name string `msgpack:"name"`

	// Files is the size of the on-disk file set.
	Files int `msgpack:"files"`

	// Changed reports whether anything was written (false = hash-gate skip).
	Changed bool `msgpack:"changed"`

	// Err carries a per-set publish failure; the other sets still publish.
	Err string `msgpack:"err,omitempty"`

	// Skipped names why the set was not published at all (e.g.
	// "gitfs-managed": GitFS owns state publishing — its clone-validity gate
	// must never be bypassed by on-demand or watcher/ticker publishes).
	Skipped string `msgpack:"skipped,omitempty"`
}

// UpdateResponse is the lease holder's reply.
type UpdateResponse struct {
	// Master is the answering master's coordination ID.
	Master string `msgpack:"master"`

	// Sets are the per-set outcomes, in settings/states/reactor order.
	Sets []SetResult `msgpack:"sets"`
}

// StatusRequest asks who currently holds the publisher lease. Answered only
// by the holder (same silent-non-holder contract as UpdateRequest).
type StatusRequest struct{}

// StatusResponse identifies the publisher-lease holder in human terms — the
// lease record itself carries only the ephemeral master KSUID, so the holder
// self-reports its hostname.
type StatusResponse struct {
	// Master is the holder's coordination ID (master-<ksuid>, ephemeral).
	Master string `msgpack:"master"`

	// Hostname is the holder's machine hostname — the box to edit files on.
	Hostname string `msgpack:"hostname"`

	// SinceUnix is when this master acquired the lease (Unix seconds, UTC).
	SinceUnix int64 `msgpack:"since_unix,omitempty"`
}
