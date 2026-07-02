package cmd

import (
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/facts"
)

// ---------- heartbeatStatus ----------

func TestHeartbeatStatus_AbsentIsOffline(t *testing.T) {
	online, lastSeen := heartbeatStatus(&facts.Heartbeat{}, false, time.Now())
	if online != "no" || lastSeen != "-" {
		t.Errorf("absent heartbeat = (%q, %q), want (no, -)", online, lastSeen)
	}
}

func TestHeartbeatStatus_FreshIsOnline(t *testing.T) {
	now := time.Now()
	hb := &facts.Heartbeat{TS: now.Add(-12 * time.Second)}
	online, lastSeen := heartbeatStatus(hb, true, now)
	if online != "yes" {
		t.Errorf("fresh heartbeat online = %q, want yes", online)
	}
	if lastSeen != "12s ago" {
		t.Errorf("fresh heartbeat lastSeen = %q, want %q", lastSeen, "12s ago")
	}
}

func TestHeartbeatStatus_StaleIsOffline(t *testing.T) {
	now := time.Now()
	hb := &facts.Heartbeat{TS: now.Add(-heartbeatFreshWindow - time.Minute)}
	online, lastSeen := heartbeatStatus(hb, true, now)
	if online != "no" {
		t.Errorf("stale heartbeat online = %q, want no", online)
	}
	if lastSeen == "-" {
		t.Errorf("stale heartbeat lastSeen = %q, want an age", lastSeen)
	}
}

func TestHeartbeatStatus_ZeroTimestampPresent(t *testing.T) {
	// A present key with no timestamp (unexpected writer) still counts as
	// online: presence in the 30s-TTL bucket implies liveness.
	online, lastSeen := heartbeatStatus(&facts.Heartbeat{}, true, time.Now())
	if online != "yes" || lastSeen != "-" {
		t.Errorf("zero-TS heartbeat = (%q, %q), want (yes, -)", online, lastSeen)
	}
}

func TestHeartbeatStatus_FutureTimestampClamped(t *testing.T) {
	// Clock skew: a heartbeat "from the future" is treated as 0s old.
	now := time.Now()
	hb := &facts.Heartbeat{TS: now.Add(30 * time.Second)}
	online, lastSeen := heartbeatStatus(hb, true, now)
	if online != "yes" {
		t.Errorf("future heartbeat online = %q, want yes", online)
	}
	if lastSeen != "0s ago" {
		t.Errorf("future heartbeat lastSeen = %q, want %q", lastSeen, "0s ago")
	}
}
