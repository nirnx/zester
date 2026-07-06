package cmd

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/event"
	"github.com/nirnx/zester/pkg/reactor"
)

// ---------- normalizeEventTag ----------

func TestNormalizeEventTag(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "slash form", in: "myco/deploy/finished", want: "myco/deploy/finished"},
		{name: "dotted form converted", in: "myco.deploy.finished", want: "myco/deploy/finished"},
		{name: "single segment", in: "maintenance", want: "maintenance"},
		{name: "underscore leading segment", in: "_internal/thing", want: "_internal/thing"},
		{name: "mixed dots and slashes rejected", in: "myco/deploy.finished", wantErr: true},
		{name: "empty", in: "", wantErr: true},
		{name: "empty segment", in: "myco//finished", wantErr: true},
		{name: "wildcard rejected", in: "myco/*/finished", wantErr: true},
		{name: "nats wildcard rejected", in: "myco/>", wantErr: true},
		{name: "space rejected", in: "myco/dep loy", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeEventTag(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeEventTag(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeEventTag(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("normalizeEventTag(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ---------- parseEventData ----------

func TestParseEventData(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    map[string]any
		wantErr bool
	}{
		{name: "no pairs is nil", in: nil, want: nil},
		{name: "empty slice is nil", in: []string{}, want: nil},
		{name: "single pair", in: []string{"version=1.2.3"}, want: map[string]any{"version": "1.2.3"}},
		{
			name: "multiple pairs",
			in:   []string{"version=1.2.3", "env=prod"},
			want: map[string]any{"version": "1.2.3", "env": "prod"},
		},
		{
			name: "value containing equals",
			in:   []string{"expr=a=b"},
			want: map[string]any{"expr": "a=b"},
		},
		{name: "empty value allowed", in: []string{"flag="}, want: map[string]any{"flag": ""}},
		{name: "missing equals rejected", in: []string{"oops"}, wantErr: true},
		{name: "empty key rejected", in: []string{"=value"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEventData(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseEventData(%v) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseEventData(%v): %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseEventData(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// ---------- renderEventLine ----------

// encodeEvent marshals an event the way publishers do.
func encodeEvent(t *testing.T, ev event.Event) []byte {
	t.Helper()
	data, err := bus.Encode(&ev)
	if err != nil {
		t.Fatalf("encode event: %v", err)
	}
	return data
}

func TestRenderEventLine_Text(t *testing.T) {
	ev := event.NewEvent("myco/deploy/finished", map[string]any{"version": "1.2.3"})
	ev.TS = time.Date(2026, 7, 3, 12, 30, 45, 0, time.UTC)
	subject := bus.PeelEventSendSubject("web-01", event.DottedTag(ev.Tag))

	line, ok := renderEventLine(subject, encodeEvent(t, ev), nil, "text")
	if !ok {
		t.Fatal("renderEventLine returned ok=false for a well-formed event")
	}
	if !strings.Contains(line, "web-01/myco/deploy/finished") {
		t.Errorf("line %q missing match key", line)
	}
	if !strings.Contains(line, `"version":"1.2.3"`) {
		t.Errorf("line %q missing compact data", line)
	}
	if strings.Contains(line, "depth=") {
		t.Errorf("line %q shows depth for a depth-0 event", line)
	}
}

func TestRenderEventLine_TextDepthShownWhenPositive(t *testing.T) {
	ev := event.NewEvent("reaction/chain/next", nil)
	ev.Depth = 2
	ev.Origin = "reaction:reactor.chain"
	subject := bus.MasterEventSubject("reaction." + event.DottedTag("chain/next"))

	line, ok := renderEventLine(subject, encodeEvent(t, ev), nil, "text")
	if !ok {
		t.Fatal("renderEventLine returned ok=false for a well-formed event")
	}
	if !strings.Contains(line, "depth=2") {
		t.Errorf("line %q missing depth=2", line)
	}
	if !strings.Contains(line, "_master/reaction/chain/next") {
		t.Errorf("line %q missing _master match key", line)
	}
}

func TestRenderEventLine_JSON(t *testing.T) {
	ev := event.NewEvent("myco/deploy/finished", map[string]any{"version": "1.2.3"})
	subject := bus.AdminEventSendSubject(event.DottedTag(ev.Tag))

	line, ok := renderEventLine(subject, encodeEvent(t, ev), nil, "json")
	if !ok {
		t.Fatal("renderEventLine returned ok=false for a well-formed event")
	}

	var rec eventWatchRecord
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("watch JSON line does not parse: %v\nline: %s", err, line)
	}
	if rec.Key != "_admin/myco/deploy/finished" {
		t.Errorf("key = %q, want _admin/myco/deploy/finished", rec.Key)
	}
	if rec.Origin != bus.OriginAdmin {
		t.Errorf("origin = %q, want %q", rec.Origin, bus.OriginAdmin)
	}
	if rec.Tag != "myco/deploy/finished" {
		t.Errorf("tag = %q, want myco/deploy/finished", rec.Tag)
	}
	if rec.ID != ev.ID {
		t.Errorf("id = %q, want %q", rec.ID, ev.ID)
	}
	if got := rec.Data["version"]; got != "1.2.3" {
		t.Errorf("data.version = %v, want 1.2.3", got)
	}
}

func TestRenderEventLine_GlobFilter(t *testing.T) {
	ev := event.NewEvent("myco/deploy/finished", nil)
	subject := bus.PeelEventSendSubject("web-01", event.DottedTag(ev.Tag))
	payload := encodeEvent(t, ev)

	tests := []struct {
		name string
		glob string
		want bool
	}{
		{name: "star crosses slashes", glob: "web-*", want: true},
		{name: "exact key", glob: "web-01/myco/deploy/finished", want: true},
		{name: "tag-only glob without origin", glob: "myco/*", want: false},
		{name: "other origin", glob: "_master/*", want: false},
		{name: "question mark", glob: "web-0?/myco/deploy/finished", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re, err := reactor.CompileMatchGlob(tt.glob)
			if err != nil {
				t.Fatalf("compile glob %q: %v", tt.glob, err)
			}
			_, ok := renderEventLine(subject, payload, re, "text")
			if ok != tt.want {
				t.Errorf("renderEventLine(glob %q) ok = %v, want %v", tt.glob, ok, tt.want)
			}
		})
	}
}

func TestRenderEventLine_DropsMalformed(t *testing.T) {
	goodEv := event.NewEvent("myco/deploy/finished", nil)
	goodPayload := encodeEvent(t, goodEv)

	tests := []struct {
		name    string
		subject string
		payload []byte
	}{
		{name: "short subject", subject: "zester.event.web-01", payload: goodPayload},
		{name: "not an event subject", subject: "zester.job.abc.return.web-01", payload: goodPayload},
		{name: "reserved origin", subject: "zester.event._bogus.send.x", payload: goodPayload},
		{name: "unknown peel sub-token", subject: "zester.event.web-01.bogus.x", payload: goodPayload},
		{
			name:    "undecodable payload",
			subject: bus.PeelEventSendSubject("web-01", "myco.deploy.finished"),
			payload: []byte{0xc1}, // msgpack "never used" byte
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if line, ok := renderEventLine(tt.subject, tt.payload, nil, "text"); ok {
				t.Errorf("renderEventLine(%q) = %q, want dropped", tt.subject, line)
			}
		})
	}
}
