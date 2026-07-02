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
	if len(cfg.Master.URLs) == 0 {
		cfg.Master.URLs = []string{"tls://localhost:4222"}
	}
	return &cfg, nil
}
