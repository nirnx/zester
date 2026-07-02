package main

// The former splitNonEmpty helper was replaced by the []string flag binding
// in internal/config (BindFlags/stringSliceValue). This test pins the same
// comma-split semantics at the master's flag surface: whitespace trimmed,
// empty items dropped, fully-empty input yields no remotes.

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGitfsRemotesFlagSplitSemantics(t *testing.T) {
	// Hermetic empty config file so the host's /etc/zester/master.yaml
	// (if any) cannot leak into the test.
	path := filepath.Join(t.TempDir(), "master.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a,b", []string{"a", "b"}},
		{" a , ,b ", []string{"a", "b"}},
		{",,", nil},
		{"single", []string{"single"}},
	}

	for _, tt := range tests {
		fs := flag.NewFlagSet("zester-master", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		configFile, err := setupMasterFlags(fs)
		if err != nil {
			t.Fatalf("setupMasterFlags: %v", err)
		}
		if err := fs.Parse([]string{"--config", path, "--gitfs-remotes", tt.in}); err != nil {
			t.Fatalf("Parse(%q): %v", tt.in, err)
		}
		cfg, err := loadMasterConfig(fs, *configFile)
		if err != nil {
			t.Fatalf("loadMasterConfig(%q): %v", tt.in, err)
		}
		if got := cfg.GitFS.Remotes; !reflect.DeepEqual(got, tt.want) {
			t.Errorf("--gitfs-remotes %q -> %v, want %v", tt.in, got, tt.want)
		}
	}
}
