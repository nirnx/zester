// Package config provides configuration loading for the Zester CLI.
// Configuration is read from /etc/zester/master.yaml or ~/.zester/config.yaml.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v2"
)

// Config holds the CLI configuration for connecting to a Zester master.
type Config struct {
	Master MasterConfig `yaml:"master"`
}

// MasterConfig holds connection details for the master's NATS server.
type MasterConfig struct {
	URLs         []string `yaml:"urls"`
	CredsFile    string   `yaml:"creds_file"`
	NKeySeedFile string   `yaml:"nkey_seed_file"`
	TLSCert      string   `yaml:"tls_cert"`
	TLSKey       string   `yaml:"tls_key"`
	TLSCA        string   `yaml:"tls_ca"`
}

// DefaultPaths returns the ordered list of config file paths to search.
func DefaultPaths() []string {
	paths := []string{"/etc/zester/master.yaml"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".zester", "config.yaml"))
	}
	return paths
}

// Load reads the first available configuration file from the default paths.
// If configPath is non-empty, it is used instead.
func Load(configPath string) (*Config, error) {
	if configPath != "" {
		return loadFile(configPath)
	}

	for _, p := range DefaultPaths() {
		cfg, err := loadFile(p)
		if err == nil {
			return cfg, nil
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("config: read %s: %w", p, err)
		}
	}

	// Return defaults when no config file exists.
	return &Config{
		Master: MasterConfig{
			URLs: []string{"tls://localhost:4222"},
		},
	}, nil
}

func loadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	// All-in-one convenience: the CLI's default search hits the master
	// DAEMON config (/etc/zester/master.yaml) first, which has no `master:`
	// block — so on the master host the CLI would otherwise get no creds/CA
	// and fail NATS TLS with a bare cert error. When there's no `master:`
	// block, derive the connection from the daemon's own fields: its NATS
	// URL, its nats_ca, and the admin credentials in its auth_dir.
	if len(cfg.Master.URLs) == 0 && cfg.Master.CredsFile == "" && cfg.Master.TLSCA == "" {
		var dc daemonConnFields
		if yaml.Unmarshal(data, &dc) == nil && (dc.NatsURL != "" || dc.AuthDir != "") {
			if dc.NatsURL != "" {
				cfg.Master.URLs = []string{dc.NatsURL}
			}
			cfg.Master.TLSCA = dc.NatsCA
			authDir := dc.AuthDir
			if authDir == "" {
				authDir = "/var/lib/zester/auth"
			}
			creds := filepath.Join(authDir, "admin.creds")
			if _, statErr := os.Stat(creds); statErr == nil {
				cfg.Master.CredsFile = creds
			}
		}
	}

	if len(cfg.Master.URLs) == 0 {
		cfg.Master.URLs = []string{"tls://localhost:4222"}
	}
	return &cfg, nil
}

// daemonConnFields captures the subset of the master DAEMON config the CLI can
// derive its connection from when the file carries no `master:` block.
type daemonConnFields struct {
	NatsURL string `yaml:"nats_url"`
	NatsCA  string `yaml:"nats_ca"`
	AuthDir string `yaml:"auth_dir"`
}
