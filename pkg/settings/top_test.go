package settings

import (
	"testing"
)

func TestParseTopFile_Basic(t *testing.T) {
	data := []byte(`
base:
  '*':
    - common.base
  'role:webserver':
    - webservers.nginx
    - webservers.certs
  'os:ubuntu':
    - databases.postgres
`)

	top, err := ParseTopFile(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(top.Environments) != 1 || top.Environments[0].Name != "base" {
		t.Fatal("missing 'base' environment")
	}

	if len(top.Environments[0].Entries) != 3 {
		t.Errorf("entries count = %d, want 3", len(top.Environments[0].Entries))
	}
}

func TestParseTopFile_MultipleEnvs(t *testing.T) {
	data := []byte(`
base:
  '*':
    - common.base
production:
  'env:production':
    - prod.secrets
`)

	top, err := ParseTopFile(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(top.Environments) != 2 {
		t.Errorf("environments count = %d, want 2", len(top.Environments))
	}
	if top.Environments[0].Name != "base" {
		t.Errorf("first environment = %q, want %q", top.Environments[0].Name, "base")
	}
	if top.Environments[1].Name != "production" {
		t.Errorf("second environment = %q, want %q", top.Environments[1].Name, "production")
	}
}

func TestParseTopFile_Invalid(t *testing.T) {
	_, err := ParseTopFile([]byte("not: [valid: yaml: structure"))
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestTopFile_ResolveForPeel_All(t *testing.T) {
	top := &TopFile{
		Environments: []Environment{
			{Name: "base", Entries: []TargetEntry{
				{Pattern: "*", SettingsRefs: []string{"common.base"}},
			}},
		},
	}

	refs := top.ResolveForPeel("any-peel", nil, &SimpleTargetMatcher{})
	if len(refs) != 1 || refs[0] != "common.base" {
		t.Errorf("refs = %v, want [common.base]", refs)
	}
}

func TestTopFile_ResolveForPeel_FactBased(t *testing.T) {
	top := &TopFile{
		Environments: []Environment{
			{Name: "base", Entries: []TargetEntry{
				{Pattern: "*", SettingsRefs: []string{"common.base"}},
				{Pattern: "role:webserver", SettingsRefs: []string{"webservers.nginx"}},
				{Pattern: "role:database", SettingsRefs: []string{"databases.postgres"}},
			}},
		},
	}

	facts := map[string]any{"role": "webserver"}
	refs := top.ResolveForPeel("web-01", facts, &SimpleTargetMatcher{})

	expected := map[string]bool{"common.base": true, "webservers.nginx": true}
	if len(refs) != 2 {
		t.Fatalf("refs count = %d, want 2, got %v", len(refs), refs)
	}
	for _, r := range refs {
		if !expected[r] {
			t.Errorf("unexpected ref: %s", r)
		}
	}
}

func TestTopFile_ResolveForPeel_GlobPattern(t *testing.T) {
	top := &TopFile{
		Environments: []Environment{
			{Name: "base", Entries: []TargetEntry{
				{Pattern: "web*", SettingsRefs: []string{"webservers.nginx"}},
				{Pattern: "db*", SettingsRefs: []string{"databases.postgres"}},
			}},
		},
	}

	refs := top.ResolveForPeel("web-01", nil, &SimpleTargetMatcher{})
	if len(refs) != 1 || refs[0] != "webservers.nginx" {
		t.Errorf("refs = %v, want [webservers.nginx]", refs)
	}
}

func TestTopFile_ResolveForPeel_NoDuplicates(t *testing.T) {
	top := &TopFile{
		Environments: []Environment{
			{Name: "base", Entries: []TargetEntry{
				{Pattern: "*", SettingsRefs: []string{"common.base"}},
				{Pattern: "web*", SettingsRefs: []string{"common.base"}},
			}},
		},
	}

	refs := top.ResolveForPeel("web-01", nil, &SimpleTargetMatcher{})
	if len(refs) != 1 {
		t.Errorf("refs = %v, want [common.base] (no duplicates)", refs)
	}
}

func TestSimpleTargetMatcher_Wildcard(t *testing.T) {
	m := &SimpleTargetMatcher{}
	if !m.Match("*", "any", nil) {
		t.Error("* should match any peel")
	}
}

func TestSimpleTargetMatcher_Glob(t *testing.T) {
	m := &SimpleTargetMatcher{}

	tests := []struct {
		pattern string
		peelID  string
		want    bool
	}{
		{"web*", "web-01", true},
		{"web*", "db-01", false},
		{"web-??", "web-01", true},
		{"web-??", "web-001", false},
	}

	for _, tt := range tests {
		if got := m.Match(tt.pattern, tt.peelID, nil); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.peelID, got, tt.want)
		}
	}
}

func TestSimpleTargetMatcher_Fact(t *testing.T) {
	m := &SimpleTargetMatcher{}

	facts := map[string]any{
		"role": "webserver",
		"os":   "linux",
	}

	tests := []struct {
		pattern string
		want    bool
	}{
		{"role:webserver", true},
		{"role:database", false},
		{"os:linux", true},
		{"os:windows", false},
		{"missing:value", false},
	}

	for _, tt := range tests {
		if got := m.Match(tt.pattern, "peel-01", facts); got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.pattern, got, tt.want)
		}
	}
}
