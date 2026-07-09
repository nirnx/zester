package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const defaultMasterConfigPath = "/etc/zester/master.yaml"

// MasterDaemonConfig holds configuration for the zester-master binary.
//
// The `flag`/`usage` tags drive BindFlags/ApplyVisited (see bind.go): each
// tagged field becomes a CLI flag whose default is the field's value from
// MasterDaemonDefaults, with precedence flag > YAML config file > default.
type MasterDaemonConfig struct {
	NatsURL           string        `yaml:"nats_url" flag:"nats-url" usage:"NATS server URL"`
	NatsCA            string        `yaml:"nats_ca" flag:"nats-ca" usage:"CA certificate for NATS TLS server verification"`
	AuthDir           string        `yaml:"auth_dir" flag:"auth-dir" usage:"Directory containing auth files (master.creds, account.seed)"`
	StatesDir         string        `yaml:"states_dir" flag:"states-dir" usage:"Root directory for state files"`
	SettingsDir       string        `yaml:"settings_dir" flag:"settings-dir" usage:"Root directory for settings files"`
	JetStreamReplicas int           `yaml:"jetstream_replicas" flag:"jetstream-replicas" usage:"JetStream replication factor (0 = auto: min(3, detected cluster size); explicit count overrides)"`
	HealthAddr        string        `yaml:"health_addr" flag:"health-addr" usage:"Local /healthz listen address"`
	LogLevel          string        `yaml:"log_level" flag:"log-level" usage:"Log level (debug, info, warn, error)"`
	LogFormat         string        `yaml:"log_format" flag:"log-format" usage:"Log format (json, text)"`
	Enroll            MasterEnroll  `yaml:"enroll"`
	API               MasterAPI     `yaml:"api"`
	GitFS             GitFSConfig   `yaml:"gitfs"`
	Reactor           MasterReactor `yaml:"reactor"`
	CA                MasterCA      `yaml:"ca"`
	NatsAdvertise     []string      `yaml:"nats_advertise_urls" flag:"nats-advertise-urls" usage:"Fleet-facing NATS URLs served to peels via enrollment discovery (tls:// only, no loopback); empty disables discovery"`

	// FilesRepublishInterval is the lease holder's periodic re-walk of the
	// settings/states/reactor dirs (hash-gated: an unchanged tree writes
	// nothing). The backstop for edits the file watcher misses; 0 disables.
	FilesRepublishInterval Duration `yaml:"files_republish_interval" flag:"files-republish-interval" usage:"Periodic republish of settings/state/reactor files by the lease holder (hash-gated; 0 disables)"`

	// FilesWatch enables the fsnotify watcher on the settings/states/reactor
	// dirs: on-disk edits publish within ~1s instead of waiting for the
	// republish tick. inotify on Linux; unreliable on NFS/remote mounts,
	// which is why the interval backstop stays on.
	FilesWatch bool `yaml:"files_watch" flag:"files-watch" usage:"Watch settings/state/reactor dirs and publish on change (lease holder only)"`

	// FilesMirror makes STANDBY masters mirror the published file sets from
	// KV into their local source dirs, so every master's dirs track fleet
	// truth and a failover never republishes a stale tree. Disable when
	// masters share one filesystem for these dirs (shared volume/NFS — the
	// holder's publishes already ARE the standby's dirs) or when a set is
	// GitFS-sourced (auto-excluded).
	FilesMirror bool `yaml:"files_mirror" flag:"files-mirror" usage:"Standby masters mirror published settings/state/reactor files from KV into their local dirs"`

	// PublisherStatusFile is rewritten on every publisher-lease transition
	// (role, master id, hostname, since) — the packaged MOTD snippet reads
	// it to warn operators logging into a standby that file edits there are
	// not published. Empty disables. The packaged unit's RuntimeDirectory
	// provides /run/zester.
	PublisherStatusFile string `yaml:"publisher_status_file" flag:"publisher-status-file" usage:"File rewritten with this master's publisher-lease role on every transition (empty disables)"`
}

// MasterCA configures the embedded certificate authority. Mode selects how
// the enrollment TLS certificate is obtained:
//
//	auto      embedded iff <ca-dir>/root.crt exists, else external (default)
//	embedded  load the embedded CA and self-issue/renew the enroll cert;
//	          fail startup if CA material is absent
//	external  never touch CA material; require operator-provided enroll certs
//	          (today's behavior)
type MasterCA struct {
	Mode               string   `yaml:"mode" flag:"ca-mode" usage:"CA mode: auto|embedded|external (default auto)"`
	Dir                string   `yaml:"dir" flag:"ca-dir" usage:"Directory holding embedded CA material (default <auth_dir>/ca)"`
	EnrollCertValidity Duration `yaml:"enroll_cert_validity" flag:"ca-enroll-cert-validity" usage:"Validity of the self-issued enrollment certificate"`
	EnrollSANs         []string `yaml:"enroll_sans" flag:"ca-enroll-sans" usage:"Extra DNS/IP SANs for the self-issued enrollment certificate (hostname and localhost always included)"`
}

// MasterReactor holds reactor engine configuration (event-driven reactions).
// Zero values in YAML mean "disabled" for the gates that support it:
// default_throttle 0 = no default refractory period, max_event_age 0 = no
// staleness gate, source_rate_limit 0 = no per-source rate limit, and
// storm_rate 0 = no circuit breaker (masterd translates these to the
// pkg/reactor knob conventions). The duration knobs use config.Duration so
// the documented bare-`0` YAML form parses (yaml.v3 rejects bare integers
// for plain time.Duration fields).
type MasterReactor struct {
	Enabled         bool     `yaml:"enabled" flag:"reactor" usage:"Enable the reactor engine (event-driven reactions)"`
	Dir             string   `yaml:"dir" flag:"reactor-dir" usage:"Local directory holding reactor rule files (top.zy + reaction .zy files)"`
	Workers         int      `yaml:"workers" flag:"reactor-workers" usage:"Reactor render/execute worker pool size"`
	MaxChainDepth   int      `yaml:"max_chain_depth" flag:"reactor-max-chain-depth" usage:"Maximum reaction chain depth before events are dropped"`
	EnableChaining  bool     `yaml:"enable_chaining" flag:"reactor-enable-chaining" usage:"Allow reaction rules to emit derived events (event.send)"`
	DefaultThrottle Duration `yaml:"default_throttle" flag:"reactor-default-throttle" usage:"Default per-(rule,source) refractory period (0 = none)"`
	SourceRateLimit int      `yaml:"source_rate_limit" flag:"reactor-source-rate-limit" usage:"Per-source event rate limit in events/minute (0 = unlimited)"`
	MaxEventAge     Duration `yaml:"max_event_age" flag:"reactor-max-event-age" usage:"Drop events older than this at consume time (0 = full replay)"`
	StormRate       int      `yaml:"storm_rate" flag:"reactor-storm-rate" usage:"Per-rule fires/minute that trips the circuit breaker (0 = no breaker)"`
	BreakerCooldown Duration `yaml:"breaker_cooldown" flag:"reactor-breaker-cooldown" usage:"How long a tripped reaction circuit breaker stays open"`
}

// MasterEnroll holds enrollment HTTP API configuration.
type MasterEnroll struct {
	Addr    string `yaml:"addr" flag:"enroll-addr" usage:"Enrollment HTTP API listen address"`
	TLSCert string `yaml:"tls_cert" flag:"enroll-tls-cert" usage:"TLS certificate for enrollment API (required)"`
	TLSKey  string `yaml:"tls_key" flag:"enroll-tls-key" usage:"TLS private key for enrollment API (required)"`
}

// MasterAPI holds REST API configuration for master-side automation routes.
// DocsEnabled defaults to false: the Swagger UI and OpenAPI spec are served
// unauthenticated on the peel-facing enrollment listener, so exposing them
// is an explicit opt-in.
type MasterAPI struct {
	DocsEnabled bool             `yaml:"docs_enabled" flag:"api-docs" usage:"Serve Swagger UI and OpenAPI spec (unauthenticated) on the enrollment listener"`
	Tokens      []MasterAPIToken `yaml:"tokens"`
}

// MasterAPIToken configures one API client identity with a token file.
type MasterAPIToken struct {
	Username  string `yaml:"username"`
	TokenFile string `yaml:"token_file"`
}

// GitFSConfig holds Git-based state file sync configuration.
//
// Remotes is bound as a comma-separated []string flag: an explicitly empty
// --gitfs-remotes "" disables GitFS even when the YAML config lists remotes
// (ApplyVisited writes the empty parse result back over the YAML value).
type GitFSConfig struct {
	Remotes  []string      `yaml:"remotes" flag:"gitfs-remotes" usage:"Comma-separated Git remote URLs for state file sync"`
	Interval time.Duration `yaml:"interval" flag:"gitfs-interval" usage:"GitFS pull interval"`
	SSHKey   string        `yaml:"ssh_key" flag:"gitfs-ssh-key" usage:"Path to SSH private key for GitFS authentication"`
}

// MasterDaemonDefaults returns a MasterDaemonConfig with all default values.
func MasterDaemonDefaults() MasterDaemonConfig {
	return MasterDaemonConfig{
		NatsURL:     "tls://nats:4222",
		AuthDir:     "/var/lib/zester/auth",
		StatesDir:   "/var/lib/zester/states",
		SettingsDir: "/var/lib/zester/settings",
		HealthAddr:  "127.0.0.1:9091",

		FilesRepublishInterval: Duration(30 * time.Second),
		FilesWatch:             true,
		FilesMirror:            true,
		PublisherStatusFile:    "/run/zester/publisher-status",
		LogLevel:               "info",
		LogFormat:              "json",
		Enroll: MasterEnroll{
			Addr:    ":8443",
			TLSCert: "/var/lib/zester/auth/enroll.crt",
			TLSKey:  "/var/lib/zester/auth/enroll.key",
		},
		GitFS: GitFSConfig{
			Interval: 5 * time.Minute,
		},
		CA: MasterCA{
			Mode:               "auto",
			EnrollCertValidity: Duration(90 * 24 * time.Hour),
		},
		Reactor: MasterReactor{
			Enabled:         true,
			Dir:             "/var/lib/zester/reactor",
			Workers:         4,
			MaxChainDepth:   3,
			EnableChaining:  true,
			DefaultThrottle: 0,
			SourceRateLimit: 120,
			MaxEventAge:     Duration(time.Hour),
			StormRate:       60,
			BreakerCooldown: Duration(5 * time.Minute),
		},
	}
}

// LoadMasterDaemon loads the master daemon configuration. If configPath is
// non-empty, that file is read. Otherwise /etc/zester/master.yaml is tried.
// Returns defaults if no file is found.
func LoadMasterDaemon(configPath string) (*MasterDaemonConfig, error) {
	cfg := MasterDaemonDefaults()

	path := configPath
	if path == "" {
		path = defaultMasterConfigPath
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && configPath == "" {
			return &cfg, nil
		}
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config: %s not found", path)
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	return &cfg, nil
}
