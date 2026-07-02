package target

import "testing"

func TestGlobMatcher(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		peelID  string
		want    bool
	}{
		{"wildcard all", "*", "web-01", true},
		{"prefix match", "web*", "web-01", true},
		{"prefix no match", "web*", "db-01", false},
		{"exact match", "web-01", "web-01", true},
		{"exact no match", "web-01", "web-02", false},
		{"question mark", "web-??", "web-01", true},
		{"question mark no match", "web-??", "web-001", false},
		{"character class", "web-[01][0-9]", "web-01", true},
		{"character class no match", "web-[01][0-9]", "web-21", false},
		{"complex pattern", "dc?-web-*", "dc1-web-server-01", true},
		{"complex no match", "dc?-web-*", "dc12-web-server-01", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := NewGlobMatcher(tt.pattern)
			if err != nil {
				t.Fatalf("NewGlobMatcher(%q): %v", tt.pattern, err)
			}
			if got := m.Match(tt.peelID, nil); got != tt.want {
				t.Errorf("GlobMatcher(%q).Match(%q) = %v, want %v", tt.pattern, tt.peelID, got, tt.want)
			}
		})
	}
}

func TestGlobMatcherInvalid(t *testing.T) {
	_, err := NewGlobMatcher("")
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}

	_, err = NewGlobMatcher("[invalid")
	if err == nil {
		t.Fatal("expected error for invalid pattern")
	}
}

func TestGlobMatcherType(t *testing.T) {
	m, _ := NewGlobMatcher("web*")
	if m.Type() != Glob {
		t.Errorf("Type() = %v, want Glob", m.Type())
	}
	if m.String() != "web*" {
		t.Errorf("String() = %q, want %q", m.String(), "web*")
	}
}
