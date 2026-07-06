// Package event defines the wire type and the subject/tag conventions for
// Zester's reactor event system.
//
// An event is a msgpack Event published on a subject under "zester.event."
// and captured durably by the "events" JetStream stream. The SUBJECT is
// authoritative for identity and routing: the origin (a peel ID, or the
// trusted "_master"/"_admin" tokens) and the tag are derived from subject
// tokens, which the NATS server enforces via publish permissions. The
// payload is untrusted data — consumers must cross-check Event.Tag against
// the subject-derived tag and drop mismatches.
//
// Wire evolution is additive-only per the pkg/proto policy: never rename,
// retype, or remove a msgpack field (retired keys stay reserved forever);
// new fields must be `,omitempty` with zero values that are safe for old
// and new readers. The encoded key set is pinned by a conformance test.
package event

import (
	"time"

	"github.com/segmentio/ksuid"

	"github.com/nirnx/zester/pkg/proto"
)

// Event is the wire struct for every message on the zester.event.>
// namespace. Additive-only msgpack; the key set is pinned by
// TestEventWireKeyConformance.
type Event struct {
	// ID is the publisher-minted KSUID — the dedup anchor: reaction JIDs
	// and derived-event IDs are content-addressed from it.
	ID string `msgpack:"id"`

	// Tag is the slash tag (e.g. "myco/deploy/finished"). It MUST equal
	// the subject-derived tag or consumers drop the event (anti-spoof).
	Tag string `msgpack:"tag"`

	// Data is the UNTRUSTED payload attached by the publisher.
	Data map[string]any `msgpack:"data,omitempty"`

	// TS is the publisher's timestamp (untrusted; used for staleness gating).
	TS time.Time `msgpack:"ts"`

	// V is the wire-protocol generation (proto.ProtocolVersion). A decoded
	// 0 means the field was never set and MUST be treated as compatible.
	V int `msgpack:"v,omitempty"`

	// Origin is "" for organic events and "reaction:<rule-ref>" for events
	// emitted by a reactor rule's event.send action (provenance).
	Origin string `msgpack:"origin,omitempty"`

	// Depth is the reaction chain depth (loop guard): 0 for organic events,
	// parent+1 for each reactor-derived hop.
	Depth int `msgpack:"depth,omitempty"`
}

// NewEvent mints a new organic event with a fresh KSUID ID, the current
// time, and the current protocol version. tag is the slash tag (validate
// with ValidateTag before publishing); data may be nil.
func NewEvent(tag string, data map[string]any) Event {
	return Event{
		ID:   ksuid.New().String(),
		Tag:  tag,
		Data: data,
		TS:   time.Now().UTC(),
		V:    proto.ProtocolVersion,
	}
}
