package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoad_DerivesFromDaemonConfig verifies the all-in-one convenience: a CLI
// pointed at the master DAEMON config (no `master:` block) derives its NATS
// URL, CA, and admin creds from the daemon fields.
func TestLoad_DerivesFromDaemonConfig(t *testing.T) {
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auth")
	if err := os.MkdirAll(authDir, 0755); err != nil {
		t.Fatal(err)
	}
	credsPath := filepath.Join(authDir, "admin.creds")
	if err := os.WriteFile(credsPath, []byte("creds"), 0600); err != nil {
		t.Fatal(err)
	}
	// A daemon-style master.yaml: NO `master:` block.
	daemonYAML := "nats_url: \"tls://nats.example:4222\"\nnats_ca: \"" + authDir + "/nats-ca.crt\"\nauth_dir: \"" + authDir + "\"\n"
	path := filepath.Join(dir, "master.yaml")
	if err := os.WriteFile(path, []byte(daemonYAML), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Master.URLs) != 1 || cfg.Master.URLs[0] != "tls://nats.example:4222" {
		t.Errorf("URLs = %v, want [tls://nats.example:4222]", cfg.Master.URLs)
	}
	if cfg.Master.TLSCA != authDir+"/nats-ca.crt" {
		t.Errorf("TLSCA = %q, want derived nats_ca", cfg.Master.TLSCA)
	}
	if cfg.Master.CredsFile != credsPath {
		t.Errorf("CredsFile = %q, want derived admin.creds %q", cfg.Master.CredsFile, credsPath)
	}
}

// TestLoad_ExplicitMasterBlockWins verifies a real CLI config (with a
// `master:` block) is used verbatim, not overridden by derivation.
func TestLoad_ExplicitMasterBlockWins(t *testing.T) {
	dir := t.TempDir()
	cliYAML := "master:\n  urls: [\"tls://explicit:4222\"]\n  creds_file: /x/admin.creds\n"
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(cliYAML), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Master.URLs[0] != "tls://explicit:4222" || cfg.Master.CredsFile != "/x/admin.creds" {
		t.Errorf("explicit master block not honored: %+v", cfg.Master)
	}
}
