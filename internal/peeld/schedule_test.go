package peeld

import (
	"reflect"
	"testing"

	"github.com/nirnx/zester/internal/config"
)

func TestScheduleRawEntries(t *testing.T) {
	enabled := false
	entries := map[string]config.PeelScheduleEntry{
		"fact-refresh": {
			Module:     "facts.items",
			Args:       map[string]any{"key": "os"},
			Interval:   "5m",
			Cron:       "0 * * * *",
			Splay:      "30s",
			MaxRunning: 2,
			RunOnStart: true,
			ReturnJob:  true,
			Enabled:    &enabled,
		},
		"ping": {
			Module:   "test.ping",
			Interval: "1m",
		},
	}

	raw := scheduleRawEntries(entries)
	if len(raw) != 2 {
		t.Fatalf("len = %d, want 2", len(raw))
	}

	fr, ok := raw["fact-refresh"]
	if !ok {
		t.Fatal("missing fact-refresh entry")
	}
	if fr.Module != "facts.items" {
		t.Errorf("Module = %q, want %q", fr.Module, "facts.items")
	}
	if !reflect.DeepEqual(fr.Args, map[string]any{"key": "os"}) {
		t.Errorf("Args = %v, want map[key:os]", fr.Args)
	}
	if fr.Interval != "5m" {
		t.Errorf("Interval = %q, want %q", fr.Interval, "5m")
	}
	if fr.Cron != "0 * * * *" {
		t.Errorf("Cron = %q, want %q", fr.Cron, "0 * * * *")
	}
	if fr.Splay != "30s" {
		t.Errorf("Splay = %q, want %q", fr.Splay, "30s")
	}
	if fr.MaxRunning != 2 {
		t.Errorf("MaxRunning = %d, want 2", fr.MaxRunning)
	}
	if !fr.RunOnStart {
		t.Error("RunOnStart = false, want true")
	}
	if !fr.ReturnJob {
		t.Error("ReturnJob = false, want true")
	}
	// Pointer passthrough, not a copy: distinguishes explicit false from unset.
	if fr.Enabled != &enabled {
		t.Error("Enabled pointer not passed through")
	}

	ping, ok := raw["ping"]
	if !ok {
		t.Fatal("missing ping entry")
	}
	if ping.Module != "test.ping" {
		t.Errorf("Module = %q, want %q", ping.Module, "test.ping")
	}
	if ping.Interval != "1m" {
		t.Errorf("Interval = %q, want %q", ping.Interval, "1m")
	}
	if ping.Enabled != nil {
		t.Errorf("Enabled = %v, want nil for unset", *ping.Enabled)
	}
	if ping.Args != nil {
		t.Errorf("Args = %v, want nil", ping.Args)
	}
}

func TestScheduleRawEntriesEmpty(t *testing.T) {
	if raw := scheduleRawEntries(map[string]config.PeelScheduleEntry{}); len(raw) != 0 {
		t.Errorf("empty map: len = %d, want 0", len(raw))
	}
	if raw := scheduleRawEntries(nil); len(raw) != 0 {
		t.Errorf("nil map: len = %d, want 0", len(raw))
	}
}
