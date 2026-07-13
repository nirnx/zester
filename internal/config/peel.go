package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/nirnx/zester/internal/logging"
	"gopkg.in/yaml.v3"
)

const defaultPeelConfigPath = "/etc/zester/peel.yaml"

// PeelConfig holds configuration for the zester-peel binary. The `flag` and
// `usage` tags drive BindFlags/ApplyVisited (see bind.go), so this struct is
// the single source of truth for CLI flags, YAML fields, and defaults.
type PeelConfig struct {
	ID          string   `yaml:"id" flag:"id" usage:"Peel identifier (required)"`
	NatsURL     string   `yaml:"nats_url" flag:"nats-url" usage:"NATS server URL"`
	NatsCA      string   `yaml:"nats_ca" flag:"nats-ca" usage:"CA certificate for NATS TLS server verification"`
	MasterURL   string   `yaml:"master_url" flag:"master-url" usage:"Master enrollment API URL (e.g., https://master:8443)"`
	MasterURLs  []string `yaml:"master_urls" flag:"master-urls" usage:"Comma-separated master enrollment API URLs (failover; takes precedence over --master-url)"`
	EnrollCA    string   `yaml:"enroll_ca" flag:"enroll-ca" usage:"CA certificate file for enrollment TLS verification"`
	EnrollCAPin []string `yaml:"enroll_ca_pin" flag:"enroll-ca-pin" usage:"sha256:<hex> SPKI pin(s) of the master CA root for verified first-contact enrollment"`
	EnrollTrust string   `yaml:"enroll_trust" flag:"enroll-trust" usage:"Fallback enrollment trust when no enroll_ca/enroll_ca_pin: tofu (trust first contact, default) or strict (fail closed)"`
	AuthDir     string   `yaml:"auth_dir" flag:"auth-dir" usage:"Directory for peel credentials and trust material"`
	DataDir     string   `yaml:"data_dir" flag:"data-dir" usage:"Directory for peel runtime state (settings snapshot, job dedup, baked states)"`
	StatesCache string   `yaml:"states_cache" flag:"states-cache" usage:"Local cache directory for state files from KV"`
	HealthAddr  string   `yaml:"health_addr" flag:"health-addr" usage:"Local /healthz listen address"`
	LogLevel    string   `yaml:"log_level" flag:"log-level" usage:"Log level (debug|info|warn|error)"`
	LogFormat   string   `yaml:"log_format" flag:"log-format" usage:"Log format (json|text)"`
	// StrictParams controls the unknown-state-module-parameter policy. When true
	// (the default, pre-v1 endgame) a typo'd or unrecognized parameter on a
	// migrated (Spec-carrying) state module FAILS the build with a typed
	// UnknownKeyError that names the module, the key, and a did-you-mean
	// suggestion. Set false to relax to the historical behavior: a Warn is logged
	// and the state still builds.
	StrictParams bool                         `yaml:"strict_params" flag:"strict-params" usage:"Fail state builds on unknown module parameters (default true); false logs a warning and continues"`
	Schedule     map[string]PeelScheduleEntry `yaml:"schedule,omitempty" flag:"-"`
}

// PeelScheduleEntry is a schedule entry in peel.yaml.
type PeelScheduleEntry struct {
	Module     string         `yaml:"module"`
	Args       map[string]any `yaml:"args,omitempty"`
	Interval   string         `yaml:"interval,omitempty"`
	Cron       string         `yaml:"cron,omitempty"`
	Splay      string         `yaml:"splay,omitempty"`
	MaxRunning int            `yaml:"maxrunning,omitempty"`
	RunOnStart bool           `yaml:"run_on_start,omitempty"`
	ReturnJob  bool           `yaml:"return_job,omitempty"`
	Enabled    *bool          `yaml:"enabled,omitempty"`
}

// PeelDefaults returns a PeelConfig with all default values.
func PeelDefaults() PeelConfig {
	return PeelConfig{
		// NatsURL default is empty (sentinel): an unset nats_url means the
		// peel discovers its NATS endpoints (bootstrap cache → enrollment
		// discovery → the builtin tail). A non-empty value (flag or YAML) is
		// an explicit operator override that wins over discovery. The builtin
		// tail preserves the historical tls://nats:4222 default.
		NatsURL:     "",
		AuthDir:     "/var/lib/zester/auth",
		DataDir:     "/var/lib/zester",
		StatesCache: "/var/cache/zester/states",
		HealthAddr:  "127.0.0.1:9090",
		LogLevel:    logging.DefaultLevel,
		LogFormat:   logging.DefaultFormat,
		// StrictParams defaults ON (the program's endgame): an unknown
		// state-module parameter fails the build. strict_params: false relaxes
		// to the historical Warn-and-continue behavior.
		StrictParams: true,
	}
}

// LoadPeel loads the peel configuration. If configPath is non-empty, that file
// is read. Otherwise /etc/zester/peel.yaml is tried. Returns defaults if no
// file is found.
func LoadPeel(configPath string) (*PeelConfig, error) {
	cfg := PeelDefaults()

	path := configPath
	if path == "" {
		path = defaultPeelConfigPath
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

	// Strict decoding: an unknown key fails startup loudly instead of being
	// silently discarded. This is the typo guard for the security-relevant
	// trust knobs — a mistyped `enroll_ca_pin` must not silently leave the
	// peel in the weakest (TOFU) mode believing it is pinned.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	return &cfg, nil
}
