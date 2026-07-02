package cmd

import (
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// ---------- parseModuleArgs ----------

func TestParseModuleArgs_CmdRun(t *testing.T) {
	id, args, err := parseModuleArgs("cmd.run", []string{"hostname"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "ad-hoc" {
		t.Errorf("id = %q, want %q", id, "ad-hoc")
	}
	if args["command"] != "hostname" {
		t.Errorf("args[command] = %v, want %q", args["command"], "hostname")
	}
}

func TestParseModuleArgs_CmdRunWithKV(t *testing.T) {
	id, args, err := parseModuleArgs("cmd.run", []string{"echo hello", "env=prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "ad-hoc" {
		t.Errorf("id = %q, want %q", id, "ad-hoc")
	}
	if args["command"] != "echo hello" {
		t.Errorf("args[command] = %v, want %q", args["command"], "echo hello")
	}
	if args["env"] != "prod" {
		t.Errorf("args[env] = %v, want %q", args["env"], "prod")
	}
}

func TestParseModuleArgs_CmdRunMissing(t *testing.T) {
	_, _, err := parseModuleArgs("cmd.run", nil)
	if err == nil {
		t.Fatal("expected error for missing cmd.run argument")
	}
}

func TestParseModuleArgs_TestPing(t *testing.T) {
	id, args, err := parseModuleArgs("test.ping", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "ping" {
		t.Errorf("id = %q, want %q", id, "ping")
	}
	if len(args) != 0 {
		t.Errorf("args should be empty, got %v", args)
	}
}

func TestParseModuleArgs_FactsItems(t *testing.T) {
	id, _, err := parseModuleArgs("facts.items", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "items" {
		t.Errorf("id = %q, want %q", id, "items")
	}
}

func TestParseModuleArgs_FactsGet(t *testing.T) {
	id, args, err := parseModuleArgs("facts.get", []string{"os.family"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "os.family" {
		t.Errorf("id = %q, want %q", id, "os.family")
	}
	if args["key"] != "os.family" {
		t.Errorf("args[key] = %v, want %q", args["key"], "os.family")
	}
}

func TestParseModuleArgs_FactsGetMissing(t *testing.T) {
	_, _, err := parseModuleArgs("facts.get", nil)
	if err == nil {
		t.Fatal("expected error for missing facts.get key")
	}
}

func TestParseModuleArgs_FactsKeys(t *testing.T) {
	id, _, err := parseModuleArgs("facts.keys", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "keys" {
		t.Errorf("id = %q, want %q", id, "keys")
	}
}

func TestParseModuleArgs_FactsSet(t *testing.T) {
	id, args, err := parseModuleArgs("facts.set", []string{"region", "us-east-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "region" {
		t.Errorf("id = %q, want %q", id, "region")
	}
	if args["key"] != "region" {
		t.Errorf("args[key] = %v, want %q", args["key"], "region")
	}
	if args["value"] != "us-east-1" {
		t.Errorf("args[value] = %v, want %q", args["value"], "us-east-1")
	}
}

func TestParseModuleArgs_FactsSetMissing(t *testing.T) {
	_, _, err := parseModuleArgs("facts.set", []string{"region"})
	if err == nil {
		t.Fatal("expected error for missing facts.set value")
	}

	_, _, err = parseModuleArgs("facts.set", nil)
	if err == nil {
		t.Fatal("expected error for missing facts.set key and value")
	}
}

func TestParseModuleArgs_SettingsItems(t *testing.T) {
	id, _, err := parseModuleArgs("settings.items", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "items" {
		t.Errorf("id = %q, want %q", id, "items")
	}
}

func TestParseModuleArgs_SettingsGet(t *testing.T) {
	id, args, err := parseModuleArgs("settings.get", []string{"db.host"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "db.host" {
		t.Errorf("id = %q, want %q", id, "db.host")
	}
	if args["key"] != "db.host" {
		t.Errorf("args[key] = %v, want %q", args["key"], "db.host")
	}
}

func TestParseModuleArgs_SettingsGetMissing(t *testing.T) {
	_, _, err := parseModuleArgs("settings.get", nil)
	if err == nil {
		t.Fatal("expected error for missing settings.get key")
	}
}

func TestParseModuleArgs_SettingsKeys(t *testing.T) {
	id, _, err := parseModuleArgs("settings.keys", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "keys" {
		t.Errorf("id = %q, want %q", id, "keys")
	}
}

func TestParseModuleArgs_FileManaged(t *testing.T) {
	id, args, err := parseModuleArgs("file.managed", []string{"/etc/nginx/nginx.conf", "source=/srv/nginx.conf"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "/etc/nginx/nginx.conf" {
		t.Errorf("id = %q, want %q", id, "/etc/nginx/nginx.conf")
	}
	if args["path"] != "/etc/nginx/nginx.conf" {
		t.Errorf("args[path] = %v, want %q", args["path"], "/etc/nginx/nginx.conf")
	}
	if args["source"] != "/srv/nginx.conf" {
		t.Errorf("args[source] = %v, want %q", args["source"], "/srv/nginx.conf")
	}
}

func TestParseModuleArgs_FileManagedMissing(t *testing.T) {
	_, _, err := parseModuleArgs("file.managed", nil)
	if err == nil {
		t.Fatal("expected error for missing file.managed path")
	}
}

func TestParseModuleArgs_PkgInstalled(t *testing.T) {
	id, args, err := parseModuleArgs("pkg.installed", []string{"nginx", "version=1.25"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "nginx" {
		t.Errorf("id = %q, want %q", id, "nginx")
	}
	if args["name"] != "nginx" {
		t.Errorf("args[name] = %v, want %q", args["name"], "nginx")
	}
	if args["version"] != "1.25" {
		t.Errorf("args[version] = %v, want %q", args["version"], "1.25")
	}
}

func TestParseModuleArgs_PkgInstalledMissing(t *testing.T) {
	_, _, err := parseModuleArgs("pkg.installed", nil)
	if err == nil {
		t.Fatal("expected error for missing pkg.installed name")
	}
}

func TestParseModuleArgs_StateApply(t *testing.T) {
	id, args, err := parseModuleArgs("state.apply", []string{"webserver"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "webserver" {
		t.Errorf("id = %q, want %q", id, "webserver")
	}
	if args["state"] != "webserver" {
		t.Errorf("args[state] = %v, want %q", args["state"], "webserver")
	}
}

func TestParseModuleArgs_StateApplyMissing(t *testing.T) {
	_, _, err := parseModuleArgs("state.apply", nil)
	if err == nil {
		t.Fatal("expected error for missing state.apply name")
	}
}

func TestParseModuleArgs_StateHighstate(t *testing.T) {
	id, args, err := parseModuleArgs("state.highstate", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "highstate" {
		t.Errorf("id = %q, want %q", id, "highstate")
	}
	if len(args) != 0 {
		t.Errorf("args should be empty, got %v", args)
	}
}

func TestParseModuleArgs_StateHighstateWithKV(t *testing.T) {
	id, args, err := parseModuleArgs("state.highstate", []string{"env=staging"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "highstate" {
		t.Errorf("id = %q, want %q", id, "highstate")
	}
	if args["env"] != "staging" {
		t.Errorf("args[env] = %v, want %q", args["env"], "staging")
	}
}

func TestParseModuleArgs_GenericFallbackWithArgs(t *testing.T) {
	id, args, err := parseModuleArgs("custom.module", []string{"my-id", "key=value"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "my-id" {
		t.Errorf("id = %q, want %q", id, "my-id")
	}
	if args["key"] != "value" {
		t.Errorf("args[key] = %v, want %q", args["key"], "value")
	}
}

func TestParseModuleArgs_GenericFallbackNoArgs(t *testing.T) {
	id, args, err := parseModuleArgs("custom.module", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "ad-hoc" {
		t.Errorf("id = %q, want %q", id, "ad-hoc")
	}
	if len(args) != 0 {
		t.Errorf("args should be empty, got %v", args)
	}
}

// ---------- moduleTimeout ----------

func newTimeoutCmd(setFlag bool, val time.Duration) *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Duration("timeout", 60*time.Second, "execution timeout")
	if setFlag {
		_ = cmd.Flags().Set("timeout", val.String())
	}
	return cmd
}

func TestModuleTimeout_Defaults(t *testing.T) {
	tests := []struct {
		module string
		want   time.Duration
	}{
		{"state.apply", 5 * time.Minute},
		{"state.highstate", 10 * time.Minute},
		{"cmd.run", 60 * time.Second},
		{"test.ping", 60 * time.Second},
		{"facts.get", 60 * time.Second},
		{"custom.module", 60 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.module, func(t *testing.T) {
			cmd := newTimeoutCmd(false, 0)
			got := moduleTimeout(cmd, tt.module)
			if got != tt.want {
				t.Errorf("moduleTimeout(%q) = %v, want %v", tt.module, got, tt.want)
			}
		})
	}
}

func TestModuleTimeout_ExplicitOverride(t *testing.T) {
	// Even state.apply should respect an explicit --timeout flag.
	cmd := newTimeoutCmd(true, 30*time.Second)
	got := moduleTimeout(cmd, "state.apply")
	if got != 30*time.Second {
		t.Errorf("moduleTimeout with explicit flag = %v, want 30s", got)
	}

	// And for state.highstate.
	cmd2 := newTimeoutCmd(true, 2*time.Minute)
	got2 := moduleTimeout(cmd2, "state.highstate")
	if got2 != 2*time.Minute {
		t.Errorf("moduleTimeout with explicit flag = %v, want 2m", got2)
	}
}

// ---------- parseKeyValues ----------

func TestParseKeyValues(t *testing.T) {
	args := make(map[string]any)
	parseKeyValues([]string{"env=prod", "region=us-east-1", "count=3"}, args)

	if args["env"] != "prod" {
		t.Errorf("args[env] = %v, want %q", args["env"], "prod")
	}
	if args["region"] != "us-east-1" {
		t.Errorf("args[region] = %v, want %q", args["region"], "us-east-1")
	}
	if args["count"] != "3" {
		t.Errorf("args[count] = %v, want %q", args["count"], "3")
	}
}

func TestParseKeyValues_NoEquals(t *testing.T) {
	args := make(map[string]any)
	parseKeyValues([]string{"notapair", "key=value"}, args)

	if _, ok := args["notapair"]; ok {
		t.Error("non-key=value string should not be added to args")
	}
	if args["key"] != "value" {
		t.Errorf("args[key] = %v, want %q", args["key"], "value")
	}
}

func TestParseKeyValues_EmptyValue(t *testing.T) {
	args := make(map[string]any)
	parseKeyValues([]string{"flag="}, args)

	if args["flag"] != "" {
		t.Errorf("args[flag] = %v, want empty string", args["flag"])
	}
}

func TestParseKeyValues_ValueWithEquals(t *testing.T) {
	args := make(map[string]any)
	parseKeyValues([]string{"conn=host=db port=5432"}, args)

	// strings.Cut splits on first "=", so value is "host=db port=5432"
	if args["conn"] != "host=db port=5432" {
		t.Errorf("args[conn] = %v, want %q", args["conn"], "host=db port=5432")
	}
}

func TestParseKeyValues_Empty(t *testing.T) {
	args := make(map[string]any)
	parseKeyValues(nil, args)
	if len(args) != 0 {
		t.Errorf("expected empty args, got %v", args)
	}
}

// ---------- isGlob ----------

func TestIsGlob(t *testing.T) {
	tests := []struct {
		pattern string
		want    bool
	}{
		{"*", true},
		{"web-*", true},
		{"web-?1", true},
		{"[abc]", true},
		{"web-01", false},
		{"my.host.com", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			if got := isGlob(tt.pattern); got != tt.want {
				t.Errorf("isGlob(%q) = %v, want %v", tt.pattern, got, tt.want)
			}
		})
	}
}

// ---------- matchGlob ----------

func TestMatchGlob(t *testing.T) {
	candidates := []string{"web-01", "web-02", "db-01", "web-staging", "cache-01"}

	matched, err := matchGlob("web-*", candidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"web-01", "web-02", "web-staging"}
	if len(matched) != len(want) {
		t.Fatalf("matched %v, want %v", matched, want)
	}
	for i, m := range matched {
		if m != want[i] {
			t.Errorf("matched[%d] = %q, want %q", i, m, want[i])
		}
	}
}

func TestMatchGlob_Star(t *testing.T) {
	candidates := []string{"web-01", "db-01"}
	matched, err := matchGlob("*", candidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matched) != 2 {
		t.Errorf("expected 2 matches, got %d", len(matched))
	}
}

func TestMatchGlob_NoMatch(t *testing.T) {
	candidates := []string{"web-01", "db-01"}
	matched, err := matchGlob("cache-*", candidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matched) != 0 {
		t.Errorf("expected 0 matches, got %v", matched)
	}
}

func TestMatchGlob_QuestionMark(t *testing.T) {
	candidates := []string{"web-01", "web-02", "web-10"}
	matched, err := matchGlob("web-0?", candidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"web-01", "web-02"}
	if len(matched) != len(want) {
		t.Fatalf("matched %v, want %v", matched, want)
	}
}

func TestMatchGlob_InvalidPattern(t *testing.T) {
	_, err := matchGlob("[invalid", []string{"a"})
	if err == nil {
		t.Fatal("expected error for invalid glob pattern")
	}
}

func TestMatchGlob_EmptyCandidates(t *testing.T) {
	matched, err := matchGlob("*", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matched) != 0 {
		t.Errorf("expected 0 matches, got %v", matched)
	}
}
