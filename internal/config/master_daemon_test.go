package config

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMasterDaemonDefaults(t *testing.T) {
	cfg := MasterDaemonDefaults()

	if cfg.NatsURL != "tls://nats:4222" {
		t.Errorf("NatsURL = %q, want %q", cfg.NatsURL, "tls://nats:4222")
	}
	if cfg.StatesDir != "/data/states" {
		t.Errorf("StatesDir = %q, want %q", cfg.StatesDir, "/data/states")
	}
	if cfg.SettingsDir != "/data/settings" {
		t.Errorf("SettingsDir = %q, want %q", cfg.SettingsDir, "/data/settings")
	}
	if cfg.Enroll.Addr != ":8443" {
		t.Errorf("Enroll.Addr = %q, want %q", cfg.Enroll.Addr, ":8443")
	}
	if cfg.Enroll.TLSCert != "/data/auth/enroll.crt" {
		t.Errorf("Enroll.TLSCert = %q, want %q", cfg.Enroll.TLSCert, "/data/auth/enroll.crt")
	}
	if cfg.Enroll.TLSKey != "/data/auth/enroll.key" {
		t.Errorf("Enroll.TLSKey = %q, want %q", cfg.Enroll.TLSKey, "/data/auth/enroll.key")
	}
	if cfg.GitFS.Interval != 5*time.Minute {
		t.Errorf("GitFS.Interval = %v, want %v", cfg.GitFS.Interval, 5*time.Minute)
	}
	if len(cfg.GitFS.Remotes) != 0 {
		t.Errorf("GitFS.Remotes = %v, want empty", cfg.GitFS.Remotes)
	}
	if cfg.GitFS.SSHKey != "" {
		t.Errorf("GitFS.SSHKey = %q, want empty", cfg.GitFS.SSHKey)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want %q", cfg.LogFormat, "json")
	}
}

func TestLoadMasterDaemonLogging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.yaml")

	data := `log_level: debug
log_format: text
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadMasterDaemon(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
	if cfg.LogFormat != "text" {
		t.Errorf("LogFormat = %q, want %q", cfg.LogFormat, "text")
	}
}

// TestMasterDaemonConfigBindsFlags verifies the struct's flag tags are
// structurally valid for BindFlags (no duplicates, supported types only) and
// that ApplyVisited reproduces flag > YAML precedence including the
// explicit-empty --gitfs-remotes "" override. The full flag-parity assertion
// (names + defaults) lives in cmd/zester-master.
func TestMasterDaemonConfigBindsFlags(t *testing.T) {
	defaults := MasterDaemonDefaults()
	fs := flag.NewFlagSet("master", flag.ContinueOnError)
	if err := BindFlags(fs, &defaults); err != nil {
		t.Fatalf("BindFlags: %v", err)
	}

	args := []string{
		"--nats-url", "tls://flag:4222",
		"--gitfs-remotes", "",
		"--log-level", "debug",
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Simulate the YAML load step overwriting the struct.
	cfg := MasterDaemonDefaults()
	cfg.NatsURL = "tls://yaml:4222"
	cfg.GitFS.Remotes = []string{"git@github.com:org/repo.git"}
	cfg.Enroll.Addr = ":9443"

	if err := ApplyVisited(fs, &cfg); err != nil {
		t.Fatalf("ApplyVisited: %v", err)
	}

	if cfg.NatsURL != "tls://flag:4222" {
		t.Errorf("NatsURL = %q, want flag value", cfg.NatsURL)
	}
	if len(cfg.GitFS.Remotes) != 0 {
		t.Errorf("GitFS.Remotes = %v, want empty (explicit --gitfs-remotes \"\")", cfg.GitFS.Remotes)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
	// Flags not passed keep the YAML value.
	if cfg.Enroll.Addr != ":9443" {
		t.Errorf("Enroll.Addr = %q, want YAML value :9443", cfg.Enroll.Addr)
	}
	// And YAML-untouched fields keep defaults.
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want default json", cfg.LogFormat)
	}
}

func TestLoadMasterDaemonFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.yaml")

	data := `nats_url: tls://custom:4222
nats_ca: /custom/nats-ca.crt
states_dir: /srv/states
settings_dir: /srv/settings
enroll:
  addr: ":9443"
  tls_cert: /custom/cert.pem
  tls_key: /custom/key.pem
gitfs:
  remotes:
    - git@github.com:org/base.git
    - git@github.com:org/app.git
  interval: 2m
  ssh_key: /custom/deploy.key
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadMasterDaemon(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.NatsURL != "tls://custom:4222" {
		t.Errorf("NatsURL = %q, want %q", cfg.NatsURL, "tls://custom:4222")
	}
	if cfg.NatsCA != "/custom/nats-ca.crt" {
		t.Errorf("NatsCA = %q, want %q", cfg.NatsCA, "/custom/nats-ca.crt")
	}
	if cfg.StatesDir != "/srv/states" {
		t.Errorf("StatesDir = %q, want %q", cfg.StatesDir, "/srv/states")
	}
	if cfg.SettingsDir != "/srv/settings" {
		t.Errorf("SettingsDir = %q, want %q", cfg.SettingsDir, "/srv/settings")
	}
	if cfg.Enroll.Addr != ":9443" {
		t.Errorf("Enroll.Addr = %q, want %q", cfg.Enroll.Addr, ":9443")
	}
	if cfg.Enroll.TLSCert != "/custom/cert.pem" {
		t.Errorf("Enroll.TLSCert = %q, want %q", cfg.Enroll.TLSCert, "/custom/cert.pem")
	}
	if cfg.Enroll.TLSKey != "/custom/key.pem" {
		t.Errorf("Enroll.TLSKey = %q, want %q", cfg.Enroll.TLSKey, "/custom/key.pem")
	}
	if len(cfg.GitFS.Remotes) != 2 {
		t.Fatalf("GitFS.Remotes len = %d, want 2", len(cfg.GitFS.Remotes))
	}
	if cfg.GitFS.Remotes[0] != "git@github.com:org/base.git" {
		t.Errorf("GitFS.Remotes[0] = %q", cfg.GitFS.Remotes[0])
	}
	if cfg.GitFS.Interval != 2*time.Minute {
		t.Errorf("GitFS.Interval = %v, want 2m", cfg.GitFS.Interval)
	}
	if cfg.GitFS.SSHKey != "/custom/deploy.key" {
		t.Errorf("GitFS.SSHKey = %q, want %q", cfg.GitFS.SSHKey, "/custom/deploy.key")
	}
}

func TestLoadMasterDaemonMissing(t *testing.T) {
	// No file at default path → returns defaults, no error.
	cfg, err := LoadMasterDaemon("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NatsURL != "tls://nats:4222" {
		t.Errorf("NatsURL = %q, want default", cfg.NatsURL)
	}
	if cfg.StatesDir != "/data/states" {
		t.Errorf("StatesDir = %q, want default", cfg.StatesDir)
	}
}

func TestLoadMasterDaemonExplicitMissing(t *testing.T) {
	// Explicit path that doesn't exist → error.
	_, err := LoadMasterDaemon("/nonexistent/master.yaml")
	if err == nil {
		t.Fatal("expected error for explicit missing file")
	}
}

func TestLoadMasterDaemonPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.yaml")

	data := `nats_url: tls://partial:4222
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadMasterDaemon(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.NatsURL != "tls://partial:4222" {
		t.Errorf("NatsURL = %q, want %q", cfg.NatsURL, "tls://partial:4222")
	}
	// Unset fields keep defaults.
	if cfg.StatesDir != "/data/states" {
		t.Errorf("StatesDir = %q, want default %q", cfg.StatesDir, "/data/states")
	}
	if cfg.Enroll.Addr != ":8443" {
		t.Errorf("Enroll.Addr = %q, want default %q", cfg.Enroll.Addr, ":8443")
	}
	if cfg.GitFS.Interval != 5*time.Minute {
		t.Errorf("GitFS.Interval = %v, want default 5m", cfg.GitFS.Interval)
	}
}

func TestMasterDaemonDefaultsHealthAddrAndAPI(t *testing.T) {
	cfg := MasterDaemonDefaults()

	if cfg.HealthAddr != "127.0.0.1:9091" {
		t.Errorf("HealthAddr = %q, want %q", cfg.HealthAddr, "127.0.0.1:9091")
	}
	// Docs are served unauthenticated on the enrollment listener, so they
	// must be an explicit opt-in.
	if cfg.API.DocsEnabled {
		t.Error("API.DocsEnabled = true, want false by default")
	}
	if len(cfg.API.Tokens) != 0 {
		t.Errorf("API.Tokens = %v, want empty", cfg.API.Tokens)
	}
}

func TestLoadMasterDaemonHealthAddrAndAPI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.yaml")

	data := `health_addr: 0.0.0.0:9200
api:
  docs_enabled: true
  tokens:
    - username: ci
      token_file: /data/auth/api-ci.token
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadMasterDaemon(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.HealthAddr != "0.0.0.0:9200" {
		t.Errorf("HealthAddr = %q, want %q", cfg.HealthAddr, "0.0.0.0:9200")
	}
	if !cfg.API.DocsEnabled {
		t.Error("API.DocsEnabled = false, want true")
	}
	if len(cfg.API.Tokens) != 1 {
		t.Fatalf("API.Tokens len = %d, want 1", len(cfg.API.Tokens))
	}
	if cfg.API.Tokens[0].Username != "ci" {
		t.Errorf("API.Tokens[0].Username = %q, want %q", cfg.API.Tokens[0].Username, "ci")
	}
	if cfg.API.Tokens[0].TokenFile != "/data/auth/api-ci.token" {
		t.Errorf("API.Tokens[0].TokenFile = %q, want %q", cfg.API.Tokens[0].TokenFile, "/data/auth/api-ci.token")
	}
}

func TestLoadMasterDaemonHealthAddrAndAPIDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.yaml")

	data := `nats_url: tls://partial:4222
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadMasterDaemon(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.HealthAddr != "127.0.0.1:9091" {
		t.Errorf("HealthAddr = %q, want default %q", cfg.HealthAddr, "127.0.0.1:9091")
	}
	if cfg.API.DocsEnabled {
		t.Error("API.DocsEnabled = true, want default false")
	}
}

func TestLoadMasterDaemonGitFS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.yaml")

	data := `gitfs:
  remotes:
    - git@github.com:a/b.git
    - git@github.com:c/d.git
    - https://github.com/e/f.git
  interval: 10m
  ssh_key: /keys/deploy
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadMasterDaemon(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.GitFS.Remotes) != 3 {
		t.Fatalf("GitFS.Remotes len = %d, want 3", len(cfg.GitFS.Remotes))
	}
	if cfg.GitFS.Interval != 10*time.Minute {
		t.Errorf("GitFS.Interval = %v, want 10m", cfg.GitFS.Interval)
	}
	if cfg.GitFS.SSHKey != "/keys/deploy" {
		t.Errorf("GitFS.SSHKey = %q, want %q", cfg.GitFS.SSHKey, "/keys/deploy")
	}
}
