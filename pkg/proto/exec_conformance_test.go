package proto_test

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/ptorbus/zester/pkg/proto"
)

// policyMsg is appended to every conformance failure so the fix is obvious.
const policyMsg = "pkg/proto wire types are ADDITIVE-ONLY (see pkg/proto/doc.go): " +
	"never rename/retype/remove a msgpack field name; new fields must be " +
	"`,omitempty` with a zero value that is safe for old readers. If you " +
	"intentionally ADDED a field, populate it in this test's fixture and " +
	"append its key to the golden list."

// fullStateResult populates every StateResult field with a non-zero value so
// omitempty fields are guaranteed to appear on the wire.
func fullStateResult() proto.StateResult {
	return proto.StateResult{
		Name:       "file.managed:/etc/motd",
		Changed:    true,
		Diff:       "+hello",
		Duration:   1500 * time.Millisecond,
		Details:    map[string]string{"mode": "0644"},
		Error:      "boom",
		Skipped:    true,
		SkipReason: "onlyif",
	}
}

func fullExecRequest() proto.ExecRequest {
	return proto.ExecRequest{
		V:      proto.ProtocolVersion,
		JID:    "jid-123",
		Module: "cmd.run",
		ID:     "uptime",
		Args:   map[string]any{"command": "uptime"},
		Epoch:  7,
	}
}

func fullExecResponse() proto.ExecResponse {
	return proto.ExecResponse{
		V:       proto.ProtocolVersion,
		PeelID:  "peel-1",
		JID:     "jid-123",
		Success: true,
		Test:    true,
		Results: []proto.StateResult{fullStateResult()},
		Error:   "boom",
	}
}

// requireAllFieldsSet fails if any struct field of v is its zero value.
// This keeps the fixtures honest: a newly added omitempty field that is
// left unpopulated would silently vanish from the encoded key set, so we
// force every field to be set before checking the golden list.
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

func assertGoldenKeys(t *testing.T, typeName string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s encoded key set changed.\n  got:  %v\n  want: %v\n%s",
			typeName, got, want, policyMsg)
	}
}

// TestWireKeyConformance pins the exact set of msgpack key names each wire
// type encodes. Renaming, retyping (to a type that changes the key set), or
// removing a field fails this test; additions require a deliberate golden
// update, which is the moment to re-read the additive-only policy.
func TestWireKeyConformance(t *testing.T) {
	t.Run("ExecRequest", func(t *testing.T) {
		req := fullExecRequest()
		requireAllFieldsSet(t, req)
		assertGoldenKeys(t, "ExecRequest", encodedKeys(t, req), []string{
			"args", "epoch", "id", "jid", "module", "v",
		})
	})

	t.Run("ExecResponse", func(t *testing.T) {
		res := fullExecResponse()
		requireAllFieldsSet(t, res)
		assertGoldenKeys(t, "ExecResponse", encodedKeys(t, res), []string{
			"error", "jid", "peel_id", "results", "success", "test", "v",
		})
	})

	t.Run("StateResult", func(t *testing.T) {
		sr := fullStateResult()
		requireAllFieldsSet(t, sr)
		assertGoldenKeys(t, "StateResult", encodedKeys(t, sr), []string{
			"changed", "details", "diff", "duration", "error", "name",
			"skip_reason", "skipped",
		})
	})

	// The nested StateResult inside ExecResponse.Results must encode with
	// the same key set as a top-level one.
	t.Run("ExecResponse/nested StateResult", func(t *testing.T) {
		data, err := msgpack.Marshal(fullExecResponse())
		if err != nil {
			t.Fatalf("msgpack marshal ExecResponse: %v", err)
		}
		var m map[string]any
		if err := msgpack.Unmarshal(data, &m); err != nil {
			t.Fatalf("msgpack unmarshal ExecResponse into map: %v", err)
		}
		results, ok := m["results"].([]any)
		if !ok || len(results) != 1 {
			t.Fatalf("ExecResponse results: expected 1-element array, got %T %v", m["results"], m["results"])
		}
		nested, ok := results[0].(map[string]any)
		if !ok {
			t.Fatalf("nested StateResult: expected map[string]any, got %T", results[0])
		}
		keys := make([]string, 0, len(nested))
		for k := range nested {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		assertGoldenKeys(t, "nested StateResult", keys, []string{
			"changed", "details", "diff", "duration", "error", "name",
			"skip_reason", "skipped",
		})
	})
}

// TestVersionFieldOmittedWhenZero verifies the omitempty contract on V:
// pre-versioning senders (and zero-valued fixtures) must not emit "v", and
// decoding a message without "v" must yield V == 0, the documented marker
// for a pre-versioning sender.
func TestVersionFieldOmittedWhenZero(t *testing.T) {
	req := fullExecRequest()
	req.V = 0
	for _, k := range encodedKeys(t, req) {
		if k == "v" {
			t.Fatalf("ExecRequest with V=0 encoded a \"v\" key; it must be omitted so "+
				"unversioned and versioned senders interoperate. %s", policyMsg)
		}
	}

	res := fullExecResponse()
	res.V = 0
	for _, k := range encodedKeys(t, res) {
		if k == "v" {
			t.Fatalf("ExecResponse with V=0 encoded a \"v\" key; it must be omitted so "+
				"unversioned and versioned senders interoperate. %s", policyMsg)
		}
	}

	// Round-trip a pre-versioning message: no "v" key on the wire → V == 0.
	data, err := msgpack.Marshal(map[string]any{"jid": "old", "module": "test.ping"})
	if err != nil {
		t.Fatalf("msgpack marshal legacy map: %v", err)
	}
	var decoded proto.ExecRequest
	if err := msgpack.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("msgpack unmarshal legacy ExecRequest: %v", err)
	}
	if decoded.V != 0 {
		t.Fatalf("legacy ExecRequest decoded V=%d, want 0 (pre-versioning sender marker)", decoded.V)
	}
	if decoded.JID != "old" || decoded.Module != "test.ping" {
		t.Fatalf("legacy ExecRequest decode mismatch: %+v", decoded)
	}
}
