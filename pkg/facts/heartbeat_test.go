package facts

import (
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

func TestHeartbeatEncodeRoundTrip(t *testing.T) {
	in := Heartbeat{
		TS:       time.Date(2026, 7, 1, 12, 30, 45, 0, time.UTC),
		Version:  "v0.9.1",
		Protocol: 1,
	}

	data, err := bus.Encode(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	var out Heartbeat
	if err := bus.Decode(data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !out.TS.Equal(in.TS) {
		t.Errorf("TS = %v, want %v", out.TS, in.TS)
	}
	if out.Version != in.Version {
		t.Errorf("Version = %q, want %q", out.Version, in.Version)
	}
	if out.Protocol != in.Protocol {
		t.Errorf("Protocol = %d, want %d", out.Protocol, in.Protocol)
	}
}

// TestHeartbeatDecodeMinimal covers a beat from an older peel that only
// stamps ts: version/protocol must zero-fill, not error (additive policy).
func TestHeartbeatDecodeMinimal(t *testing.T) {
	data, err := bus.Encode(map[string]any{"ts": time.Now().UTC()})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	var out Heartbeat
	if err := bus.Decode(data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.TS.IsZero() {
		t.Error("TS is zero, want populated")
	}
	if out.Version != "" || out.Protocol != 0 {
		t.Errorf("optional fields = (%q, %d), want zero values", out.Version, out.Protocol)
	}
}
