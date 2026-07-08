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
	if cfg.StatesDir != "/var/lib/zester/states" {
		t.Errorf("StatesDir = %q, want %q", cfg.StatesDir, "/var/lib/zester/states")
	}
	if cfg.SettingsDir != "/var/lib/zester/settings" {
		t.Errorf("SettingsDir = %q, want %q", cfg.SettingsDir, "/var/lib/zester/settings")
	}
	if cfg.Enroll.Addr != ":8443" {
		t.Errorf("Enroll.Addr = %q, want %q", cfg.Enroll.Addr, ":8443")
	}
	if cfg.Enroll.TLSCert != "/var/lib/zester/auth/enroll.crt" {
		t.Errorf("Enroll.TLSCert = %q, want %q", cfg.Enroll.TLSCert, "/var/lib/zester/auth/enroll.crt")
	}
	if cfg.Enroll.TLSKey != "/var/lib/zester/auth/enroll.key" {
		t.Errorf("Enroll.TLSKey = %q, want %q", cfg.Enroll.TLSKey, "/var/lib/zester/auth/enroll.key")
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

func TestMasterDaemonDefaultsReactor(t *testing.T) {
	cfg := MasterDaemonDefaults()

	if !cfg.Reactor.Enabled {
		t.Error("Reactor.Enabled = false, want true by default")
	}
	if cfg.Reactor.Dir != "/var/lib/zester/reactor" {
		t.Errorf("Reactor.Dir = %q, want %q", cfg.Reactor.Dir, "/data/reactor")
	}
	if cfg.Reactor.Workers != 4 {
		t.Errorf("Reactor.Workers = %d, want 4", cfg.Reactor.Workers)
	}
	if cfg.Reactor.MaxChainDepth != 3 {
		t.Errorf("Reactor.MaxChainDepth = %d, want 3", cfg.Reactor.MaxChainDepth)
	}
	if !cfg.Reactor.EnableChaining {
		t.Error("Reactor.EnableChaining = false, want true by default")
	}
	if cfg.Reactor.DefaultThrottle != 0 {
		t.Errorf("Reactor.DefaultThrottle = %v, want 0", cfg.Reactor.DefaultThrottle)
	}
	if cfg.Reactor.SourceRateLimit != 120 {
		t.Errorf("Reactor.SourceRateLimit = %d, want 120", cfg.Reactor.SourceRateLimit)
	}
	if cfg.Reactor.MaxEventAge != Duration(time.Hour) {
		t.Errorf("Reactor.MaxEventAge = %v, want 1h", cfg.Reactor.MaxEventAge)
	}
	if cfg.Reactor.StormRate != 60 {
		t.Errorf("Reactor.StormRate = %d, want 60", cfg.Reactor.StormRate)
	}
	if cfg.Reactor.BreakerCooldown != Duration(5*time.Minute) {
		t.Errorf("Reactor.BreakerCooldown = %v, want 5m", cfg.Reactor.BreakerCooldown)
	}
}

func TestLoadMasterDaemonReactor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.yaml")

	data := `reactor:
  enabled: false
  dir: /srv/reactor
  workers: 8
  max_chain_depth: 5
  enable_chaining: false
  default_throttle: 30s
  source_rate_limit: 10
  max_event_age: 2h
  storm_rate: 5
  breaker_cooldown: 1m
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadMasterDaemon(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Reactor.Enabled {
		t.Error("Reactor.Enabled = true, want false from YAML")
	}
	if cfg.Reactor.Dir != "/srv/reactor" {
		t.Errorf("Reactor.Dir = %q, want /srv/reactor", cfg.Reactor.Dir)
	}
	if cfg.Reactor.Workers != 8 {
		t.Errorf("Reactor.Workers = %d, want 8", cfg.Reactor.Workers)
	}
	if cfg.Reactor.MaxChainDepth != 5 {
		t.Errorf("Reactor.MaxChainDepth = %d, want 5", cfg.Reactor.MaxChainDepth)
	}
	if cfg.Reactor.EnableChaining {
		t.Error("Reactor.EnableChaining = true, want false from YAML")
	}
	if cfg.Reactor.DefaultThrottle != Duration(30*time.Second) {
		t.Errorf("Reactor.DefaultThrottle = %v, want 30s", cfg.Reactor.DefaultThrottle)
	}
	if cfg.Reactor.SourceRateLimit != 10 {
		t.Errorf("Reactor.SourceRateLimit = %d, want 10", cfg.Reactor.SourceRateLimit)
	}
	if cfg.Reactor.MaxEventAge != Duration(2*time.Hour) {
		t.Errorf("Reactor.MaxEventAge = %v, want 2h", cfg.Reactor.MaxEventAge)
	}
	if cfg.Reactor.StormRate != 5 {
		t.Errorf("Reactor.StormRate = %d, want 5", cfg.Reactor.StormRate)
	}
	if cfg.Reactor.BreakerCooldown != Duration(time.Minute) {
		t.Errorf("Reactor.BreakerCooldown = %v, want 1m", cfg.Reactor.BreakerCooldown)
	}
}

// TestLoadMasterDaemonReactorZeroDurations pins the documented "0 disables"
// YAML forms: both the bare scalar 0 and the unit-suffixed 0s must load
// (plain time.Duration fields reject bare integers — the exact failure that
// used to brick the master when operators followed the docs), while non-zero
// bare numbers stay a loud parse error rather than a silent unit guess.
func TestLoadMasterDaemonReactorZeroDurations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.yaml")

	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		check   func(t *testing.T, cfg *MasterDaemonConfig)
	}{
		{
			name: "bare zero disables",
			yaml: "reactor:\n  max_event_age: 0\n  default_throttle: 0\n  breaker_cooldown: 0\n",
			check: func(t *testing.T, cfg *MasterDaemonConfig) {
				if cfg.Reactor.MaxEventAge != 0 {
					t.Errorf("MaxEventAge = %v, want 0", cfg.Reactor.MaxEventAge)
				}
				if cfg.Reactor.DefaultThrottle != 0 {
					t.Errorf("DefaultThrottle = %v, want 0", cfg.Reactor.DefaultThrottle)
				}
				if cfg.Reactor.BreakerCooldown != 0 {
					t.Errorf("BreakerCooldown = %v, want 0", cfg.Reactor.BreakerCooldown)
				}
			},
		},
		{
			name: "zero with unit disables",
			yaml: "reactor:\n  max_event_age: 0s\n  default_throttle: 0s\n",
			check: func(t *testing.T, cfg *MasterDaemonConfig) {
				if cfg.Reactor.MaxEventAge != 0 {
					t.Errorf("MaxEventAge = %v, want 0", cfg.Reactor.MaxEventAge)
				}
				if cfg.Reactor.DefaultThrottle != 0 {
					t.Errorf("DefaultThrottle = %v, want 0", cfg.Reactor.DefaultThrottle)
				}
			},
		},
		{
			name: "quoted zero disables",
			yaml: "reactor:\n  max_event_age: \"0\"\n",
			check: func(t *testing.T, cfg *MasterDaemonConfig) {
				if cfg.Reactor.MaxEventAge != 0 {
					t.Errorf("MaxEventAge = %v, want 0", cfg.Reactor.MaxEventAge)
				}
			},
		},
		{
			name:    "bare non-zero number is ambiguous",
			yaml:    "reactor:\n  max_event_age: 3600\n",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tt.yaml), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadMasterDaemon(path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected a parse error for a unit-less non-zero duration")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadMasterDaemon: %v", err)
			}
			tt.check(t, cfg)
		})
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
		"--reactor=false",
		"--reactor-max-event-age", "30m",
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Simulate the YAML load step overwriting the struct.
	cfg := MasterDaemonDefaults()
	cfg.NatsURL = "tls://yaml:4222"
	cfg.GitFS.Remotes = []string{"git@github.com:org/repo.git"}
	cfg.Enroll.Addr = ":9443"
	cfg.Reactor.Workers = 8

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
	if cfg.Reactor.Enabled {
		t.Error("Reactor.Enabled = true, want false (explicit --reactor=false)")
	}
	if cfg.Reactor.MaxEventAge != Duration(30*time.Minute) {
		t.Errorf("Reactor.MaxEventAge = %v, want flag value 30m", cfg.Reactor.MaxEventAge)
	}
	// Flags not passed keep the YAML value.
	if cfg.Enroll.Addr != ":9443" {
		t.Errorf("Enroll.Addr = %q, want YAML value :9443", cfg.Enroll.Addr)
	}
	if cfg.Reactor.Workers != 8 {
		t.Errorf("Reactor.Workers = %d, want YAML value 8", cfg.Reactor.Workers)
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
	if cfg.StatesDir != "/var/lib/zester/states" {
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
	if cfg.StatesDir != "/var/lib/zester/states" {
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
