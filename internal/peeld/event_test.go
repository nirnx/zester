package peeld

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/internal/metrics"
	"github.com/nirnx/zester/pkg/beacon"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/event"
	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/proto"
)

// newEventTestAgent builds on newTestAgent with just enough extra wiring to
// drive execModule directly: a populated states-cache dir already marked
// effective (skips the states-engine rebuild, which needs a NATS client) and
// a bare module context.
func newEventTestAgent(t *testing.T) *Agent {
	t.Helper()
	a := newTestAgent(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "placeholder.zy"), []byte("x: {test.ping: []}"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.cfg.StatesCache = dir
	a.effectiveStatesDir = dir
	a.mctx = exec.NewModuleContext(&exec.ProviderSet{}, map[string]any{}, nil, discardLogger())
	return a
}

// subscribeEvents captures every event published under zester.event.> on the
// agent's FakePubSub.
func subscribeEvents(t *testing.T, a *Agent) *[]struct {
	subject string
	ev      event.Event
} {
	t.Helper()
	var got []struct {
		subject string
		ev      event.Event
	}
	ps := a.ps.(*bustest.FakePubSub)
	if _, err := ps.Subscribe("zester.event.>", func(msg *bus.Msg) {
		var ev event.Event
		if err := bus.Decode(msg.Data, &ev); err != nil {
			t.Errorf("decode event: %v", err)
			return
		}
		got = append(got, struct {
			subject string
			ev      event.Event
		}{msg.Subject, ev})
	}); err != nil {
		t.Fatal(err)
	}
	return &got
}

// TestExecEventSendPublishes pins the wire contract: subject
// zester.event.<peelID>.send.<dotted-tag>, payload Tag equal to the
// subject-derived slash tag (the reactor's anti-spoof cross-check), args as
// Data, fresh KSUID, protocol version stamped, organic depth 0.
func TestExecEventSendPublishes(t *testing.T) {
	a := newTestAgent(t)
	got := subscribeEvents(t, a)

	resp := a.execEventSend("myco/deploy/finished", map[string]any{"version": "v1.2.3"}, 0)

	if resp.Error != "" || !resp.Success {
		t.Fatalf("response = success=%v error=%q, want success", resp.Success, resp.Error)
	}
	if len(*got) != 1 {
		t.Fatalf("published = %d events, want 1", len(*got))
	}
	pub := (*got)[0]

	if want := "zester.event.p1.send.myco.deploy.finished"; pub.subject != want {
		t.Errorf("subject = %q, want %q", pub.subject, want)
	}

	origin, slashTag, kind, err := event.ParseSubject(pub.subject)
	if err != nil {
		t.Fatalf("ParseSubject(%q): %v", pub.subject, err)
	}
	if origin != "p1" || kind != event.KindSend {
		t.Errorf("origin/kind = %q/%q, want p1/%q", origin, kind, event.KindSend)
	}
	if pub.ev.Tag != slashTag || pub.ev.Tag != "myco/deploy/finished" {
		t.Errorf("payload Tag = %q, subject-derived = %q — consumer would drop as spoofed", pub.ev.Tag, slashTag)
	}
	if pub.ev.Data["version"] != "v1.2.3" {
		t.Errorf("Data = %v, want {version: v1.2.3}", pub.ev.Data)
	}
	if pub.ev.ID == "" {
		t.Error("event ID empty")
	}
	if pub.ev.V != proto.ProtocolVersion {
		t.Errorf("event V = %d, want %d", pub.ev.V, proto.ProtocolVersion)
	}
	if pub.ev.Depth != 0 || pub.ev.Origin != "" {
		t.Errorf("organic event Depth=%d Origin=%q, want 0/empty", pub.ev.Depth, pub.ev.Origin)
	}
	if pub.ev.TS.IsZero() {
		t.Error("event TS is zero")
	}

	if len(resp.Results) != 1 || resp.Results[0].Details["result"] != "event sent: myco/deploy/finished" {
		t.Errorf("results = %+v, want 'event sent: myco/deploy/finished'", resp.Results)
	}
	if resp.Results[0].Details["event_id"] != pub.ev.ID {
		t.Errorf("result event_id = %q, want %q", resp.Results[0].Details["event_id"], pub.ev.ID)
	}
}

// TestNormalizeEventTag pins the documented normalization: dotted-only tags
// convert to slash form; slash tags pass verbatim; mixed and invalid forms
// are rejected.
func TestNormalizeEventTag(t *testing.T) {
	tests := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{raw: "myco/deploy/finished", want: "myco/deploy/finished"},
		{raw: "myco.deploy.finished", want: "myco/deploy/finished"},
		{raw: "single", want: "single"},
		{raw: "under_score-dash", want: "under_score-dash"},
		{raw: "a.b/c", wantErr: true}, // mixed dots+slashes
		{raw: "a..b", wantErr: true},  // empty segment after conversion
		{raw: "a//b", wantErr: true},  // empty slash segment
		{raw: "bad tag", wantErr: true},
		{raw: "a/*/b", wantErr: true}, // wildcard injection
		{raw: "a/>/b", wantErr: true}, // wildcard injection
		{raw: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := normalizeEventTag(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeEventTag(%q) = %q, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeEventTag(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("normalizeEventTag(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestExecEventSendInvalidTagNoPublish: invalid tags fail the execution and
// nothing reaches the bus.
func TestExecEventSendInvalidTagNoPublish(t *testing.T) {
	a := newTestAgent(t)
	got := subscribeEvents(t, a)

	for _, raw := range []string{"", "bad tag", "a.b/c", "a/*/b"} {
		resp := a.execEventSend(raw, nil, 0)
		if resp.Error == "" || resp.Success {
			t.Errorf("execEventSend(%q) = success=%v error=%q, want error", raw, resp.Success, resp.Error)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("published = %d events for invalid tags, want 0", len(*got))
	}
}

// TestExecModuleEventSendDepthPropagation drives the FULL dispatch path
// (execModule) and pins amendment 21: the ExecRequest's ReactorDepth is
// stamped into the emitted event's Depth, and event.send is dispatched as a
// special case (not via pkg/execmod or the state registry).
func TestExecModuleEventSendDepthPropagation(t *testing.T) {
	a := newEventTestAgent(t)
	got := subscribeEvents(t, a)

	req := proto.ExecRequest{
		ID:           "myco.chain.next", // dotted form via the bare positional
		Module:       "event.send",
		Args:         map[string]any{"hop": "two"},
		ReactorDepth: 2,
	}
	resp, err := a.execModule(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != "" || !resp.Success {
		t.Fatalf("response = success=%v error=%q", resp.Success, resp.Error)
	}
	if len(*got) != 1 {
		t.Fatalf("published = %d events, want 1", len(*got))
	}
	pub := (*got)[0]
	if want := "zester.event.p1.send.myco.chain.next"; pub.subject != want {
		t.Errorf("subject = %q, want %q", pub.subject, want)
	}
	if pub.ev.Tag != "myco/chain/next" {
		t.Errorf("Tag = %q, want myco/chain/next (dotted positional normalized)", pub.ev.Tag)
	}
	if pub.ev.Depth != 2 {
		t.Errorf("Depth = %d, want 2 (from ExecRequest.ReactorDepth)", pub.ev.Depth)
	}
	if pub.ev.Data["hop"] != "two" {
		t.Errorf("Data = %v, want {hop: two}", pub.ev.Data)
	}
}

// TestExecEventSendTagArgFallback: callers without a bare positional
// (scheduled runs) pass tag=<tag>; the control key is stripped from Data.
func TestExecEventSendTagArgFallback(t *testing.T) {
	a := newTestAgent(t)
	got := subscribeEvents(t, a)

	resp := a.execEventSend("", map[string]any{"tag": "backup/done", "files": 42}, 0)
	if resp.Error != "" || !resp.Success {
		t.Fatalf("response = success=%v error=%q", resp.Success, resp.Error)
	}
	if len(*got) != 1 {
		t.Fatalf("published = %d events, want 1", len(*got))
	}
	ev := (*got)[0].ev
	if ev.Tag != "backup/done" {
		t.Errorf("Tag = %q, want backup/done", ev.Tag)
	}
	if _, ok := ev.Data["tag"]; ok {
		t.Error("control key 'tag' leaked into Data")
	}
	if len(ev.Data) != 1 {
		t.Errorf("Data = %v, want only {files: 42}", ev.Data)
	}
}

// TestExecEventSendTestMode: test=True reports without publishing and strips
// the control key.
func TestExecEventSendTestMode(t *testing.T) {
	a := newTestAgent(t)
	got := subscribeEvents(t, a)

	resp := a.execEventSend("myco/dry/run", map[string]any{"test": true}, 0)
	if !resp.Success || !resp.Test {
		t.Fatalf("response = success=%v test=%v error=%q, want dry-run success", resp.Success, resp.Test, resp.Error)
	}
	if len(*got) != 0 {
		t.Fatalf("published = %d events in test mode, want 0", len(*got))
	}
	if resp.Results[0].Details["result"] != "would send event: myco/dry/run" {
		t.Errorf("result = %q, want the would-send message", resp.Results[0].Details["result"])
	}
}

// failingPubSub is a bus.PubSub whose publishes always fail — the offline
// case (no peel-side buffering for ad-hoc events: the error is returned).
type failingPubSub struct{ err error }

func (f *failingPubSub) Publish(string, []byte) error { return f.err }
func (f *failingPubSub) Subscribe(string, func(*bus.Msg)) (bus.Subscription, error) {
	return nil, f.err
}

// TestExecEventSendPublishError: a NATS publish failure fails the execution
// with the underlying error surfaced.
func TestExecEventSendPublishError(t *testing.T) {
	a := newTestAgent(t)
	a.ps = &failingPubSub{err: errors.New("nats: connection closed")}

	resp := a.execEventSend("myco/x", nil, 0)
	if resp.Success || resp.Error == "" {
		t.Fatalf("response = success=%v error=%q, want failure", resp.Success, resp.Error)
	}
	if !strings.Contains(resp.Error, "nats: connection closed") {
		t.Errorf("error = %q, want the publish error surfaced", resp.Error)
	}
}

// TestEventSendIsMutating pins the queueing decision (amendment 20):
// event.send has a side effect, so it must ride the execMu-serialized worker
// path, never the read-only fast path.
func TestEventSendIsMutating(t *testing.T) {
	a := newTestAgent(t)
	if a.readOnlyModule("event.send") {
		t.Error("readOnlyModule(event.send) = true, want false (mutating queue path)")
	}
}

// TestExecBusyFlagDuringExecution: the busy flag backing the beacon
// manager's BusyFn is set for the duration of a mutating execution (observed
// synchronously from inside the FakePubSub publish) and cleared afterwards.
func TestExecBusyFlagDuringExecution(t *testing.T) {
	a := newEventTestAgent(t)
	ps := a.ps.(*bustest.FakePubSub)

	busyDuring := false
	observed := false
	if _, err := ps.Subscribe("zester.event.>", func(*bus.Msg) {
		observed = true
		busyDuring = a.execBusy.Load()
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := a.execModule(context.Background(), proto.ExecRequest{ID: "busy/probe", Module: "event.send"}); err != nil {
		t.Fatal(err)
	}

	if !observed {
		t.Fatal("event never published; busy flag not observed")
	}
	if !busyDuring {
		t.Error("execBusy = false during mutating execution, want true")
	}
	if a.execBusy.Load() {
		t.Error("execBusy = true after execution returned, want false")
	}
}

// TestApplyResolvedSettingsBeaconReload covers the hot-reload plumbing: a
// settings change re-parses the beacons section and hot-swaps the manager's
// config (including disabling it when the key disappears), mirroring the
// basket_scope / schedule reload pattern.
func TestApplyResolvedSettingsBeaconReload(t *testing.T) {
	a := newTestAgent(t)
	mgr := beacon.NewManager(beacon.ManagerConfig{PeelID: "p1", Logger: discardLogger()})
	a.beaconPtr.Store(mgr)

	a.applyResolvedSettings(map[string]any{
		"beacons": map[string]any{
			"service": map[string]any{
				"services":     map[string]any{"nginx": map[string]any{}},
				"interval":     30,
				"onchangeonly": false,
			},
		},
	}, false)

	cfg := mgr.CurrentConfig()
	if cfg.Service == nil {
		t.Fatal("beacon config not applied on settings change")
	}
	if len(cfg.Service.Services) != 1 || cfg.Service.Services[0] != "nginx" {
		t.Errorf("services = %v, want [nginx]", cfg.Service.Services)
	}
	if cfg.Service.Interval != 30*time.Second {
		t.Errorf("interval = %v, want 30s", cfg.Service.Interval)
	}
	if cfg.Service.OnChangeOnly {
		t.Error("onchangeonly = true, want false")
	}

	// Removing the beacons key disables polling.
	a.applyResolvedSettings(map[string]any{}, false)
	if mgr.CurrentConfig().Service != nil {
		t.Error("beacon config not cleared after beacons key removed")
	}

	// Metric plumbing: the OnEvent hook target exists on the peel registry.
	m := metrics.NewPeelRegistry()
	m.PeelBeaconEventsTotal.WithLabelValues("service").Inc()
}
