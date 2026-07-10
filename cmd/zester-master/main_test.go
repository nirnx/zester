package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMasterFlagParity pins the master's full CLI flag surface: every
// pre-existing flag must keep its exact name and default value, and every
// flag must carry a usage string. New flags are additive — add them here
// deliberately.
func TestMasterFlagParity(t *testing.T) {
	fs := flag.NewFlagSet("zester-master", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if _, _, err := setupMasterFlags(fs); err != nil {
		t.Fatalf("setupMasterFlags: %v", err)
	}

	// flag name -> expected default (flag.Flag.DefValue string form).
	want := map[string]string{
		"config":             "",
		"version":            "false",
		"nats-url":           "tls://nats:4222",
		"nats-ca":            "",
		"auth-dir":           "/var/lib/zester/auth",
		"enroll-addr":        ":8443",
		"enroll-tls-cert":    "/var/lib/zester/auth/enroll.crt",
		"enroll-tls-key":     "/var/lib/zester/auth/enroll.key",
		"states-dir":         "/var/lib/zester/states",
		"settings-dir":       "/var/lib/zester/settings",
		"jetstream-replicas": "0",
		"health-addr":        "127.0.0.1:9091",
		"api-docs":           "false",
		"gitfs-remotes":      "",
		"gitfs-interval":     (5 * time.Minute).String(),
		"gitfs-ssh-key":      "",
		// New in the flag-binder migration:
		"log-level":  "info",
		"log-format": "json",
		// Reactor (event-driven reactions):
		"reactor":                   "true",
		"reactor-dir":               "/var/lib/zester/reactor",
		"reactor-workers":           "4",
		"reactor-max-chain-depth":   "3",
		"reactor-enable-chaining":   "true",
		"reactor-default-throttle":  "0s",
		"reactor-source-rate-limit": "120",
		"reactor-max-event-age":     time.Hour.String(),
		"reactor-storm-rate":        "60",
		"reactor-breaker-cooldown":  (5 * time.Minute).String(),
		// Embedded CA + discovery:
		"ca-mode":                 "auto",
		"ca-dir":                  "",
		"ca-enroll-cert-validity": (90 * 24 * time.Hour).String(),
		"ca-enroll-sans":          "",
		"nats-advertise-urls":     "",

		// Live file publishing (hash-gated republish ticker + fsnotify watcher)
		// and the multi-master standby mirror.
		"files-republish-interval": (30 * time.Second).String(),
		"files-watch":              "true",
		"files-mirror":             "true",
		"publisher-status-file":    "/run/zester/publisher-status",
		// Promoted-version auto-rollout (0.5.0):
		"update-auto-rollout":    "true",
		"update-auto-components": "peel",
		"update-auto-batch-size": "5",
		"update-auto-soak-time":  (60 * time.Second).String(),
		"update-auto-max-failed": "1",
		"update-auto-interval":   time.Minute.String(),
	}

	got := map[string]*flag.Flag{}
	fs.VisitAll(func(f *flag.Flag) { got[f.Name] = f })

	for name, def := range want {
		f, ok := got[name]
		if !ok {
			t.Errorf("missing flag --%s", name)
			continue
		}
		if f.DefValue != def {
			t.Errorf("flag --%s default = %q, want %q", name, f.DefValue, def)
		}
		if f.Usage == "" {
			t.Errorf("flag --%s has empty usage string", name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected flag --%s registered (add it to the parity test if intentional)", name)
		}
	}
}

// TestMasterConfigPrecedence verifies flag > YAML > default, including the
// explicit-empty --gitfs-remotes "" override that disables GitFS even when
// the config file lists remotes.
func TestMasterConfigPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.yaml")
	yaml := `nats_url: tls://yaml:4222
health_addr: 0.0.0.0:9999
log_level: warn
gitfs:
  remotes:
    - git@github.com:org/repo.git
  interval: 2m
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := flag.NewFlagSet("zester-master", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFile, _, err := setupMasterFlags(fs)
	if err != nil {
		t.Fatalf("setupMasterFlags: %v", err)
	}
	args := []string{
		"--config", path,
		"--nats-url", "tls://flag:4222",
		"--gitfs-remotes", "",
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	cfg, err := loadMasterConfig(fs, *configFile)
	if err != nil {
		t.Fatalf("loadMasterConfig: %v", err)
	}

	if cfg.NatsURL != "tls://flag:4222" {
		t.Errorf("NatsURL = %q, want flag value tls://flag:4222", cfg.NatsURL)
	}
	if cfg.HealthAddr != "0.0.0.0:9999" {
		t.Errorf("HealthAddr = %q, want YAML value 0.0.0.0:9999", cfg.HealthAddr)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want YAML value warn", cfg.LogLevel)
	}
	if len(cfg.GitFS.Remotes) != 0 {
		t.Errorf("GitFS.Remotes = %v, want empty (explicit --gitfs-remotes \"\")", cfg.GitFS.Remotes)
	}
	if cfg.GitFS.Interval != 2*time.Minute {
		t.Errorf("GitFS.Interval = %v, want YAML value 2m", cfg.GitFS.Interval)
	}
	if cfg.AuthDir != "/var/lib/zester/auth" {
		t.Errorf("AuthDir = %q, want default /var/lib/zester/auth", cfg.AuthDir)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want default json", cfg.LogFormat)
	}
}
