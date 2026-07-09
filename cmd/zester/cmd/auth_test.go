package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/auth"
)

// TestAuthInit exercises 'zester nats-auth init': it generates the full hierarchy,
// writes a loadable nats-server.conf, produces usable creds, and refuses to
// clobber without --force.
func TestAuthInit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "auth")
	conf := filepath.Join(t.TempDir(), "nats-server.conf")

	if err := authCmd.PersistentFlags().Set("dir", dir); err != nil {
		t.Fatal(err)
	}
	if err := authInitCmd.Flags().Set("nats-conf", conf); err != nil {
		t.Fatal(err)
	}
	if err := authInitCmd.RunE(authInitCmd, nil); err != nil {
		t.Fatalf("nats-auth init: %v", err)
	}

	for _, f := range []string{"operator.jwt", "account.jwt", "account.seed", "master.creds", "admin.creds"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	// account.seed is the fleet trust root — must be 0600.
	if fi, err := os.Stat(filepath.Join(dir, "account.seed")); err == nil && fi.Mode().Perm() != 0600 {
		t.Errorf("account.seed mode = %v, want 0600", fi.Mode().Perm())
	}

	// nats-server.conf references the operator JWT and both accounts.
	confData, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"operator:", "system_account:", "resolver: MEMORY", "jetstream {"} {
		if !strings.Contains(string(confData), want) {
			t.Errorf("nats-server.conf missing %q", want)
		}
	}

	// admin.creds is a usable creds file (JWT + seed parse).
	if _, err := auth.LoadCredsFile(filepath.Join(dir, "admin.creds")); err != nil {
		t.Errorf("admin.creds not loadable: %v", err)
	}

	// Refuse to overwrite without --force.
	if err := authInitCmd.RunE(authInitCmd, nil); err == nil {
		t.Error("second nats-auth init without --force succeeded, want refusal")
	}
	// --force overwrites.
	if err := authInitCmd.Flags().Set("force", "true"); err != nil {
		t.Fatal(err)
	}
	if err := authInitCmd.RunE(authInitCmd, nil); err != nil {
		t.Errorf("nats-auth init --force: %v", err)
	}
	_ = authInitCmd.Flags().Set("force", "false")
}
