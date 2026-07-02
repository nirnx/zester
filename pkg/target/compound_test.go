package target

import "testing"

func TestCompoundMatcher(t *testing.T) {
	facts := map[string]any{
		"os":  "ubuntu",
		"env": "production",
	}

	tests := []struct {
		name   string
		expr   string
		peelID string
		facts  map[string]any
		want   bool
	}{
		{
			"simple and",
			"web* and G@os:ubuntu",
			"web-01", facts, true,
		},
		{
			"and fails left",
			"db* and G@os:ubuntu",
			"web-01", facts, false,
		},
		{
			"and fails right",
			"web* and G@os:centos",
			"web-01", facts, false,
		},
		{
			"simple or",
			"web* or db*",
			"db-01", nil, true,
		},
		{
			"or both fail",
			"web* or api*",
			"db-01", nil, false,
		},
		{
			"not",
			"not db*",
			"web-01", nil, true,
		},
		{
			"not match",
			"not web*",
			"web-01", nil, false,
		},
		{
			"compound and not",
			"web* and not E@.*-dev-.*",
			"web-prod-01", nil, true,
		},
		{
			"compound and not match",
			"web* and not E@.*-dev-.*",
			"web-dev-01", nil, false,
		},
		{
			"triple and",
			"web* and G@os:ubuntu and G@env:production",
			"web-01", facts, true,
		},
		{
			"or with facts",
			"G@os:ubuntu or G@os:centos",
			"any", facts, true,
		},
		{
			"parentheses",
			"(web* or api*) and G@os:ubuntu",
			"api-01", facts, true,
		},
		{
			"parentheses no match",
			"(web* or api*) and G@os:centos",
			"api-01", facts, false,
		},
		{
			"pcre in compound",
			"E@^web-\\d+ and G@os:ubuntu",
			"web-01", facts, true,
		},
		{
			"list in compound",
			"L@web-01,web-02 and G@env:production",
			"web-01", facts, true,
		},
		{
			"list in compound no match",
			"L@web-01,web-02 and G@env:production",
			"web-03", facts, false,
		},
		{
			"complex nested",
			"(web* and G@os:ubuntu) or (db* and G@env:production)",
			"db-01", facts, true,
		},
		{
			"not with parens",
			"not (web* and G@os:centos)",
			"web-01", facts, true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := ParseCompound(tt.expr)
			if err != nil {
				t.Fatalf("ParseCompound(%q): %v", tt.expr, err)
			}
			if got := m.Match(tt.peelID, tt.facts); got != tt.want {
				t.Errorf("CompoundMatcher(%q).Match(%q) = %v, want %v", tt.expr, tt.peelID, got, tt.want)
			}
		})
	}
}

func TestCompoundMatcherType(t *testing.T) {
	m, err := ParseCompound("web* and G@os:linux")
	if err != nil {
		t.Fatal(err)
	}
	if m.Type() != Compound {
		t.Errorf("Type() = %v, want Compound", m.Type())
	}
}

func TestCompoundParseErrors(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"empty", ""},
		{"trailing and", "web* and"},
		{"trailing or", "web* or"},
		{"unclosed paren", "(web*"},
		{"empty paren", "()"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCompound(tt.expr)
			if err == nil {
				t.Errorf("ParseCompound(%q) expected error, got nil", tt.expr)
			}
		})
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"web*", []string{"web*"}},
		{"web* and G@os:ubuntu", []string{"web*", "and", "G@os:ubuntu"}},
		{"(web* or db*) and not E@.*-dev-.*", []string{"(", "web*", "or", "db*", ")", "and", "not", "E@.*-dev-.*"}},
		{"  web*   and   db*  ", []string{"web*", "and", "db*"}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := tokenize(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("tokenize(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tokenize(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}
