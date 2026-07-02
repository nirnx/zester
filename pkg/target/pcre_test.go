package target

import "testing"

func TestPCREMatcher(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		peelID  string
		want    bool
	}{
		{"simple regex", `E@web-\d+`, "web-01", true},
		{"simple no match", `E@web-\d+`, "db-01", false},
		{"dc pattern", `E@web-dc[12]-srv\d+`, "web-dc1-srv42", true},
		{"dc no match", `E@web-dc[12]-srv\d+`, "web-dc3-srv42", false},
		{"anchored", `E@^web-`, "web-01", true},
		{"anchored no match", `E@^web-`, "node-web-01", false},
		{"without prefix", `web-\d+`, "web-99", true},
		{"full match anchored", `E@^web-01$`, "web-01", true},
		{"full match no match", `E@^web-01$`, "web-011", false},
		{"dot star", `E@.*-dev-.*`, "app-dev-server", true},
		{"dot star no match", `E@.*-dev-.*`, "app-prod-server", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := NewPCREMatcher(tt.pattern)
			if err != nil {
				t.Fatalf("NewPCREMatcher(%q): %v", tt.pattern, err)
			}
			if got := m.Match(tt.peelID, nil); got != tt.want {
				t.Errorf("PCREMatcher(%q).Match(%q) = %v, want %v", tt.pattern, tt.peelID, got, tt.want)
			}
		})
	}
}

func TestPCREMatcherInvalid(t *testing.T) {
	_, err := NewPCREMatcher("E@")
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}

	_, err = NewPCREMatcher("E@[invalid")
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestPCREMatcherType(t *testing.T) {
	m, _ := NewPCREMatcher("E@test")
	if m.Type() != PCRE {
		t.Errorf("Type() = %v, want PCRE", m.Type())
	}
}
