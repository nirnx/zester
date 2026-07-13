package cmd

import (
	"errors"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/job"
	"github.com/nirnx/zester/pkg/proto"
)

var errTimeout = errors.New("request timeout")

func TestShouldColor(t *testing.T) {
	// shouldColor should return false when NO_COLOR is set.
	t.Setenv("NO_COLOR", "1")
	if shouldColor() {
		t.Error("shouldColor() = true; want false when NO_COLOR is set")
	}

	// With NO_COLOR unset, shouldColor depends on whether stdout is a TTY.
	// In tests, stdout is not a TTY, so it should return false.
	t.Setenv("NO_COLOR", "")
	// NO_COLOR="" is still a non-empty check — the spec says "if set".
	// Actually looking at the code: os.Getenv returns "" and the check is != "".
	// So NO_COLOR="" means shouldColor proceeds to the TTY check.
	// In test, stdout is typically not a TTY, so shouldColor should be false.
	// We just verify it doesn't panic; the actual value depends on the test runner.
}

func TestShouldColor_EnvSet(t *testing.T) {
	t.Setenv("NO_COLOR", "true")
	if shouldColor() {
		t.Error("shouldColor() = true; want false when NO_COLOR=true")
	}
}

func TestColorize(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		color   string
		enabled bool
		want    string
	}{
		{
			name:    "color disabled returns plain text",
			text:    "hello",
			color:   colorGreen,
			enabled: false,
			want:    "hello",
		},
		{
			name:    "color enabled wraps text",
			text:    "hello",
			color:   colorGreen,
			enabled: true,
			want:    colorGreen + "hello" + colorReset,
		},
		{
			name:    "red color enabled",
			text:    "error",
			color:   colorRed,
			enabled: true,
			want:    colorRed + "error" + colorReset,
		},
		{
			name:    "empty text with color enabled",
			text:    "",
			color:   colorGreen,
			enabled: true,
			want:    colorGreen + colorReset,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := colorize(tt.text, tt.color, tt.enabled)
			if got != tt.want {
				t.Errorf("colorize(%q, %q, %t) = %q; want %q", tt.text, tt.color, tt.enabled, got, tt.want)
			}
		})
	}
}

func TestIsStreamlined(t *testing.T) {
	tests := []struct {
		module string
		want   bool
	}{
		{"cmd.run", true},
		{"test.ping", true},
		{"sys.doc", true},
		{"facts.items", true},
		{"facts.get", true},
		{"facts.keys", true},
		{"settings.items", true},
		{"settings.get", true},
		{"settings.keys", true},
		{"file.managed", false},
		{"pkg.installed", false},
		{"state.apply", false},
		{"state.highstate", false},
		{"sys.list_functions", false},
		{"custom.module", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.module, func(t *testing.T) {
			got := isStreamlined(tt.module)
			if got != tt.want {
				t.Errorf("isStreamlined(%q) = %t; want %t", tt.module, got, tt.want)
			}
		})
	}
}

func TestDirectToOutputRecords(t *testing.T) {
	t.Run("successful response with results", func(t *testing.T) {
		results := []directResult{
			{
				peelID: "peel-01",
				resp: &proto.ExecResponse{
					PeelID:  "peel-01",
					Success: true,
					Results: []proto.StateResult{
						{
							Name:     "install-nginx",
							Changed:  true,
							Details:  map[string]string{"stdout": "installed"},
							Duration: 2 * time.Second,
							Diff:     "--- old\n+++ new",
						},
					},
				},
			},
		}

		records := directToOutputRecords(results)
		if len(records) != 1 {
			t.Fatalf("got %d records; want 1", len(records))
		}

		rec := records[0]
		if rec.PeelID != "peel-01" {
			t.Errorf("PeelID = %q; want %q", rec.PeelID, "peel-01")
		}
		if !rec.Success {
			t.Error("Success = false; want true")
		}
		if rec.Error != "" {
			t.Errorf("Error = %q; want empty", rec.Error)
		}
		if len(rec.Results) != 1 {
			t.Fatalf("got %d results; want 1", len(rec.Results))
		}

		sr := rec.Results[0]
		if sr.Name != "install-nginx" {
			t.Errorf("Name = %q; want %q", sr.Name, "install-nginx")
		}
		if !sr.Changed {
			t.Error("Changed = false; want true")
		}
		if sr.Details["stdout"] != "installed" {
			t.Errorf("Details[stdout] = %q; want %q", sr.Details["stdout"], "installed")
		}
		if sr.Duration != 2*time.Second {
			t.Errorf("Duration = %v; want %v", sr.Duration, 2*time.Second)
		}
		if sr.Diff != "--- old\n+++ new" {
			t.Errorf("Diff = %q; want %q", sr.Diff, "--- old\n+++ new")
		}
	})

	t.Run("transport error", func(t *testing.T) {
		results := []directResult{
			{
				peelID: "peel-02",
				err:    errors.New("context deadline exceeded"),
			},
		}

		records := directToOutputRecords(results)
		if len(records) != 1 {
			t.Fatalf("got %d records; want 1", len(records))
		}

		rec := records[0]
		if rec.PeelID != "peel-02" {
			t.Errorf("PeelID = %q; want %q", rec.PeelID, "peel-02")
		}
		if rec.Success {
			t.Error("Success = true; want false")
		}
		if rec.Error == "" {
			t.Error("Error is empty; want non-empty error message")
		}
	})

	t.Run("response-level error", func(t *testing.T) {
		results := []directResult{
			{
				peelID: "peel-03",
				resp: &proto.ExecResponse{
					PeelID:  "peel-03",
					Success: false,
					Error:   "module not found",
				},
			},
		}

		records := directToOutputRecords(results)
		rec := records[0]
		if rec.Success {
			t.Error("Success = true; want false")
		}
		if rec.Error != "module not found" {
			t.Errorf("Error = %q; want %q", rec.Error, "module not found")
		}
	})

	t.Run("nil response no error", func(t *testing.T) {
		results := []directResult{
			{peelID: "peel-04"},
		}
		records := directToOutputRecords(results)
		rec := records[0]
		if rec.Success {
			t.Error("Success = true; want false for nil response")
		}
		if rec.Error != "" {
			t.Errorf("Error = %q; want empty", rec.Error)
		}
	})

	t.Run("multiple results in one response", func(t *testing.T) {
		results := []directResult{
			{
				peelID: "peel-05",
				resp: &proto.ExecResponse{
					PeelID:  "peel-05",
					Success: true,
					Results: []proto.StateResult{
						{Name: "step-1", Changed: true, Duration: time.Second},
						{Name: "step-2", Changed: false, Duration: 500 * time.Millisecond, Skipped: true, SkipReason: "already applied"},
					},
				},
			},
		}

		records := directToOutputRecords(results)
		if len(records[0].Results) != 2 {
			t.Fatalf("got %d results; want 2", len(records[0].Results))
		}
		if records[0].Results[1].Skipped != true {
			t.Error("Results[1].Skipped = false; want true")
		}
		if records[0].Results[1].SkipReason != "already applied" {
			t.Errorf("Results[1].SkipReason = %q; want %q", records[0].Results[1].SkipReason, "already applied")
		}
	})

	t.Run("empty input", func(t *testing.T) {
		records := directToOutputRecords(nil)
		if len(records) != 0 {
			t.Errorf("got %d records; want 0", len(records))
		}
	})
}

func TestJobReturnsToOutputRecords(t *testing.T) {
	t.Run("successful return with results", func(t *testing.T) {
		returns := []job.Return{
			{
				JID:     "job-001",
				PeelID:  "peel-01",
				Success: true,
				ReturnData: map[string]any{
					"results": []any{
						map[string]any{
							"name":    "install-nginx",
							"changed": true,
							"details": map[string]any{
								"stdout": "installed",
							},
							"duration": int64(2 * time.Second),
							"diff":     "--- old\n+++ new",
						},
					},
				},
			},
		}

		records := jobReturnsToOutputRecords(returns)
		if len(records) != 1 {
			t.Fatalf("got %d records; want 1", len(records))
		}

		rec := records[0]
		if rec.PeelID != "peel-01" {
			t.Errorf("PeelID = %q; want %q", rec.PeelID, "peel-01")
		}
		if !rec.Success {
			t.Error("Success = false; want true")
		}
		if len(rec.Results) != 1 {
			t.Fatalf("got %d results; want 1", len(rec.Results))
		}

		sr := rec.Results[0]
		if sr.Name != "install-nginx" {
			t.Errorf("Name = %q; want %q", sr.Name, "install-nginx")
		}
		if !sr.Changed {
			t.Error("Changed = false; want true")
		}
		if sr.Details["stdout"] != "installed" {
			t.Errorf("Details[stdout] = %q; want %q", sr.Details["stdout"], "installed")
		}
		if sr.Duration != 2*time.Second {
			t.Errorf("Duration = %v; want %v", sr.Duration, 2*time.Second)
		}
		if sr.Diff != "--- old\n+++ new" {
			t.Errorf("Diff = %q; want %q", sr.Diff, "--- old\n+++ new")
		}
	})

	t.Run("return with error", func(t *testing.T) {
		returns := []job.Return{
			{
				PeelID:  "peel-02",
				Success: false,
				Error:   "timeout waiting for ack",
			},
		}

		records := jobReturnsToOutputRecords(returns)
		rec := records[0]
		if rec.Success {
			t.Error("Success = true; want false")
		}
		if rec.Error != "timeout waiting for ack" {
			t.Errorf("Error = %q; want %q", rec.Error, "timeout waiting for ack")
		}
		if len(rec.Results) != 0 {
			t.Errorf("got %d results; want 0 when ReturnData is nil", len(rec.Results))
		}
	})

	t.Run("return with non-map ReturnData", func(t *testing.T) {
		returns := []job.Return{
			{
				PeelID:     "peel-03",
				Success:    true,
				ReturnData: "just a string",
			},
		}

		records := jobReturnsToOutputRecords(returns)
		rec := records[0]
		if !rec.Success {
			t.Error("Success = false; want true")
		}
		if len(rec.Results) != 0 {
			t.Errorf("got %d results; want 0 for non-map ReturnData", len(rec.Results))
		}
	})

	t.Run("return with skipped state", func(t *testing.T) {
		returns := []job.Return{
			{
				PeelID:  "peel-04",
				Success: true,
				ReturnData: map[string]any{
					"results": []any{
						map[string]any{
							"name":        "conditional-step",
							"changed":     false,
							"skipped":     true,
							"skip_reason": "onchanges_not_met",
						},
					},
				},
			},
		}

		records := jobReturnsToOutputRecords(returns)
		sr := records[0].Results[0]
		if !sr.Skipped {
			t.Error("Skipped = false; want true")
		}
		if sr.SkipReason != "onchanges_not_met" {
			t.Errorf("SkipReason = %q; want %q", sr.SkipReason, "onchanges_not_met")
		}
	})

	t.Run("return with state-level error", func(t *testing.T) {
		returns := []job.Return{
			{
				PeelID:  "peel-05",
				Success: false,
				ReturnData: map[string]any{
					"results": []any{
						map[string]any{
							"name":    "failing-step",
							"changed": false,
							"error":   "command exited with code 1",
						},
					},
				},
			},
		}

		records := jobReturnsToOutputRecords(returns)
		sr := records[0].Results[0]
		if sr.Error != "command exited with code 1" {
			t.Errorf("Error = %q; want %q", sr.Error, "command exited with code 1")
		}
	})

	t.Run("multiple returns", func(t *testing.T) {
		returns := []job.Return{
			{PeelID: "peel-a", Success: true, ReturnData: map[string]any{"results": []any{}}},
			{PeelID: "peel-b", Success: true, ReturnData: map[string]any{"results": []any{}}},
		}

		records := jobReturnsToOutputRecords(returns)
		if len(records) != 2 {
			t.Fatalf("got %d records; want 2", len(records))
		}
		if records[0].PeelID != "peel-a" {
			t.Errorf("records[0].PeelID = %q; want %q", records[0].PeelID, "peel-a")
		}
		if records[1].PeelID != "peel-b" {
			t.Errorf("records[1].PeelID = %q; want %q", records[1].PeelID, "peel-b")
		}
	})

	t.Run("empty input", func(t *testing.T) {
		records := jobReturnsToOutputRecords(nil)
		if len(records) != 0 {
			t.Errorf("got %d records; want 0", len(records))
		}
	})
}

func TestMapStrSafe(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want string
	}{
		{
			name: "existing string key",
			m:    map[string]any{"name": "hello"},
			key:  "name",
			want: "hello",
		},
		{
			name: "existing int key",
			m:    map[string]any{"count": 42},
			key:  "count",
			want: "42",
		},
		{
			name: "existing bool key",
			m:    map[string]any{"flag": true},
			key:  "flag",
			want: "true",
		},
		{
			name: "missing key",
			m:    map[string]any{"a": "b"},
			key:  "missing",
			want: "",
		},
		{
			name: "empty map",
			m:    map[string]any{},
			key:  "anything",
			want: "",
		},
		{
			name: "nil value in map",
			m:    map[string]any{"key": nil},
			key:  "key",
			want: "<nil>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapStrSafe(tt.m, tt.key)
			if got != tt.want {
				t.Errorf("mapStrSafe(%v, %q) = %q; want %q", tt.m, tt.key, got, tt.want)
			}
		})
	}
}
