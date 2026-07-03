package event_test

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/ptorbus/zester/pkg/event"
	"github.com/ptorbus/zester/pkg/proto"
)

// policyMsg is appended to every conformance failure so the fix is obvious.
const policyMsg = "pkg/event wire types are ADDITIVE-ONLY (see pkg/proto/doc.go): " +
	"never rename/retype/remove a msgpack field name; new fields must be " +
	"`,omitempty` with a zero value that is safe for old readers. If you " +
	"intentionally ADDED a field, populate it in this test's fixture and " +
	"append its key to the golden list."

// fullEvent populates every Event field with a non-zero value so omitempty
// fields are guaranteed to appear on the wire.
func fullEvent() event.Event {
	return event.Event{
		ID:     "2QK5x8mock",
		Tag:    "myco/deploy/finished",
		Data:   map[string]any{"service": "nginx"},
		TS:     time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
		V:      proto.ProtocolVersion,
		Origin: "reaction:reactor.restart_service",
		Depth:  2,
	}
}

// requireAllFieldsSet fails if any struct field of v is its zero value.
// This keeps the fixture honest: a newly added omitempty field left
// unpopulated would silently vanish from the encoded key set.
func requireAllFieldsSet(t *testing.T, v any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		if rv.Field(i).IsZero() {
			t.Fatalf("fixture %s.%s is a zero value — populate it so its wire key is exercised. %s",
				rt.Name(), rt.Field(i).Name, policyMsg)
		}
	}
}

// encodedKeys marshals v with msgpack (same library as bus.Encode) and
// returns the sorted set of top-level map keys it produced.
func encodedKeys(t *testing.T, v any) []string {
	t.Helper()
	data, err := msgpack.Marshal(v)
	if err != nil {
		t.Fatalf("msgpack marshal %T: %v", v, err)
	}
	var m map[string]any
	if err := msgpack.Unmarshal(data, &m); err != nil {
		t.Fatalf("msgpack unmarshal %T into map: %v", v, err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestEventWireKeyConformance pins the exact set of msgpack key names Event
// encodes. Renaming, retyping (to a type that changes the key set), or
// removing a field fails this test; additions require a deliberate golden
// update, which is the moment to re-read the additive-only policy.
func TestEventWireKeyConformance(t *testing.T) {
	ev := fullEvent()
	requireAllFieldsSet(t, ev)
	got := encodedKeys(t, ev)
	want := []string{"data", "depth", "id", "origin", "tag", "ts", "v"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Event encoded key set changed.\n  got:  %v\n  want: %v\n%s", got, want, policyMsg)
	}
}

// TestEventOmitemptyFieldsOmittedWhenZero verifies the omitempty contract:
// an organic, data-less event encodes only the mandatory keys, so old
// readers never see keys they do not know and zero values stay implicit.
func TestEventOmitemptyFieldsOmittedWhenZero(t *testing.T) {
	ev := fullEvent()
	ev.Data = nil
	ev.V = 0
	ev.Origin = ""
	ev.Depth = 0

	got := encodedKeys(t, ev)
	want := []string{"id", "tag", "ts"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Event with zero omitempty fields encoded key set:\n  got:  %v\n  want: %v\n%s", got, want, policyMsg)
	}
}

// TestEventLegacyDecode round-trips a minimal pre-extension message: keys
// absent on the wire must decode to zero values (V == 0 marks a sender that
// never set the field and is always compatible).
func TestEventLegacyDecode(t *testing.T) {
	data, err := msgpack.Marshal(map[string]any{"id": "old-id", "tag": "a/b"})
	if err != nil {
		t.Fatalf("msgpack marshal legacy map: %v", err)
	}
	var decoded event.Event
	if err := msgpack.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("msgpack unmarshal legacy Event: %v", err)
	}
	if decoded.ID != "old-id" || decoded.Tag != "a/b" {
		t.Fatalf("legacy Event decode mismatch: %+v", decoded)
	}
	if decoded.V != 0 || decoded.Depth != 0 || decoded.Origin != "" || decoded.Data != nil {
		t.Fatalf("legacy Event: absent keys must decode to zero values, got %+v", decoded)
	}
}

// TestEventRoundTrip encodes and decodes a fully populated Event and checks
// field fidelity.
func TestEventRoundTrip(t *testing.T) {
	original := fullEvent()
	data, err := msgpack.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded event.Event
	if err := msgpack.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ID != original.ID || decoded.Tag != original.Tag ||
		decoded.V != original.V || decoded.Origin != original.Origin ||
		decoded.Depth != original.Depth {
		t.Errorf("round trip mismatch:\n  got:  %+v\n  want: %+v", decoded, original)
	}
	if !decoded.TS.Equal(original.TS) {
		t.Errorf("TS: got %v, want %v", decoded.TS, original.TS)
	}
	if got := decoded.Data["service"]; got != "nginx" {
		t.Errorf("Data[service]: got %v, want nginx", got)
	}
}
