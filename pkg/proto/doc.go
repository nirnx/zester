// Package proto defines the wire types shared between zester components
// (CLI, master, peels) — most notably ExecRequest and ExecResponse exchanged
// over NATS request/reply and job dispatch subjects.
//
// # Wire compatibility policy (ADDITIVE-ONLY)
//
// Mixed-version fleets are the designed steady state: every batched
// self-update rollout runs old and new binaries side by side for the soak
// period, and MessagePack decoding silently skips unknown keys and
// zero-fills missing ones. A renamed, retyped, or removed field therefore
// never produces a decode error — it silently becomes a zero value on the
// other side (a zeroed Epoch fencing token, an empty Args map, a false
// Success flag). Every change to a type in this package — and to any other
// msgpack-encoded wire or KV struct — MUST follow these rules:
//
//   - NEVER rename, retype, or remove an existing msgpack field name.
//     To retire a field, deprecate it in the godoc and stop reading it;
//     its key name stays reserved forever.
//   - New fields MUST be tagged `,omitempty`, so messages from new senders
//     that do not use the new feature are byte-compatible with old ones.
//   - The zero value of every field MUST be safe for every reader: an old
//     reader ignores keys it does not know, and a new reader receiving a
//     message from an old sender sees the new field as its zero value and
//     must behave correctly (e.g. V == 0 means "pre-versioning sender").
//   - Bump ProtocolVersion ONLY when a change is semantically breaking —
//     when a peer can no longer behave correctly by treating missing
//     fields as zero values. Purely additive changes never bump it.
//     Rollouts can gate on it via the update manifest's minimum-protocol
//     field (Manifest.MinProtocol), refusing to roll a fleet onto a binary
//     whose protocol the current fleet cannot speak.
//
// The conformance test in this package (exec_conformance_test.go) pins the
// exact set of encoded key names for each wire type; an accidental rename
// or removal fails CI with a pointer back to this policy.
package proto
