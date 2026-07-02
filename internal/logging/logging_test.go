package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ptorbus/zester/internal/version"
)

func TestSetupJSONOutputShape(t *testing.T) {
	var buf bytes.Buffer
	logger, err := Setup(&buf, "master", "info", "json")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	logger.Info("hello", "key", "value")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, buf.String())
	}
	if rec["msg"] != "hello" {
		t.Errorf("msg = %v, want hello", rec["msg"])
	}
	if rec["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", rec["level"])
	}
	if rec["key"] != "value" {
		t.Errorf("key = %v, want value", rec["key"])
	}
	if rec["component"] != "master" {
		t.Errorf("component = %v, want master", rec["component"])
	}
	if rec["version"] != version.Version {
		t.Errorf("version = %v, want %v", rec["version"], version.Version)
	}
}

func TestSetupTextOutputShape(t *testing.T) {
	var buf bytes.Buffer
	logger, err := Setup(&buf, "peel", "info", "text")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	logger.Info("hello")

	out := buf.String()
	if strings.HasPrefix(out, "{") {
		t.Errorf("text format produced JSON-looking output: %s", out)
	}
	for _, want := range []string{"msg=hello", "level=INFO", "component=peel", "version=" + version.Version} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q: %s", want, out)
		}
	}
}

func TestSetupDefaultsAreJSONInfo(t *testing.T) {
	var buf bytes.Buffer
	logger, err := Setup(&buf, "watchdog", "", "")
	if err != nil {
		t.Fatalf("Setup with empty level/format: %v", err)
	}

	logger.Debug("filtered")
	if buf.Len() != 0 {
		t.Errorf("debug record emitted at default (info) level: %s", buf.String())
	}

	logger.Info("kept")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("default format is not JSON: %v\noutput: %s", err, buf.String())
	}
	if rec["msg"] != "kept" {
		t.Errorf("msg = %v, want kept", rec["msg"])
	}
}

func TestSetupLevelFiltering(t *testing.T) {
	tests := []struct {
		level    string
		emitted  []string // messages expected in output
		filtered []string // messages expected to be dropped
	}{
		{"debug", []string{"dbg", "inf", "wrn", "err"}, nil},
		{"info", []string{"inf", "wrn", "err"}, []string{"dbg"}},
		{"warn", []string{"wrn", "err"}, []string{"dbg", "inf"}},
		{"error", []string{"err"}, []string{"dbg", "inf", "wrn"}},
	}
	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			var buf bytes.Buffer
			logger, err := Setup(&buf, "c", tt.level, "text")
			if err != nil {
				t.Fatalf("Setup(%q): %v", tt.level, err)
			}
			logger.Debug("dbg")
			logger.Info("inf")
			logger.Warn("wrn")
			logger.Error("err")

			out := buf.String()
			for _, msg := range tt.emitted {
				if !strings.Contains(out, "msg="+msg) {
					t.Errorf("level %s: expected %q in output: %s", tt.level, msg, out)
				}
			}
			for _, msg := range tt.filtered {
				if strings.Contains(out, "msg="+msg) {
					t.Errorf("level %s: %q should have been filtered: %s", tt.level, msg, out)
				}
			}
		})
	}
}

func TestSetupCaseInsensitiveAndTrimmed(t *testing.T) {
	var buf bytes.Buffer
	if _, err := Setup(&buf, "c", " WARN ", " TEXT "); err != nil {
		t.Fatalf("Setup with mixed-case padded values: %v", err)
	}
}

func TestSetupInvalidLevel(t *testing.T) {
	var buf bytes.Buffer
	_, err := Setup(&buf, "c", "verbose", "json")
	if err == nil {
		t.Fatal("expected error for invalid level")
	}
	for _, valid := range Levels() {
		if !strings.Contains(err.Error(), valid) {
			t.Errorf("error should list valid level %q: %v", valid, err)
		}
	}
	if !strings.Contains(err.Error(), "verbose") {
		t.Errorf("error should mention the invalid value: %v", err)
	}
}

func TestSetupInvalidFormat(t *testing.T) {
	var buf bytes.Buffer
	_, err := Setup(&buf, "c", "info", "xml")
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
	for _, valid := range Formats() {
		if !strings.Contains(err.Error(), valid) {
			t.Errorf("error should list valid format %q: %v", valid, err)
		}
	}
	if !strings.Contains(err.Error(), "xml") {
		t.Errorf("error should mention the invalid value: %v", err)
	}
}

func TestParseLevel(t *testing.T) {
	for _, lvl := range Levels() {
		if _, err := ParseLevel(lvl); err != nil {
			t.Errorf("ParseLevel(%q): %v", lvl, err)
		}
	}
	if _, err := ParseLevel("nope"); err == nil {
		t.Error("ParseLevel should reject invalid values")
	}
}

func TestLevelsAndFormatsLists(t *testing.T) {
	if got := Levels(); len(got) != 4 {
		t.Errorf("Levels() = %v, want 4 entries", got)
	}
	if got := Formats(); len(got) != 2 {
		t.Errorf("Formats() = %v, want 2 entries", got)
	}
}
