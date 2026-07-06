package event_test

import (
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/event"
	"github.com/nirnx/zester/pkg/proto"
)

func TestNewEvent(t *testing.T) {
	before := time.Now().UTC()
	ev := event.NewEvent("myco/deploy/finished", map[string]any{"rev": "abc"})
	after := time.Now().UTC()

	if ev.ID == "" {
		t.Error("ID should be minted")
	}
	if ev.Tag != "myco/deploy/finished" {
		t.Errorf("Tag: got %q", ev.Tag)
	}
	if ev.Data["rev"] != "abc" {
		t.Errorf("Data: got %v", ev.Data)
	}
	if ev.V != proto.ProtocolVersion {
		t.Errorf("V: got %d, want %d", ev.V, proto.ProtocolVersion)
	}
	if ev.TS.Before(before) || ev.TS.After(after) {
		t.Errorf("TS %v outside [%v, %v]", ev.TS, before, after)
	}
	if ev.Origin != "" || ev.Depth != 0 {
		t.Errorf("organic event must have zero Origin/Depth, got %q/%d", ev.Origin, ev.Depth)
	}

	if other := event.NewEvent("a/b", nil); other.ID == ev.ID {
		t.Error("consecutive events must mint distinct IDs")
	}
	if nilData := event.NewEvent("a/b", nil); nilData.Data != nil {
		t.Errorf("nil data must stay nil (omitempty), got %v", nilData.Data)
	}
}

func TestDottedSlashTagRoundTrip(t *testing.T) {
	tests := []struct {
		slash, dotted string
	}{
		{"a", "a"},
		{"a/b", "a.b"},
		{"myco/deploy/finished", "myco.deploy.finished"},
		{"beacon/web-01/service/nginx", "beacon.web-01.service.nginx"},
		{"under_score/da-sh", "under_score.da-sh"},
	}
	for _, tt := range tests {
		if got := event.DottedTag(tt.slash); got != tt.dotted {
			t.Errorf("DottedTag(%q) = %q, want %q", tt.slash, got, tt.dotted)
		}
		if got := event.SlashTag(tt.dotted); got != tt.slash {
			t.Errorf("SlashTag(%q) = %q, want %q", tt.dotted, got, tt.slash)
		}
		// 1:1 mapping: round trips are identities.
		if got := event.SlashTag(event.DottedTag(tt.slash)); got != tt.slash {
			t.Errorf("SlashTag(DottedTag(%q)) = %q", tt.slash, got)
		}
		if got := event.DottedTag(event.SlashTag(tt.dotted)); got != tt.dotted {
			t.Errorf("DottedTag(SlashTag(%q)) = %q", tt.dotted, got)
		}
	}
}

func TestMatchKey(t *testing.T) {
	tests := []struct {
		origin, tag, want string
	}{
		{"web-01", "myco/deploy/finished", "web-01/myco/deploy/finished"},
		{bus.OriginMaster, "enroll/pending/enr-1", "_master/enroll/pending/enr-1"},
		{bus.OriginAdmin, "chain/next", "_admin/chain/next"},
		// A spoofing peel embedding "_master" in its TAG can never produce
		// a key with the _master ORIGIN prefix.
		{"evil", "_master/enroll/pending/enr-1", "evil/_master/enroll/pending/enr-1"},
	}
	for _, tt := range tests {
		if got := event.MatchKey(tt.origin, tt.tag); got != tt.want {
			t.Errorf("MatchKey(%q, %q) = %q, want %q", tt.origin, tt.tag, got, tt.want)
		}
	}
}

func TestValidateTag(t *testing.T) {
	valid := []string{
		"a",
		"a/b",
		"myco/deploy/finished",
		"A-Z_09/x",
		"_leading/underscore", // only ORIGINS reserve the leading underscore
		"-dash",
	}
	for _, tag := range valid {
		if err := event.ValidateTag(tag); err != nil {
			t.Errorf("ValidateTag(%q) unexpected error: %v", tag, err)
		}
	}

	invalid := []struct {
		name, tag string
	}{
		{"empty", ""},
		{"empty segment middle", "a//b"},
		{"empty segment leading", "/a"},
		{"empty segment trailing", "a/"},
		{"dot", "a.b"},
		{"dot segment", "a/b.c"},
		{"star", "a*"},
		{"star segment", "a/*"},
		{"gt", "a>"},
		{"gt segment", "a/>"},
		{"space", "a b"},
		{"tab", "a/\tb"},
		{"newline", "a\n"},
		{"unicode", "café"},
		{"nats dot injection", "a/b.c.d"},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if err := event.ValidateTag(tt.tag); err == nil {
				t.Errorf("ValidateTag(%q) should fail", tt.tag)
			}
		})
	}
}

func TestParseSubject(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		origin  string
		tag     string
		kind    event.Kind
	}{
		{
			name:    "peel send single token",
			subject: "zester.event.web-01.send.ping",
			origin:  "web-01",
			tag:     "ping",
			kind:    event.KindSend,
		},
		{
			name:    "peel send multi token",
			subject: "zester.event.web-01.send.myco.deploy.finished",
			origin:  "web-01",
			tag:     "myco/deploy/finished",
			kind:    event.KindSend,
		},
		{
			name:    "beacon",
			subject: "zester.event.web-01.beacon.service",
			origin:  "web-01",
			tag:     "beacon/web-01/service",
			kind:    event.KindBeacon,
		},
		{
			name:    "beacon with extras",
			subject: "zester.event.web-01.beacon.service.nginx",
			origin:  "web-01",
			tag:     "beacon/web-01/service/nginx",
			kind:    event.KindBeacon,
		},
		{
			name:    "master single token",
			subject: "zester.event._master.ping",
			origin:  bus.OriginMaster,
			tag:     "ping",
			kind:    event.KindMaster,
		},
		{
			name:    "master enroll pending",
			subject: "zester.event._master.enroll.pending.enr-1",
			origin:  bus.OriginMaster,
			tag:     "enroll/pending/enr-1",
			kind:    event.KindMaster,
		},
		{
			// For _master, EVERYTHING after the origin is the tag — a
			// "send" token is not special.
			name:    "master send token is tag data",
			subject: "zester.event._master.send.x",
			origin:  bus.OriginMaster,
			tag:     "send/x",
			kind:    event.KindMaster,
		},
		{
			name:    "master derived reaction event",
			subject: "zester.event._master.reaction.chain.next",
			origin:  bus.OriginMaster,
			tag:     "reaction/chain/next",
			kind:    event.KindMaster,
		},
		{
			name:    "admin send",
			subject: "zester.event._admin.send.myco.deploy",
			origin:  bus.OriginAdmin,
			tag:     "myco/deploy",
			kind:    event.KindAdmin,
		},
		{
			// A peel embedding "_master" in its own tag tokens keeps its
			// real origin: the match key can never gain a _master prefix.
			name:    "peel spoofing _master in tag",
			subject: "zester.event.evil.send._master.enroll.pending.enr-1",
			origin:  "evil",
			tag:     "_master/enroll/pending/enr-1",
			kind:    event.KindSend,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origin, tag, kind, err := event.ParseSubject(tt.subject)
			if err != nil {
				t.Fatalf("ParseSubject(%q): %v", tt.subject, err)
			}
			if origin != tt.origin {
				t.Errorf("origin: got %q, want %q", origin, tt.origin)
			}
			if tag != tt.tag {
				t.Errorf("tag: got %q, want %q", tag, tt.tag)
			}
			if kind != tt.kind {
				t.Errorf("kind: got %q, want %q", kind, tt.kind)
			}
		})
	}
}

func TestParseSubjectRejects(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		errPart string
	}{
		{"empty", "", "at least 4 tokens"},
		{"one token", "zester", "at least 4 tokens"},
		{"two tokens", "zester.event", "at least 4 tokens"},
		{"three tokens", "zester.event.web-01", "at least 4 tokens"},
		{"wrong root", "nats.event.web-01.send.x", "not under zester.event"},
		{"wrong category", "zester.fact.web-01.send.x", "not under zester.event"},
		{"job subject", "zester.job.jid1.return.web-01", "not under zester.event"},
		{"empty origin token", "zester.event..send.x", "empty token"},
		{"empty middle token", "zester.event.web-01..x", "empty token"},
		{"trailing dot", "zester.event.web-01.send.", "empty token"},
		{"leading dot", ".zester.event.web-01.send", "empty token"},
		{"double dot in tag", "zester.event.web-01.send.a..b", "empty token"},
		{"wildcard origin", "zester.event.*.send.x", "wildcard token"},
		{"wildcard tag token", "zester.event.web-01.send.*", "wildcard token"},
		{"full wildcard tail", "zester.event.web-01.send.>", "wildcard token"},
		{"wildcard root", "*.event.web-01.send.x", "wildcard token"},
		{"peel unknown sub-token", "zester.event.web-01.foo.bar", "unknown peel event sub-token"},
		{"peel bare send", "zester.event.web-01.send", "no tag tokens"},
		{"peel bare beacon", "zester.event.web-01.beacon", "no name token"},
		{"admin without send", "zester.event._admin.foo.bar", "sub-token"},
		{"admin bare send", "zester.event._admin.send", "no tag tokens"},
		{"admin beacon shape", "zester.event._admin.beacon.disk", "sub-token"},
		{"reserved origin", "zester.event._future.send.x", "reserved origin"},
		{"reserved origin bare underscore", "zester.event._.send.x", "reserved origin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origin, tag, kind, err := event.ParseSubject(tt.subject)
			if err == nil {
				t.Fatalf("ParseSubject(%q) should fail, got origin=%q tag=%q kind=%q", tt.subject, origin, tag, kind)
			}
			if !strings.Contains(err.Error(), tt.errPart) {
				t.Errorf("error %q does not mention %q", err, tt.errPart)
			}
			if origin != "" || tag != "" || kind != "" {
				t.Errorf("failed parse must return zero values, got origin=%q tag=%q kind=%q", origin, tag, kind)
			}
		})
	}
}

// TestSubjectHelperRoundTrips builds subjects with the pkg/bus helpers and
// parses them back, proving the helper <-> parser contract stays in sync.
func TestSubjectHelperRoundTrips(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		origin  string
		tag     string
		kind    event.Kind
	}{
		{
			name:    "peel send helper",
			subject: bus.PeelEventSendSubject("web-01", event.DottedTag("myco/deploy/finished")),
			origin:  "web-01",
			tag:     "myco/deploy/finished",
			kind:    event.KindSend,
		},
		{
			name:    "master helper",
			subject: bus.MasterEventSubject(event.DottedTag("enroll/pending/enr-1")),
			origin:  bus.OriginMaster,
			tag:     "enroll/pending/enr-1",
			kind:    event.KindMaster,
		},
		{
			name:    "admin send helper",
			subject: bus.AdminEventSendSubject(event.DottedTag("chain/next")),
			origin:  bus.OriginAdmin,
			tag:     "chain/next",
			kind:    event.KindAdmin,
		},
		{
			name:    "beacon helper",
			subject: bus.BeaconSubject("web-01", "service"),
			origin:  "web-01",
			tag:     "beacon/web-01/service",
			kind:    event.KindBeacon,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origin, tag, kind, err := event.ParseSubject(tt.subject)
			if err != nil {
				t.Fatalf("ParseSubject(%q): %v", tt.subject, err)
			}
			if origin != tt.origin || tag != tt.tag || kind != tt.kind {
				t.Errorf("got (%q, %q, %q), want (%q, %q, %q)", origin, tag, kind, tt.origin, tt.tag, tt.kind)
			}
		})
	}
}
