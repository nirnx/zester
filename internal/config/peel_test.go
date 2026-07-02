package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPeelDefaults(t *testing.T) {
	cfg := PeelDefaults()

	if cfg.NatsURL != "tls://nats:4222" {
		t.Errorf("NatsURL = %q, want %q", cfg.NatsURL, "tls://nats:4222")
	}
	if cfg.StatesCache != "/data/states-cache" {
		t.Errorf("StatesCache = %q, want %q", cfg.StatesCache, "/data/states-cache")
	}
	if cfg.ID != "" {
		t.Errorf("ID = %q, want empty", cfg.ID)
	}
	if cfg.MasterURL != "" {
		t.Errorf("MasterURL = %q, want empty", cfg.MasterURL)
	}
	if cfg.EnrollCA != "" {
		t.Errorf("EnrollCA = %q, want empty", cfg.EnrollCA)
	}
	if len(cfg.MasterURLs) != 0 {
		t.Errorf("MasterURLs = %v, want empty", cfg.MasterURLs)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want %q", cfg.LogFormat, "json")
	}
}

func TestLoadPeelMasterURLsAndLogging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peel.yaml")

	data := `id: web-01
master_urls:
  - https://master-1:8443
  - https://master-2:8443
log_level: debug
log_format: text
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPeel(path)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"https://master-1:8443", "https://master-2:8443"}
	if len(cfg.MasterURLs) != len(want) {
		t.Fatalf("MasterURLs = %v, want %v", cfg.MasterURLs, want)
	}
	for i := range want {
		if cfg.MasterURLs[i] != want[i] {
			t.Errorf("MasterURLs[%d] = %q, want %q", i, cfg.MasterURLs[i], want[i])
		}
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
	if cfg.LogFormat != "text" {
		t.Errorf("LogFormat = %q, want %q", cfg.LogFormat, "text")
	}
}

func TestLoadPeelFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peel.yaml")

	data := `id: web-01
nats_url: tls://custom:4222
master_url: https://master:8443
enroll_ca: /data/auth/ca.crt
states_cache: /srv/states-cache
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPeel(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ID != "web-01" {
		t.Errorf("ID = %q, want %q", cfg.ID, "web-01")
	}
	if cfg.NatsURL != "tls://custom:4222" {
		t.Errorf("NatsURL = %q, want %q", cfg.NatsURL, "tls://custom:4222")
	}
	if cfg.MasterURL != "https://master:8443" {
		t.Errorf("MasterURL = %q, want %q", cfg.MasterURL, "https://master:8443")
	}
	if cfg.EnrollCA != "/data/auth/ca.crt" {
		t.Errorf("EnrollCA = %q, want %q", cfg.EnrollCA, "/data/auth/ca.crt")
	}
	if cfg.StatesCache != "/srv/states-cache" {
		t.Errorf("StatesCache = %q, want %q", cfg.StatesCache, "/srv/states-cache")
	}
}

func TestLoadPeelMissing(t *testing.T) {
	cfg, err := LoadPeel("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NatsURL != "tls://nats:4222" {
		t.Errorf("NatsURL = %q, want default", cfg.NatsURL)
	}
	if cfg.StatesCache != "/data/states-cache" {
		t.Errorf("StatesCache = %q, want default", cfg.StatesCache)
	}
}

func TestLoadPeelExplicitMissing(t *testing.T) {
	_, err := LoadPeel("/nonexistent/peel.yaml")
	if err == nil {
		t.Fatal("expected error for explicit missing file")
	}
}

func TestLoadPeelNatsCAHealthAddr(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peel.yaml")

	data := `nats_ca: /custom/nats-ca.crt
health_addr: 0.0.0.0:9999
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPeel(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.NatsCA != "/custom/nats-ca.crt" {
		t.Errorf("NatsCA = %q, want %q", cfg.NatsCA, "/custom/nats-ca.crt")
	}
	if cfg.HealthAddr != "0.0.0.0:9999" {
		t.Errorf("HealthAddr = %q, want %q", cfg.HealthAddr, "0.0.0.0:9999")
	}
}

func TestLoadPeelHealthAddrDefault(t *testing.T) {
	if got := PeelDefaults().HealthAddr; got != "127.0.0.1:9090" {
		t.Errorf("PeelDefaults().HealthAddr = %q, want %q", got, "127.0.0.1:9090")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "peel.yaml")

	data := `id: web-01
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPeel(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.HealthAddr != "127.0.0.1:9090" {
		t.Errorf("HealthAddr = %q, want default %q", cfg.HealthAddr, "127.0.0.1:9090")
	}
	if cfg.NatsCA != "" {
		t.Errorf("NatsCA = %q, want empty", cfg.NatsCA)
	}
}

func TestLoadPeelSchedule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peel.yaml")

	data := `schedule:
  fact-refresh:
    module: facts.items
    args:
      key: os
    interval: 5m
    cron: "0 * * * *"
    splay: 30s
    maxrunning: 2
    run_on_start: true
    return_job: true
    enabled: false
  ping:
    module: test.ping
    interval: 1m
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPeel(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Schedule) != 2 {
		t.Fatalf("Schedule len = %d, want 2", len(cfg.Schedule))
	}

	fr, ok := cfg.Schedule["fact-refresh"]
	if !ok {
		t.Fatal("Schedule missing fact-refresh entry")
	}
	if fr.Module != "facts.items" {
		t.Errorf("Module = %q, want %q", fr.Module, "facts.items")
	}
	if fr.Args["key"] != "os" {
		t.Errorf("Args[key] = %v, want %q", fr.Args["key"], "os")
	}
	if fr.Interval != "5m" {
		t.Errorf("Interval = %q, want %q", fr.Interval, "5m")
	}
	if fr.Cron != "0 * * * *" {
		t.Errorf("Cron = %q, want %q", fr.Cron, "0 * * * *")
	}
	if fr.Splay != "30s" {
		t.Errorf("Splay = %q, want %q", fr.Splay, "30s")
	}
	if fr.MaxRunning != 2 {
		t.Errorf("MaxRunning = %d, want 2", fr.MaxRunning)
	}
	if !fr.RunOnStart {
		t.Error("RunOnStart = false, want true")
	}
	if !fr.ReturnJob {
		t.Error("ReturnJob = false, want true")
	}
	if fr.Enabled == nil {
		t.Fatal("Enabled = nil, want pointer to false")
	}
	if *fr.Enabled {
		t.Error("*Enabled = true, want false")
	}

	// Unset enabled stays nil (distinct from explicit false).
	ping, ok := cfg.Schedule["ping"]
	if !ok {
		t.Fatal("Schedule missing ping entry")
	}
	if ping.Module != "test.ping" {
		t.Errorf("Module = %q, want %q", ping.Module, "test.ping")
	}
	if ping.Interval != "1m" {
		t.Errorf("Interval = %q, want %q", ping.Interval, "1m")
	}
	if ping.Enabled != nil {
		t.Errorf("Enabled = %v, want nil for unset", *ping.Enabled)
	}
}

func TestLoadPeelNoSchedule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peel.yaml")

	data := `id: web-01
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPeel(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Schedule) != 0 {
		t.Errorf("Schedule = %v, want empty", cfg.Schedule)
	}
}

func TestLoadPeelPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peel.yaml")

	data := `id: db-01
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPeel(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ID != "db-01" {
		t.Errorf("ID = %q, want %q", cfg.ID, "db-01")
	}
	// Unset fields keep defaults.
	if cfg.NatsURL != "tls://nats:4222" {
		t.Errorf("NatsURL = %q, want default %q", cfg.NatsURL, "tls://nats:4222")
	}
	if cfg.StatesCache != "/data/states-cache" {
		t.Errorf("StatesCache = %q, want default %q", cfg.StatesCache, "/data/states-cache")
	}
}
