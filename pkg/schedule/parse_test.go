package schedule_test

import (
	"testing"

	"github.com/ptorbus/zester/pkg/schedule"
)

func boolPtr(b bool) *bool { return &b }

func TestParse(t *testing.T) {
	t.Run("valid interval entry", func(t *testing.T) {
		raw := map[string]schedule.RawEntry{
			"backup": {
				Module:   "cmd.run",
				Interval: "5m",
			},
		}
		entries, errs := schedule.Parse(raw, schedule.SourceConfig)
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		e := entries[0]
		if e.Name != "backup" {
			t.Errorf("Name = %q, want %q", e.Name, "backup")
		}
		if e.Module != "cmd.run" {
			t.Errorf("Module = %q, want %q", e.Module, "cmd.run")
		}
		if e.Interval.Minutes() != 5 {
			t.Errorf("Interval = %v, want 5m", e.Interval)
		}
		if e.Source != schedule.SourceConfig {
			t.Errorf("Source = %q, want %q", e.Source, schedule.SourceConfig)
		}
	})

	t.Run("valid cron entry", func(t *testing.T) {
		raw := map[string]schedule.RawEntry{
			"hourly": {
				Module: "state.highstate",
				Cron:   "0 * * * *",
			},
		}
		entries, errs := schedule.Parse(raw, schedule.SourceSettings)
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		e := entries[0]
		if e.Cron != "0 * * * *" {
			t.Errorf("Cron = %q, want %q", e.Cron, "0 * * * *")
		}
		if e.Source != schedule.SourceSettings {
			t.Errorf("Source = %q, want %q", e.Source, schedule.SourceSettings)
		}
	})

	t.Run("parse with splay", func(t *testing.T) {
		raw := map[string]schedule.RawEntry{
			"check": {
				Module:   "test.ping",
				Interval: "10m",
				Splay:    "30s",
			},
		}
		entries, errs := schedule.Parse(raw, schedule.SourceConfig)
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if entries[0].Splay.Seconds() != 30 {
			t.Errorf("Splay = %v, want 30s", entries[0].Splay)
		}
	})

	t.Run("disabled entry excluded from result", func(t *testing.T) {
		raw := map[string]schedule.RawEntry{
			"disabled": {
				Module:   "cmd.run",
				Interval: "5m",
				Enabled:  boolPtr(false),
			},
		}
		entries, errs := schedule.Parse(raw, schedule.SourceConfig)
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if len(entries) != 0 {
			t.Errorf("expected 0 entries for disabled entry, got %d", len(entries))
		}
	})

	t.Run("invalid interval string returns error", func(t *testing.T) {
		raw := map[string]schedule.RawEntry{
			"bad": {
				Module:   "cmd.run",
				Interval: "not-a-duration",
			},
		}
		entries, errs := schedule.Parse(raw, schedule.SourceConfig)
		if len(errs) == 0 {
			t.Fatal("expected errors, got none")
		}
		if len(entries) != 0 {
			t.Errorf("expected 0 entries, got %d", len(entries))
		}
	})

	t.Run("bad cron returns error", func(t *testing.T) {
		raw := map[string]schedule.RawEntry{
			"bad-cron": {
				Module: "cmd.run",
				Cron:   "bad-cron-expr",
			},
		}
		entries, errs := schedule.Parse(raw, schedule.SourceConfig)
		if len(errs) == 0 {
			t.Fatal("expected errors, got none")
		}
		if len(entries) != 0 {
			t.Errorf("expected 0 entries, got %d", len(entries))
		}
	})

	t.Run("default maxrunning is 1", func(t *testing.T) {
		raw := map[string]schedule.RawEntry{
			"check": {
				Module:   "test.ping",
				Interval: "1m",
			},
		}
		entries, _ := schedule.Parse(raw, schedule.SourceConfig)
		if len(entries) == 1 && entries[0].MaxRunning != 1 {
			t.Errorf("MaxRunning = %d, want 1", entries[0].MaxRunning)
		}
	})
}

func TestParseSettings(t *testing.T) {
	t.Run("extracts from settings map", func(t *testing.T) {
		settings := map[string]any{
			"schedule": map[string]any{
				"heartbeat": map[string]any{
					"module":   "test.ping",
					"interval": "1m",
				},
			},
		}
		entries, errs := schedule.ParseSettings(settings, schedule.SourceSettings)
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if entries[0].Name != "heartbeat" {
			t.Errorf("Name = %q, want %q", entries[0].Name, "heartbeat")
		}
	})

	t.Run("no schedule key returns nil", func(t *testing.T) {
		settings := map[string]any{
			"other": "value",
		}
		entries, errs := schedule.ParseSettings(settings, schedule.SourceSettings)
		if entries != nil || errs != nil {
			t.Errorf("expected nil, nil; got %v, %v", entries, errs)
		}
	})

	t.Run("wrong type for schedule key returns error", func(t *testing.T) {
		settings := map[string]any{
			"schedule": "not-a-map",
		}
		entries, errs := schedule.ParseSettings(settings, schedule.SourceSettings)
		if len(errs) == 0 {
			t.Fatal("expected errors, got none")
		}
		if entries != nil {
			t.Errorf("expected nil entries, got %v", entries)
		}
	})

	t.Run("source field correctly set", func(t *testing.T) {
		settings := map[string]any{
			"schedule": map[string]any{
				"job": map[string]any{
					"module":   "cmd.run",
					"interval": "5m",
				},
			},
		}
		entries, _ := schedule.ParseSettings(settings, schedule.SourceSettings)
		if len(entries) == 1 && entries[0].Source != schedule.SourceSettings {
			t.Errorf("Source = %q, want %q", entries[0].Source, schedule.SourceSettings)
		}
	})
}
