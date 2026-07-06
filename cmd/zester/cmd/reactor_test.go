package cmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/reactor"
)

// ---------- buildReactorTestData ----------

func TestBuildReactorTestData(t *testing.T) {
	tests := []struct {
		name      string
		eventJSON string
		dataPairs []string
		want      map[string]any
		wantErr   bool
	}{
		{name: "nothing given", want: nil},
		{
			name:      "event-json only",
			eventJSON: `{"service":"nginx","running":false}`,
			want:      map[string]any{"service": "nginx", "running": false},
		},
		{
			name:      "data pairs only",
			dataPairs: []string{"version=1.2.3"},
			want:      map[string]any{"version": "1.2.3"},
		},
		{
			name:      "data overrides event-json",
			eventJSON: `{"service":"nginx","env":"dev"}`,
			dataPairs: []string{"env=prod"},
			want:      map[string]any{"service": "nginx", "env": "prod"},
		},
		{name: "invalid json", eventJSON: `{not-json`, wantErr: true},
		{name: "json array rejected", eventJSON: `["a"]`, wantErr: true},
		{name: "json null rejected", eventJSON: `null`, wantErr: true},
		{name: "malformed data pair", dataPairs: []string{"oops"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildReactorTestData(tt.eventJSON, tt.dataPairs)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("buildReactorTestData(%q, %v) = %v, want error", tt.eventJSON, tt.dataPairs, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildReactorTestData(%q, %v): %v", tt.eventJSON, tt.dataPairs, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildReactorTestData(%q, %v) = %v, want %v", tt.eventJSON, tt.dataPairs, got, tt.want)
			}
		})
	}
}

// ---------- reactorTestToOutput ----------

func TestReactorTestToOutput(t *testing.T) {
	resp := reactor.TestResponse{Matched: []reactor.MatchedRule{
		{Rule: "reactor.restart_service", Actions: []string{"dispatch service.restart"}},
		{Rule: "reactor.broken", Errors: []string{"render: boom"}},
	}}

	out := reactorTestToOutput("web-01/myco/x", resp)
	if out.Key != "web-01/myco/x" {
		t.Errorf("key = %q, want web-01/myco/x", out.Key)
	}
	if len(out.Matched) != 2 {
		t.Fatalf("matched = %d entries, want 2", len(out.Matched))
	}
	if out.Matched[0].Rule != "reactor.restart_service" || out.Matched[1].Errors[0] != "render: boom" {
		t.Errorf("matched not mirrored: %+v", out.Matched)
	}

	// Empty response still yields a non-nil slice so JSON renders [].
	empty := reactorTestToOutput("k", reactor.TestResponse{})
	if empty.Matched == nil {
		t.Error("Matched is nil for an empty response, want empty slice")
	}
}

// ---------- formatReactorTestText ----------

func TestFormatReactorTestText_NoMatches(t *testing.T) {
	got := formatReactorTestText("web-01/myco/x", nil, false)
	if !strings.Contains(got, "No reactor rules matched") || !strings.Contains(got, `"web-01/myco/x"`) {
		t.Errorf("no-match output = %q", got)
	}
}

func TestFormatReactorTestText_MatchedRules(t *testing.T) {
	matched := []reactor.MatchedRule{
		{
			Rule: "reactor.restart_service",
			Actions: []string{
				`dispatch service.restart target="web-01" timeout=1m0s`,
				`log "restarted"`,
			},
		},
		{Rule: "reactor.broken", Errors: []string{"render: undefined variable"}},
		{Rule: "reactor.empty"},
	}

	got := formatReactorTestText("web-01/beacon/web-01/service", matched, false)

	if !strings.Contains(got, `3 rule(s) matched key "web-01/beacon/web-01/service"`) {
		t.Errorf("output missing match count header:\n%s", got)
	}
	if !strings.Contains(got, "reactor.restart_service:") {
		t.Errorf("output missing rule ref:\n%s", got)
	}
	if !strings.Contains(got, `- dispatch service.restart target="web-01" timeout=1m0s`) {
		t.Errorf("output missing action summary:\n%s", got)
	}
	if !strings.Contains(got, "ERROR: render: undefined variable") {
		t.Errorf("output missing rule error:\n%s", got)
	}
	if !strings.Contains(got, "(no actions)") {
		t.Errorf("output missing empty-rule marker:\n%s", got)
	}
}

func TestFormatReactorTestText_Color(t *testing.T) {
	matched := []reactor.MatchedRule{
		{Rule: "reactor.ok", Actions: []string{"log \"x\""}},
		{Rule: "reactor.bad", Errors: []string{"boom"}},
	}

	got := formatReactorTestText("k/t", matched, true)
	if !strings.Contains(got, colorGreen+"reactor.ok"+colorReset) {
		t.Errorf("clean rule not green:\n%q", got)
	}
	if !strings.Contains(got, colorRed+"reactor.bad"+colorReset) {
		t.Errorf("erroring rule not red:\n%q", got)
	}

	plain := formatReactorTestText("k/t", matched, false)
	if strings.Contains(plain, "\033[") {
		t.Errorf("no-color output contains ANSI codes:\n%q", plain)
	}
}
