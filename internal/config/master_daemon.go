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
	NatsURL           string       `yaml:"nats_url" flag:"nats-url" usage:"NATS server URL"`
	NatsCA            string       `yaml:"nats_ca" flag:"nats-ca" usage:"CA certificate for NATS TLS server verification"`
	AuthDir           string       `yaml:"auth_dir" flag:"auth-dir" usage:"Directory containing auth files (master.creds, account.seed)"`
	StatesDir         string       `yaml:"states_dir" flag:"states-dir" usage:"Root directory for state files"`
	SettingsDir       string       `yaml:"settings_dir" flag:"settings-dir" usage:"Root directory for settings files"`
	JetStreamReplicas int          `yaml:"jetstream_replicas" flag:"jetstream-replicas" usage:"JetStream replication factor (0 = auto: min(3, detected cluster size); explicit count overrides)"`
	HealthAddr        string       `yaml:"health_addr" flag:"health-addr" usage:"Local /healthz listen address"`
	LogLevel          string       `yaml:"log_level" flag:"log-level" usage:"Log level (debug, info, warn, error)"`
	LogFormat         string       `yaml:"log_format" flag:"log-format" usage:"Log format (json, text)"`
	Enroll            MasterEnroll `yaml:"enroll"`
	API               MasterAPI    `yaml:"api"`
	GitFS             GitFSConfig  `yaml:"gitfs"`
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
		AuthDir:     "/data/auth",
		StatesDir:   "/data/states",
		SettingsDir: "/data/settings",
		HealthAddr:  "127.0.0.1:9091",
		LogLevel:    "info",
		LogFormat:   "json",
		Enroll: MasterEnroll{
			Addr:    ":8443",
			TLSCert: "/data/auth/enroll.crt",
			TLSKey:  "/data/auth/enroll.key",
		},
		GitFS: GitFSConfig{
			Interval: 5 * time.Minute,
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
