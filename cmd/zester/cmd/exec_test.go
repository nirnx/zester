package cmd

import (
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/target"
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

// TestParseModuleArgs_CmdRunKeyValueForms pins the review-round-4 P1 fix:
// a Salt-style `cmd.run name=<cmd>` (or command=/cmd=) invocation is key=value
// form — the assignment must NOT become the literal command string — while a
// positional command that merely CONTAINS '=' (env-prefix style) stays the
// verbatim command.
func TestParseModuleArgs_CmdRunKeyValueForms(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantCmd string
		extra   map[string]any
	}{
		{"name= form (Salt parity)", []string{"name=echo hi"}, "echo hi", nil},
		{"command= form", []string{"command=uptime"}, "uptime", nil},
		{"cmd= form", []string{"cmd=uptime", "env=prod"}, "uptime", map[string]any{"env": "prod"}},
		{"positional with embedded =", []string{"FOO=bar env"}, "FOO=bar env", nil},
		{"positional then kv", []string{"echo a=b", "shell=/bin/sh"}, "echo a=b", map[string]any{"shell": "/bin/sh"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, args, err := parseModuleArgs("cmd.run", tc.args)
			if err != nil {
				t.Fatalf("parseModuleArgs: %v", err)
			}
			if id != "ad-hoc" {
				t.Errorf("id = %q, want ad-hoc", id)
			}
			key := "command"
			if _, viaName := args["name"]; viaName {
				key = "name"
			}
			if _, viaCmd := args["cmd"]; viaCmd {
				key = "cmd"
			}
			if got := args[key]; got != tc.wantCmd {
				t.Errorf("args[%q] = %v, want %q (full args: %v)", key, got, tc.wantCmd, args)
			}
			for k, v := range tc.extra {
				if args[k] != v {
					t.Errorf("args[%q] = %v, want %v", k, args[k], v)
				}
			}
		})
	}
}

// TestParseModuleArgs_PillarAliases pins the round-5 critic fix: pillar.* is
// the peel's Salt-compat alias of settings.*, answered by the same handler
// that reads only args["key"] — so the CLI must bind the keyed form for both
// spellings (pillar.get previously fell through to the generic fallback with
// empty args and the peel errored).
func TestParseModuleArgs_PillarAliases(t *testing.T) {
	id, args, err := parseModuleArgs("pillar.get", []string{"app.port"})
	if err != nil {
		t.Fatalf("pillar.get: %v", err)
	}
	if id != "app.port" || args["key"] != "app.port" {
		t.Errorf("pillar.get: id=%q args=%v, want key bound", id, args)
	}
	if _, _, err := parseModuleArgs("pillar.get", nil); err == nil {
		t.Error("pillar.get without a key must be a usage error")
	}
	if id, _, err := parseModuleArgs("pillar.items", nil); err != nil || id != "items" {
		t.Errorf("pillar.items: id=%q err=%v", id, err)
	}
	if id, _, err := parseModuleArgs("pillar.keys", nil); err != nil || id != "keys" {
		t.Errorf("pillar.keys: id=%q err=%v", id, err)
	}
}

// TestParseModuleArgs_DocdataKeyValueGuard pins the generalized round-4 fix
// (round-5 critic): a first token assigning to a DECLARED parameter key makes
// the whole invocation key=value form for every docdata module — previously
// `pkg.installed name=nginx` bound the literal string "name=nginx" as the
// package name. An assignment to an UNDECLARED key stays positional.
func TestParseModuleArgs_DocdataKeyValueGuard(t *testing.T) {
	// Primary via its canonical key.
	id, args, err := parseModuleArgs("pkg.installed", []string{"name=nginx"})
	if err != nil {
		t.Fatalf("pkg.installed name=: %v", err)
	}
	if id != "nginx" || args["name"] != "nginx" {
		t.Errorf("pkg.installed name=: id=%q args=%v, want nginx/name:nginx", id, args)
	}

	// Primary via a registered ALIAS; the ID follows the alias value.
	id, args, err = parseModuleArgs("file.managed", []string{"path=/etc/motd", "contents=hi"})
	if err != nil {
		t.Fatalf("file.managed path=: %v", err)
	}
	if id != "/etc/motd" || args["path"] != "/etc/motd" || args["contents"] != "hi" {
		t.Errorf("file.managed path=: id=%q args=%v", id, args)
	}

	// Declared non-primary key with a default-less primary absent: usage error,
	// not a state named "ad-hoc".
	if _, _, err := parseModuleArgs("pkg.installed", []string{"version=1.2"}); err == nil {
		t.Error("pkg.installed version= without a name must be a usage error")
	}

	// Undeclared key stays a positional value (only the schema's own key set
	// switches modes).
	id, args, err = parseModuleArgs("pkg.installed", []string{"foo=bar"})
	if err != nil {
		t.Fatalf("pkg.installed foo=: %v", err)
	}
	if id != "foo=bar" || args["name"] != "foo=bar" {
		t.Errorf("pkg.installed foo=: id=%q args=%v, want verbatim positional", id, args)
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
	// file.managed now resolves through the embedded docdata (Phase-4
	// unification): the bare positional binds to its DECLARED primary "name",
	// not the historical "path". These are decode-identical on a migrated peel
	// ("path" is a registered alias of "name" and the ID carries the same
	// value), so the observable dispatch is unchanged.
	if args["name"] != "/etc/nginx/nginx.conf" {
		t.Errorf("args[name] = %v, want %q", args["name"], "/etc/nginx/nginx.conf")
	}
	if _, ok := args["path"]; ok {
		t.Errorf("args[path] should be unset (primary is now name); got %v", args["path"])
	}
	if args["source"] != "/srv/nginx.conf" {
		t.Errorf("args[source] = %v, want %q", args["source"], "/srv/nginx.conf")
	}
}

// The "path" alias still works when passed as an explicit key=value token
// (cliargs stores it verbatim; the migrated file.managed reads it as an alias
// of "name").
func TestParseModuleArgs_FileManagedPathAliasExplicit(t *testing.T) {
	// A bogus positional plus an explicit path= override: the positional binds
	// to the primary and path= is carried through untouched for the peel's
	// alias resolution.
	_, args, err := parseModuleArgs("file.managed", []string{"placeholder", "path=/etc/real.conf"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args["path"] != "/etc/real.conf" {
		t.Errorf("args[path] = %v, want %q", args["path"], "/etc/real.conf")
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

// Salt parity: `state.apply` without a state name is the highstate — bare, in
// key=value-only form (`state.apply test=true` is the classic highstate dry
// run), or with an explicit empty state=. An explicit state= assignment still
// selects that state.
func TestParseModuleArgs_StateApplyHighstateAlias(t *testing.T) {
	t.Run("bare", func(t *testing.T) {
		id, args, err := parseModuleArgs("state.apply", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "highstate" {
			t.Errorf("id = %q, want %q", id, "highstate")
		}
		if _, ok := args["state"]; ok {
			t.Errorf("args[state] = %v, want absent", args["state"])
		}
	})

	t.Run("test=true only", func(t *testing.T) {
		id, args, err := parseModuleArgs("state.apply", []string{"test=true"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "highstate" {
			t.Errorf("id = %q, want %q (a key=value token is not a state name)", id, "highstate")
		}
		if _, ok := args["test"]; !ok {
			t.Error("args[test] missing; key=value form must still parse")
		}
	})

	t.Run("explicit state=", func(t *testing.T) {
		id, args, err := parseModuleArgs("state.apply", []string{"state=webserver", "test=true"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "webserver" {
			t.Errorf("id = %q, want %q", id, "webserver")
		}
		if args["state"] != "webserver" {
			t.Errorf("args[state] = %v, want %q", args["state"], "webserver")
		}
	})

	t.Run("mods= Salt kwarg", func(t *testing.T) {
		id, args, err := parseModuleArgs("state.apply", []string{"mods=webserver"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "webserver" {
			t.Errorf("id = %q, want %q", id, "webserver")
		}
		if args["mods"] != "webserver" {
			t.Errorf("args[mods] = %v, want %q", args["mods"], "webserver")
		}
	})

	// Only the DECLARED keys (state/mods/test) switch to key=value mode: an
	// arbitrary assignment — a staet= typo, a misordered env=prod — stays a
	// positional state name and fails loudly at compile time on the peel,
	// never silently escalating to a full highstate.
	t.Run("undeclared assignment stays positional", func(t *testing.T) {
		id, args, err := parseModuleArgs("state.apply", []string{"staet=webserver"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "staet=webserver" || args["state"] != "staet=webserver" {
			t.Errorf("id = %q args[state] = %v, want the literal token as the state name", id, args["state"])
		}
	})

	// A bare token after key=value arguments is rejected outright —
	// ParseKeyValues would silently drop it, and a dropped state name must
	// never turn into a fleet-wide highstate.
	t.Run("stray positional after key=value errors", func(t *testing.T) {
		_, _, err := parseModuleArgs("state.apply", []string{"test=true", "webserver"})
		if err == nil {
			t.Fatal("expected an error for a stray positional after key=value arguments")
		}
	})
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

// TestParseModuleArgs_UnifiedParity pins the Phase-4 unification: after
// replacing the hand-maintained per-module positional table with docdata-driven
// primary-parameter lookup, EVERY module that was previously hard-coded (plus
// the generic fallback) resolves to the same (id, args). For all of them the
// result is byte-identical to the legacy table EXCEPT file.managed, whose
// positional now binds to its canonical primary "name" instead of "path"
// (decode-identical via the "path" alias + the carried ID). This table is the
// regression guard for that equivalence.
func TestParseModuleArgs_UnifiedParity(t *testing.T) {
	tests := []struct {
		name      string
		module    string
		remaining []string
		wantID    string
		wantArgs  map[string]any
	}{
		// Special-cased query/dispatch modules + cmd.run (unchanged path).
		{"cmd.run", "cmd.run", []string{"hostname"}, "ad-hoc", map[string]any{"command": "hostname"}},
		{"cmd.run kv", "cmd.run", []string{"echo hi", "env=prod"}, "ad-hoc", map[string]any{"command": "echo hi", "env": "prod"}},
		{"test.ping", "test.ping", nil, "ping", map[string]any{}},
		{"facts.items", "facts.items", nil, "items", map[string]any{}},
		{"facts.get", "facts.get", []string{"os.family"}, "os.family", map[string]any{"key": "os.family"}},
		{"facts.keys", "facts.keys", nil, "keys", map[string]any{}},
		{"facts.set", "facts.set", []string{"region", "us-east-1"}, "region", map[string]any{"key": "region", "value": "us-east-1"}},
		{"settings.items", "settings.items", nil, "items", map[string]any{}},
		{"settings.get", "settings.get", []string{"db.host"}, "db.host", map[string]any{"key": "db.host"}},
		{"settings.keys", "settings.keys", nil, "keys", map[string]any{}},
		{"state.apply", "state.apply", []string{"webserver"}, "webserver", map[string]any{"state": "webserver"}},
		{"state.apply bare (highstate alias)", "state.apply", nil, "highstate", map[string]any{}},
		{"state.highstate", "state.highstate", nil, "highstate", map[string]any{}},
		{"state.highstate kv", "state.highstate", []string{"env=staging"}, "highstate", map[string]any{"env": "staging"}},

		// Docdata-driven (primary == "name"): pkg.installed is byte-identical to
		// the legacy table.
		{"pkg.installed", "pkg.installed", []string{"nginx", "version=1.25"}, "nginx", map[string]any{"name": "nginx", "version": "1.25"}},
		// file.managed: canonical primary "name" (was legacy "path").
		{"file.managed", "file.managed", []string{"/etc/n.conf", "source=/srv/n.conf"}, "/etc/n.conf", map[string]any{"name": "/etc/n.conf", "source": "/srv/n.conf"}},

		// Generic fallback (module absent from docdata).
		{"generic with args", "custom.module", []string{"my-id", "key=value"}, "my-id", map[string]any{"key": "value"}},
		{"generic no args", "custom.module", nil, "ad-hoc", map[string]any{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, args, err := parseModuleArgs(tt.module, tt.remaining)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tt.wantID {
				t.Errorf("id = %q, want %q", id, tt.wantID)
			}
			if len(args) != len(tt.wantArgs) {
				t.Fatalf("args = %v, want %v", args, tt.wantArgs)
			}
			for k, want := range tt.wantArgs {
				if got := args[k]; got != want {
					t.Errorf("args[%q] = %v, want %v", k, got, want)
				}
			}
		})
	}
}

// TestParseModuleArgs_DocdataDrivenModules covers self-documenting modules that
// were NOT previously hard-coded: they now bind the positional to their primary
// ("name") for free, and a missing positional (default-less primary) is a clear
// usage error rather than a silent "ad-hoc" dispatch.
func TestParseModuleArgs_DocdataDrivenModules(t *testing.T) {
	for _, module := range []string{"pkg.removed", "pkg.latest", "pkg.purged", "service.running", "service.dead", "user.present"} {
		t.Run(module+" positional", func(t *testing.T) {
			id, args, err := parseModuleArgs(module, []string{"nginx"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != "nginx" {
				t.Errorf("id = %q, want %q", id, "nginx")
			}
			if args["name"] != "nginx" {
				t.Errorf("args[name] = %v, want %q", args["name"], "nginx")
			}
		})
		t.Run(module+" missing", func(t *testing.T) {
			if _, _, err := parseModuleArgs(module, nil); err == nil {
				t.Fatalf("expected error for missing %s primary argument", module)
			}
		})
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
		name   string
		module string
		args   map[string]any
		want   time.Duration
	}{
		{"state.apply named", "state.apply", map[string]any{"state": "webserver"}, 5 * time.Minute},
		// Bare state.apply IS a highstate (Salt parity) — highstate budget.
		{"state.apply bare", "state.apply", map[string]any{}, 10 * time.Minute},
		{"state.apply empty state", "state.apply", map[string]any{"state": ""}, 10 * time.Minute},
		{"state.highstate", "state.highstate", nil, 10 * time.Minute},
		{"cmd.run", "cmd.run", nil, 60 * time.Second},
		{"test.ping", "test.ping", nil, 60 * time.Second},
		{"facts.get", "facts.get", nil, 60 * time.Second},
		{"custom.module", "custom.module", nil, 60 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newTimeoutCmd(false, 0)
			got := moduleTimeout(cmd, tt.module, tt.args)
			if got != tt.want {
				t.Errorf("moduleTimeout(%q, %v) = %v, want %v", tt.module, tt.args, got, tt.want)
			}
		})
	}
}

func TestModuleTimeout_ExplicitOverride(t *testing.T) {
	// Even state.apply should respect an explicit --timeout flag.
	cmd := newTimeoutCmd(true, 30*time.Second)
	got := moduleTimeout(cmd, "state.apply", map[string]any{"state": "webserver"})
	if got != 30*time.Second {
		t.Errorf("moduleTimeout with explicit flag = %v, want 30s", got)
	}

	// And for state.highstate.
	cmd2 := newTimeoutCmd(true, 2*time.Minute)
	got2 := moduleTimeout(cmd2, "state.highstate", nil)
	if got2 != 2*time.Minute {
		t.Errorf("moduleTimeout with explicit flag = %v, want 2m", got2)
	}
}

// The parseKeyValues tests moved to pkg/cliargs alongside the relocated
// ParseKeyValues function (Tranche 0B); parseModuleArgs above still exercises it
// end-to-end here via cliargs.ParseKeyValues.

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

// ---------- direct-mode glob matching (target.GlobMatcher) ----------

// Direct mode routes glob resolution through target.NewGlobMatcher, sharing
// job mode's semantics — including the dotted-hostname normalization, so a
// target written as 'web01.pl' matches the wire id 'web01_pl'.
func TestDirectGlobMatching(t *testing.T) {
	candidates := []string{"web-01", "web-02", "db-01", "web-staging", "web01_pl"}

	match := func(pattern string) []string {
		t.Helper()
		m, err := target.NewGlobMatcher(pattern)
		if err != nil {
			t.Fatalf("NewGlobMatcher(%q): %v", pattern, err)
		}
		var out []string
		for _, c := range candidates {
			if m.Match(c, nil) {
				out = append(out, c)
			}
		}
		return out
	}

	if got := match("web-*"); len(got) != 3 {
		t.Errorf("web-*: got %v, want 3 matches", got)
	}
	if got := match("*"); len(got) != len(candidates) {
		t.Errorf("*: got %v, want all", got)
	}
	if got := match("web-0?"); len(got) != 2 {
		t.Errorf("web-0?: got %v, want 2 matches", got)
	}
	if got := match("cache-*"); len(got) != 0 {
		t.Errorf("cache-*: got %v, want none", got)
	}
	// The dotted human form matches the sanitized wire id.
	if got := match("web01.pl"); len(got) != 1 || got[0] != "web01_pl" {
		t.Errorf("web01.pl: got %v, want [web01_pl]", got)
	}

	if _, err := target.NewGlobMatcher("[invalid"); err == nil {
		t.Fatal("expected error for invalid glob pattern")
	}
}

// TestParseModuleArgs_BareSysDocDispatchesEmptyID pins the bare-invocation
// contract for execution functions with an OPTIONAL primary: `zester '*'
// sys.doc` must dispatch with an empty ID and no primary argument (the peel
// then returns the unified index) instead of erroring or falling back to the
// generic "ad-hoc" ID. Regression: TestSysDoc_UnifiedIndex (integration).
func TestParseModuleArgs_BareSysDocDispatchesEmptyID(t *testing.T) {
	id, args, err := parseModuleArgs("sys.doc", nil)
	if err != nil {
		t.Fatalf("bare sys.doc: unexpected error: %v", err)
	}
	if id != "" {
		t.Fatalf("bare sys.doc: id = %q, want empty (peel returns the unified index)", id)
	}
	if len(args) != 0 {
		t.Fatalf("bare sys.doc: args = %v, want empty", args)
	}
}
