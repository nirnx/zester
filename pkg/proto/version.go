package proto

// ProtocolVersion is the current wire-protocol generation for zester's
// msgpack messages. Senders stamp it into the V field of versioned wire
// types (ExecRequest, ExecResponse); a decoded V of 0 identifies a
// pre-versioning sender and must be treated as compatible.
//
// Compatibility policy: schema evolution is additive-only (see the package
// documentation). Adding new `,omitempty` fields does NOT bump this
// constant — old and new binaries interoperate by ignoring unknown keys
// and zero-filling missing ones. Bump ProtocolVersion only for a
// semantically breaking change, i.e. when a peer can no longer behave
// correctly by treating the missing/zero form of a field as valid. Because
// mixed-version fleets are the designed steady state during batched
// self-update rollouts, a bump should be rare and deliberate: rollouts can
// gate on it via the update manifest's minimum-protocol field
// (Manifest.MinProtocol) so an incompatible binary is never rolled onto a
// fleet that cannot speak its protocol.
const ProtocolVersion = 1
