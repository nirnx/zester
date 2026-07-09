package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestDeriveComponentDefaults_Peel(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "peel.yaml")
	// An FQDN id (with dots) must be sanitized into a valid subject token.
	body := "id: web01.example.com\nauth_dir: " + dir + "/auth\ndata_dir: " + dir + "/data\nhealth_addr: 127.0.0.1:9099\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	f := &watchdogFlags{component: "peel", childArgs: "--config " + cfg}
	// Simulate the operator only passing --component (+ our test's child-args).
	explicit := map[string]bool{"child-args": true}
	if err := deriveComponentDefaults(f, explicit, discardLogger()); err != nil {
		t.Fatalf("derive: %v", err)
	}

	if f.childBin != peelBinPath {
		t.Errorf("child-bin = %q, want %q", f.childBin, peelBinPath)
	}
	if f.id != "web01_example_com" {
		t.Errorf("id = %q, want sanitized web01_example_com", f.id)
	}
	if want := filepath.Join(dir, "auth", "web01_example_com.creds"); f.natsCreds != want {
		t.Errorf("nats-creds = %q, want %q", f.natsCreds, want)
	}
	if want := filepath.Join(dir, "auth", "nats-ca.crt"); f.natsCA != want {
		t.Errorf("nats-ca = %q, want %q", f.natsCA, want)
	}
	if want := filepath.Join(dir, "data", "nats-bootstrap.msgpack"); f.bootstrapCache != want {
		t.Errorf("bootstrap-cache = %q, want %q", f.bootstrapCache, want)
	}
	if f.healthURL != "http://127.0.0.1:9099/healthz" {
		t.Errorf("health-url = %q, want http://127.0.0.1:9099/healthz", f.healthURL)
	}
}

func TestDeriveComponentDefaults_ExplicitWins(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "peel.yaml")
	if err := os.WriteFile(cfg, []byte("id: fromconfig\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The compose wd-01 shape: everything explicit — nothing must be derived over.
	f := &watchdogFlags{
		component: "peel",
		childBin:  "/custom/zester-peel",
		childArgs: "--config " + cfg,
		id:        "wd-01",
		natsCreds: "/custom/wd-01.creds",
		natsCA:    "/custom/ca.crt",
		healthURL: "http://127.0.0.1:1234/healthz",
	}
	explicit := map[string]bool{
		"child-bin": true, "child-args": true, "id": true,
		"nats-creds": true, "nats-ca": true, "health-url": true,
	}
	if err := deriveComponentDefaults(f, explicit, discardLogger()); err != nil {
		t.Fatalf("derive: %v", err)
	}
	if f.childBin != "/custom/zester-peel" || f.id != "wd-01" ||
		f.natsCreds != "/custom/wd-01.creds" || f.natsCA != "/custom/ca.crt" ||
		f.healthURL != "http://127.0.0.1:1234/healthz" {
		t.Errorf("explicit flags were overridden: %+v", f)
	}
}

func TestDeriveComponentDefaults_ExplicitIDSanitized(t *testing.T) {
	// An explicit --id passes through the SAME sanitization as the peel's
	// config id — a dotted FQDN must derive the same identity (and the same
	// <id>.creds filename) the peel resolves, not a raw dotted path that can
	// never exist.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "peel.yaml")
	if err := os.WriteFile(cfg, []byte("auth_dir: "+dir+"/auth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &watchdogFlags{component: "peel", childArgs: "--config " + cfg, id: "web01.example.com"}
	explicit := map[string]bool{"child-args": true, "id": true}
	if err := deriveComponentDefaults(f, explicit, discardLogger()); err != nil {
		t.Fatalf("derive: %v", err)
	}
	if f.id != "web01_example_com" {
		t.Errorf("id = %q, want sanitized web01_example_com", f.id)
	}
	if want := filepath.Join(dir, "auth", "web01_example_com.creds"); f.natsCreds != want {
		t.Errorf("nats-creds = %q, want %q", f.natsCreds, want)
	}
}

func TestDeriveComponentDefaults_ChildArgsOverlay(t *testing.T) {
	// Flags in --child-args are applied by the CHILD over its config
	// (flag > YAML), so the derivation must honor them too — otherwise the
	// watchdog derives creds/health-url from values the child does not use.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "peel.yaml")
	body := "id: fromconfig\nauth_dir: " + dir + "/cfg-auth\nhealth_addr: 127.0.0.1:9099\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &watchdogFlags{
		component: "peel",
		childArgs: "--config " + cfg + " --id overridden --auth-dir " + dir + "/flag-auth --health-addr 127.0.0.1:9555",
	}
	explicit := map[string]bool{"child-args": true}
	if err := deriveComponentDefaults(f, explicit, discardLogger()); err != nil {
		t.Fatalf("derive: %v", err)
	}
	if f.id != "overridden" {
		t.Errorf("id = %q, want the child-args override 'overridden'", f.id)
	}
	if want := filepath.Join(dir, "flag-auth", "overridden.creds"); f.natsCreds != want {
		t.Errorf("nats-creds = %q, want %q", f.natsCreds, want)
	}
	if f.healthURL != "http://127.0.0.1:9555/healthz" {
		t.Errorf("health-url = %q, want the child-args override port", f.healthURL)
	}
}

func TestDeriveComponentDefaults_Master(t *testing.T) {
	// Hermetic: point --child-args at a tempdir config so the test never
	// reads a real /etc/zester/master.yaml (and the node-id pin lands in the
	// tempdir, not /var/lib/zester/auth).
	dir := t.TempDir()
	cfg := filepath.Join(dir, "master.yaml")
	if err := os.WriteFile(cfg, []byte("auth_dir: "+dir+"/auth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &watchdogFlags{component: "master", childArgs: "--config " + cfg}
	explicit := map[string]bool{"child-args": true}
	if err := deriveComponentDefaults(f, explicit, discardLogger()); err != nil {
		t.Fatalf("derive: %v", err)
	}
	if f.childBin != masterBinPath {
		t.Errorf("child-bin = %q, want %q", f.childBin, masterBinPath)
	}
	if want := filepath.Join(dir, "auth", "master.creds"); f.natsCreds != want {
		t.Errorf("nats-creds = %q, want %q", f.natsCreds, want)
	}
	if f.bootstrapCache != "" {
		t.Errorf("master should not derive a bootstrap-cache, got %q", f.bootstrapCache)
	}
	// Master health defaults to :9091 (health_addr not set in the config).
	if f.healthURL != "http://127.0.0.1:9091/healthz" {
		t.Errorf("health-url = %q, want the master :9091 default", f.healthURL)
	}
	// id derived from hostname -> non-empty and valid.
	if f.id == "" {
		t.Error("master id was not derived")
	}
}

func TestDeriveChildArgs(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "peel.yaml")
	if err := os.WriteFile(present, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := deriveChildArgs(present); got != "--config "+present {
		t.Errorf("existing config: got %q", got)
	}
	// A missing packaged config must NOT be passed explicitly — the child
	// fatals on an explicitly passed missing --config but runs on built-in
	// defaults with no flag.
	if got := deriveChildArgs(filepath.Join(dir, "absent.yaml")); got != "" {
		t.Errorf("missing config: got %q, want empty child-args", got)
	}
}

func TestConfigPathFromArgs(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--config", "/a.yaml"}, "/a.yaml"},
		{[]string{"-config", "/b.yaml"}, "/b.yaml"},
		{[]string{"--config=/c.yaml"}, "/c.yaml"},
		{[]string{"--id", "x", "--config", "/d.yaml"}, "/d.yaml"},
		{[]string{"--id", "x"}, "DEF"},
		{nil, "DEF"},
	}
	for _, c := range cases {
		if got := configPathFromArgs(c.args, "DEF"); got != c.want {
			t.Errorf("configPathFromArgs(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestArgValue(t *testing.T) {
	args := []string{"--config", "/a.yaml", "-id", "x", "--auth-dir=/auth", "-data-dir=/data"}
	cases := []struct {
		name  string
		want  string
		found bool
	}{
		{"config", "/a.yaml", true},
		{"id", "x", true},
		{"auth-dir", "/auth", true},
		{"data-dir", "/data", true},
		{"health-addr", "", false},
	}
	for _, c := range cases {
		got, ok := argValue(args, c.name)
		if got != c.want || ok != c.found {
			t.Errorf("argValue(%q) = (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.found)
		}
	}
	// A trailing flag with no value is not matched.
	if _, ok := argValue([]string{"--id"}, "id"); ok {
		t.Error("trailing valueless flag should not match")
	}
}
