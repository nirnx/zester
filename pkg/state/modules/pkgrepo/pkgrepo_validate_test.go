package pkgrepomod

import (
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
)

func pkgrepoValidateBuilder(family string) func(string, map[string]any) error {
	mctx := testPkgrepoMctx(exectest.NewFakeFileExec(), exectest.NewFakeCommandExec(), family, "")
	build := NewPkgrepoManagedBuilder(mctx, modschema.DecodeOptions{})
	return func(id string, cfg map[string]any) error {
		_, err := build(id, cfg)
		return err
	}
}

// TestPkgrepoManagedRejectsUnsafeRepoName pins the builder-tail name
// allowlist: the repository name becomes a filename under
// /etc/apt/sources.list.d, /etc/yum.repos.d and /etc/apt/keyrings, plus the
// yum section header — path separators, traversal, whitespace, and control
// characters are refused.
func TestPkgrepoManagedRejectsUnsafeRepoName(t *testing.T) {
	build := pkgrepoValidateBuilder("debian")
	bad := []string{
		"../../etc/cron.d/evil",
		"..",
		".",
		"a/b",
		"docker ce",
		"docker\n[evil]",
		"docker\tce",
		"docker;rm",
		"$(id)",
		"docker\x00",
	}
	for _, name := range bad {
		err := build(name, map[string]any{"baseurl": "deb https://example.com stable main"})
		if err == nil {
			t.Errorf("name %q: expected builder error, got nil", name)
			continue
		}
		if !strings.Contains(err.Error(), "repository name") {
			t.Errorf("name %q: error %q should say the repository name is invalid", name, err)
		}
		// The explicit `name` key takes the same path.
		if err := build("x", map[string]any{"name": name, "baseurl": "deb https://example.com stable main"}); err == nil {
			t.Errorf("explicit name %q: expected builder error, got nil", name)
		}
	}
	for _, name := range []string{"docker", "docker-ce", "epel", "deadsnakes_ppa", "ubuntu.focal", "repo-1.0"} {
		if err := build(name, map[string]any{"baseurl": "deb https://example.com stable main"}); err != nil {
			t.Errorf("name %q: unexpected builder error %v", name, err)
		}
	}
}

// TestPkgrepoManagedRejectsControlCharsInRepoFileValues pins the validation
// of the values written line-by-line into the repo file: a newline in
// humanname, baseurl, or key_url would append an extra, unmanaged repository
// line (or break out of the yum section).
func TestPkgrepoManagedRejectsControlCharsInRepoFileValues(t *testing.T) {
	for _, family := range []string{"debian", "redhat"} {
		build := pkgrepoValidateBuilder(family)
		bad := []struct {
			name   string
			config map[string]any
			field  string
		}{
			{"newline in baseurl", map[string]any{"baseurl": "deb https://example.com stable main\ndeb http://evil.example.com evil main"}, "baseurl"},
			{"carriage return in baseurl", map[string]any{"baseurl": "https://example.com/repo\r"}, "baseurl"},
			{"newline in humanname", map[string]any{"baseurl": "https://example.com/repo", "humanname": "Docker\n[evil]\nbaseurl=http://evil"}, "humanname"},
			{"newline in key_url", map[string]any{"baseurl": "https://example.com/repo", "key_url": "https://example.com/gpg\ngpgcheck=0"}, "key_url"},
			{"NUL in key_url", map[string]any{"baseurl": "https://example.com/repo", "key_url": "https://example.com/gpg\x00"}, "key_url"},
		}
		for _, tc := range bad {
			err := build("docker", tc.config)
			if err == nil {
				t.Errorf("%s/%s: expected builder error, got nil", family, tc.name)
				continue
			}
			if !strings.Contains(err.Error(), tc.field+":") {
				t.Errorf("%s/%s: error %q should name the %q parameter", family, tc.name, err, tc.field)
			}
		}
		good := map[string]any{
			"baseurl":   "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu jammy stable",
			"humanname": "Docker CE (stable)",
			"key_url":   "https://download.docker.com/linux/ubuntu/gpg",
		}
		if err := build("docker", good); err != nil {
			t.Errorf("%s: ordinary values rejected: %v", family, err)
		}
	}
}
