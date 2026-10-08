package sshauthmod

import (
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
)

// TestSSHAuthPresentRejectsLineInjection pins the builder-tail validation:
// authorized_keys is line-oriented, so a key or comment carrying a newline
// (the classic source is another peel's facts via basket()) would splice a
// second, unmanaged key into the file. The builder must refuse to build.
func TestSSHAuthPresentRejectsLineInjection(t *testing.T) {
	build := NewSSHAuthPresentBuilder(testSSHAuthMctx(exectest.NewFakeFileExec()), modschema.DecodeOptions{})

	bad := []struct {
		name   string
		id     string
		config map[string]any
		field  string
	}{
		{"newline in key blob", "AAAAKEY\nssh-rsa AAAAEVIL attacker", map[string]any{"config": sshAuthTestPath}, "name"},
		{"newline in explicit name", "k", map[string]any{"name": "AAAAKEY\nssh-rsa AAAAEVIL", "config": sshAuthTestPath}, "name"},
		{"carriage return inside key", "AAAA\rKEY", map[string]any{"config": sshAuthTestPath}, "name"},
		{"NUL in key", "AAAA\x00KEY", map[string]any{"config": sshAuthTestPath}, "name"},
		{"newline in comment", "AAAAKEY", map[string]any{"config": sshAuthTestPath, "comment": "ci\nssh-rsa AAAAEVIL"}, "comment"},
		{"tab in comment", "AAAAKEY", map[string]any{"config": sshAuthTestPath, "comment": "a\tb"}, "comment"},
		{"space in enc", "AAAAKEY", map[string]any{"config": sshAuthTestPath, "enc": "ssh rsa"}, "enc"},
		{"newline in enc", "AAAAKEY", map[string]any{"config": sshAuthTestPath, "enc": "ssh-rsa\n"}, "enc"},
	}
	for _, tc := range bad {
		_, err := build(tc.id, tc.config)
		if err == nil {
			t.Errorf("%s: expected builder error, got nil", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.field+":") {
			t.Errorf("%s: error %q should name the %q parameter", tc.name, err, tc.field)
		}
	}
}

func TestSSHAuthPresentAcceptsOrdinaryValues(t *testing.T) {
	build := NewSSHAuthPresentBuilder(testSSHAuthMctx(exectest.NewFakeFileExec()), modschema.DecodeOptions{})
	good := []struct {
		id     string
		config map[string]any
	}{
		{"AAAAB3NzaC1yc2EAAAADAQABAAAB", map[string]any{"config": sshAuthTestPath}},
		// A full key line has interior spaces — those are legitimate.
		{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA alice@laptop", map[string]any{"config": sshAuthTestPath}},
		// Surrounding whitespace is trimmed BEFORE validation, so it passes.
		{"  AAAAKEY  ", map[string]any{"config": sshAuthTestPath}},
		{"AAAAKEY", map[string]any{"config": sshAuthTestPath, "enc": "ssh-ed25519", "comment": "deploy key for ci"}},
	}
	for _, tc := range good {
		if _, err := build(tc.id, tc.config); err != nil {
			t.Errorf("id %q config %v: unexpected builder error %v", tc.id, tc.config, err)
		}
	}
}
