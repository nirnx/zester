package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/internal/config"
)

// TestPeelFlagParity pins the full zester-peel flag set: every pre-existing
// flag must keep its exact name and default value; new flags are additive.
// If this test fails, a flag was renamed, dropped, or its default changed —
// all of which break existing deployments.
func TestPeelFlagParity(t *testing.T) {
	defaults := config.PeelDefaults()
	fs, _, err := peelFlags(&defaults)
	if err != nil {
		t.Fatalf("peelFlags: %v", err)
	}

	want := map[string]string{
		// Pre-existing flags (parity: name + default must not change).
		"config":       "",
		"id":           "",
		"nats-url":     "tls://nats:4222",
		"nats-ca":      "",
		"master-url":   "",
		"enroll-ca":    "",
		"states-cache": "/data/states-cache",
		"health-addr":  "127.0.0.1:9090",
		// New flags (additive).
		"master-urls": "",
		"log-level":   "info",
		"log-format":  "json",
	}

	got := make(map[string]string)
	fs.VisitAll(func(f *flag.Flag) {
		got[f.Name] = f.DefValue
		if f.Usage == "" {
			t.Errorf("flag -%s has empty usage string", f.Name)
		}
	})

	for name, def := range want {
		gotDef, ok := got[name]
		if !ok {
			t.Errorf("missing flag -%s", name)
			continue
		}
		if gotDef != def {
			t.Errorf("flag -%s default = %q, want %q", name, gotDef, def)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected flag -%s (add it to the parity test if intentional)", name)
		}
	}
}

// TestPeelFlagPrecedence exercises the main() config pipeline end to end:
// flag > YAML > default, including the explicitly-set-empty-flag case.
func TestPeelFlagPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peel.yaml")
	data := `id: yaml-id
nats_url: tls://yaml:4222
master_urls:
  - https://yaml-master:8443
log_level: warn
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	defaults := config.PeelDefaults()
	fs, configFile, err := peelFlags(&defaults)
	if err != nil {
		t.Fatalf("peelFlags: %v", err)
	}
	args := []string{
		"--config", path,
		"--id", "flag-id",
		"--master-urls", "https://m1:8443,https://m2:8443",
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse: %v", err)
	}

	cfg, err := config.LoadPeel(*configFile)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := config.ApplyVisited(fs, cfg); err != nil {
		t.Fatalf("apply visited: %v", err)
	}

	if cfg.ID != "flag-id" {
		t.Errorf("ID = %q, want flag value %q", cfg.ID, "flag-id")
	}
	if cfg.NatsURL != "tls://yaml:4222" {
		t.Errorf("NatsURL = %q, want YAML value %q", cfg.NatsURL, "tls://yaml:4222")
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want YAML value %q", cfg.LogLevel, "warn")
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want default %q", cfg.LogFormat, "json")
	}
	wantURLs := []string{"https://m1:8443", "https://m2:8443"}
	if len(cfg.MasterURLs) != len(wantURLs) {
		t.Fatalf("MasterURLs = %v, want %v", cfg.MasterURLs, wantURLs)
	}
	for i := range wantURLs {
		if cfg.MasterURLs[i] != wantURLs[i] {
			t.Errorf("MasterURLs[%d] = %q, want %q", i, cfg.MasterURLs[i], wantURLs[i])
		}
	}
}

// TestPeelFlagExplicitEmptyOverridesYAML pins the flag.Visit semantics: an
// explicitly empty flag value clears a YAML-provided value.
func TestPeelFlagExplicitEmptyOverridesYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peel.yaml")
	data := `master_urls:
  - https://yaml-master:8443
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	defaults := config.PeelDefaults()
	fs, configFile, err := peelFlags(&defaults)
	if err != nil {
		t.Fatalf("peelFlags: %v", err)
	}
	if err := fs.Parse([]string{"--config", path, "--master-urls", ""}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	cfg, err := config.LoadPeel(*configFile)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := config.ApplyVisited(fs, cfg); err != nil {
		t.Fatalf("apply visited: %v", err)
	}

	if len(cfg.MasterURLs) != 0 {
		t.Errorf("MasterURLs = %v, want empty (explicit --master-urls \"\")", cfg.MasterURLs)
	}
}
