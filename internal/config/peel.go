package config

import (
	"fmt"
	"os"

	"github.com/ptorbus/zester/internal/logging"
	"gopkg.in/yaml.v3"
)

const defaultPeelConfigPath = "/etc/zester/peel.yaml"

// PeelConfig holds configuration for the zester-peel binary. The `flag` and
// `usage` tags drive BindFlags/ApplyVisited (see bind.go), so this struct is
// the single source of truth for CLI flags, YAML fields, and defaults.
type PeelConfig struct {
	ID          string                       `yaml:"id" flag:"id" usage:"Peel identifier (required)"`
	NatsURL     string                       `yaml:"nats_url" flag:"nats-url" usage:"NATS server URL"`
	NatsCA      string                       `yaml:"nats_ca" flag:"nats-ca" usage:"CA certificate for NATS TLS server verification"`
	MasterURL   string                       `yaml:"master_url" flag:"master-url" usage:"Master enrollment API URL (e.g., https://master:8443)"`
	MasterURLs  []string                     `yaml:"master_urls" flag:"master-urls" usage:"Comma-separated master enrollment API URLs (failover; takes precedence over --master-url)"`
	EnrollCA    string                       `yaml:"enroll_ca" flag:"enroll-ca" usage:"CA certificate file for enrollment TLS verification"`
	StatesCache string                       `yaml:"states_cache" flag:"states-cache" usage:"Local cache directory for state files from KV"`
	HealthAddr  string                       `yaml:"health_addr" flag:"health-addr" usage:"Local /healthz listen address"`
	LogLevel    string                       `yaml:"log_level" flag:"log-level" usage:"Log level (debug|info|warn|error)"`
	LogFormat   string                       `yaml:"log_format" flag:"log-format" usage:"Log format (json|text)"`
	Schedule    map[string]PeelScheduleEntry `yaml:"schedule,omitempty" flag:"-"`
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
		NatsURL:     "tls://nats:4222",
		StatesCache: "/data/states-cache",
		HealthAddr:  "127.0.0.1:9090",
		LogLevel:    logging.DefaultLevel,
		LogFormat:   logging.DefaultFormat,
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

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	return &cfg, nil
}
