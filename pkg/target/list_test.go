package target

import "testing"

func TestListMatcher(t *testing.T) {
	tests := []struct {
		name   string
		expr   string
		peelID string
		want   bool
	}{
		{"single match", "L@web-01", "web-01", true},
		{"single no match", "L@web-01", "web-02", false},
		{"multi match first", "L@web-01,web-02,web-03", "web-01", true},
		{"multi match last", "L@web-01,web-02,web-03", "web-03", true},
		{"multi no match", "L@web-01,web-02,web-03", "web-04", false},
		{"with spaces", "L@web-01, web-02, web-03", "web-02", true},
		{"without prefix", "web-01,web-02", "web-01", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := NewListMatcher(tt.expr)
			if err != nil {
				t.Fatalf("NewListMatcher(%q): %v", tt.expr, err)
			}
			if got := m.Match(tt.peelID, nil); got != tt.want {
				t.Errorf("ListMatcher(%q).Match(%q) = %v, want %v", tt.expr, tt.peelID, got, tt.want)
			}
		})
	}
}

func TestListMatcherInvalid(t *testing.T) {
	_, err := NewListMatcher("L@")
	if err == nil {
		t.Fatal("expected error for empty list")
	}

	_, err = NewListMatcher("L@,,,")
	if err == nil {
		t.Fatal("expected error for all-empty entries")
	}
}

func TestListMatcherType(t *testing.T) {
	m, _ := NewListMatcher("L@a,b")
	if m.Type() != List {
		t.Errorf("Type() = %v, want List", m.Type())
	}
	if m.String() != "L@a,b" {
		t.Errorf("String() = %q, want %q", m.String(), "L@a,b")
	}
}
