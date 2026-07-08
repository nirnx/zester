package main

import (
	"flag"
	"io"
	"testing"
	"time"
)

// TestRegisterFlags_Parity asserts the full watchdog flag set: every
// pre-existing flag keeps its exact name and default value, new flags are
// additive, and no unexpected flag is registered.
func TestRegisterFlags_Parity(t *testing.T) {
	fs := flag.NewFlagSet("zester-watchdog", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerFlags(fs)

	// name -> default value (flag.Flag.DefValue string form).
	want := map[string]string{
		// Pre-existing flags — names and defaults must never change.
		"child-bin":       "",
		"child-args":      "",
		"nats-url":        "tls://localhost:4222",
		"nats-ca":         "",
		"nats-creds":      "",
		"health-url":      "http://127.0.0.1:9090/healthz",
		"health-timeout":  "5s",
		"health-interval": "10s",
		"health-retries":  "3",
		"soak-time":       "1m0s",
		"id":              "",
		"component":       "",
		// New flags (additive). ready-url defaults empty = derived from
		// --health-url (path replaced with /readyz) so it follows the
		// child's port.
		"ready-url":       "",
		"log-level":       "info",
		"log-format":      "json",
		"bootstrap-cache": "",
	}

	got := map[string]string{}
	fs.VisitAll(func(fl *flag.Flag) {
		got[fl.Name] = fl.DefValue
		if fl.Usage == "" {
			t.Errorf("flag %q has empty usage string", fl.Name)
		}
	})

	for name, def := range want {
		gotDef, ok := got[name]
		if !ok {
			t.Errorf("missing flag %q", name)
			continue
		}
		if gotDef != def {
			t.Errorf("flag %q default = %q, want %q", name, gotDef, def)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected flag %q registered", name)
		}
	}
}

// TestRegisterFlags_ParseIntoStruct verifies parsed values land in the
// watchdogFlags struct fields.
func TestRegisterFlags_ParseIntoStruct(t *testing.T) {
	fs := flag.NewFlagSet("zester-watchdog", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := registerFlags(fs)

	err := fs.Parse([]string{
		"--child-bin", "/usr/local/bin/zester-master",
		"--health-url", "http://127.0.0.1:9091/healthz",
		"--ready-url", "http://127.0.0.1:9091/readyz",
		"--health-timeout", "7s",
		"--log-level", "debug",
		"--log-format", "text",
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if f.childBin != "/usr/local/bin/zester-master" {
		t.Errorf("childBin = %q", f.childBin)
	}
	if f.healthURL != "http://127.0.0.1:9091/healthz" {
		t.Errorf("healthURL = %q", f.healthURL)
	}
	if f.readyURL != "http://127.0.0.1:9091/readyz" {
		t.Errorf("readyURL = %q", f.readyURL)
	}
	if f.healthTimeout != 7*time.Second {
		t.Errorf("healthTimeout = %v", f.healthTimeout)
	}
	if f.logLevel != "debug" {
		t.Errorf("logLevel = %q", f.logLevel)
	}
	if f.logFormat != "text" {
		t.Errorf("logFormat = %q", f.logFormat)
	}
	// Untouched flags keep their defaults.
	if f.natsURL != "tls://localhost:4222" {
		t.Errorf("natsURL = %q, want default", f.natsURL)
	}
	if f.soakTime != 60*time.Second {
		t.Errorf("soakTime = %v, want default 60s", f.soakTime)
	}
}

// TestShellSplit covers the quoting behavior --child-args relies on.
func TestDeriveReadyURL(t *testing.T) {
	cases := []struct {
		health  string
		want    string
		wantErr bool
	}{
		{health: "http://127.0.0.1:9090/healthz", want: "http://127.0.0.1:9090/readyz"},
		{health: "http://127.0.0.1:9091/healthz", want: "http://127.0.0.1:9091/readyz"},
		{health: "https://node.example:8443/some/other/path", want: "https://node.example:8443/readyz"},
		{health: "not a url", wantErr: true},
		{health: "/healthz", wantErr: true}, // no scheme/host
	}
	for _, c := range cases {
		got, err := deriveReadyURL(c.health)
		if c.wantErr {
			if err == nil {
				t.Errorf("deriveReadyURL(%q) = %q, want error", c.health, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("deriveReadyURL(%q): %v", c.health, err)
			continue
		}
		if got != c.want {
			t.Errorf("deriveReadyURL(%q) = %q, want %q", c.health, got, c.want)
		}
	}
}

func TestShellSplit(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"--config /etc/zester/peel.yaml", []string{"--config", "/etc/zester/peel.yaml"}},
		{`--name 'hello world'`, []string{"--name", "hello world"}},
		{`--name "hello world"`, []string{"--name", "hello world"}},
	}
	for _, tc := range cases {
		got := shellSplit(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("shellSplit(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("shellSplit(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}
