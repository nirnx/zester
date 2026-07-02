package target

import (
	"fmt"
	"testing"
)

func TestFactMatcher(t *testing.T) {
	facts := map[string]any{
		"os":        "ubuntu",
		"arch":      "amd64",
		"cpu_count": 8,
		"nested": map[string]any{
			"family": "debian",
			"version": map[string]any{
				"major": 22,
				"minor": 4,
			},
		},
	}

	tests := []struct {
		name  string
		expr  string
		facts map[string]any
		want  bool
	}{
		{"simple equal", "G@os:ubuntu", facts, true},
		{"simple no match", "G@os:centos", facts, false},
		{"case insensitive", "G@os:Ubuntu", facts, true},
		{"nested key", "G@nested.family:debian", facts, true},
		{"nested no match", "G@nested.family:redhat", facts, false},
		{"deep nested", "G@nested.version.major:22", facts, true},
		{"numeric gte", "G@cpu_count:>=4", facts, true},
		{"numeric gte exact", "G@cpu_count:>=8", facts, true},
		{"numeric gte no match", "G@cpu_count:>=16", facts, false},
		{"numeric gt", "G@cpu_count:>4", facts, true},
		{"numeric gt no match", "G@cpu_count:>8", facts, false},
		{"numeric lte", "G@cpu_count:<=8", facts, true},
		{"numeric lt", "G@cpu_count:<16", facts, true},
		{"numeric lt no match", "G@cpu_count:<8", facts, false},
		{"not equal", "G@os:!=centos", facts, true},
		{"not equal match", "G@os:!=ubuntu", facts, false},
		{"missing key", "G@missing:value", facts, false},
		{"nil facts", "G@os:ubuntu", nil, false},
		{"settings prefix", "I@os:ubuntu", facts, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := NewFactMatcher(tt.expr)
			if err != nil {
				t.Fatalf("NewFactMatcher(%q): %v", tt.expr, err)
			}
			if got := m.Match("any-peel", tt.facts); got != tt.want {
				t.Errorf("FactMatcher(%q).Match() = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestFactMatcherInvalid(t *testing.T) {
	_, err := NewFactMatcher("G@nocolon")
	if err == nil {
		t.Fatal("expected error for missing colon")
	}

	_, err = NewFactMatcher("G@:value")
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestFactMatcherType(t *testing.T) {
	m, _ := NewFactMatcher("G@os:linux")
	if m.Type() != Fact {
		t.Errorf("Type() = %v, want Fact", m.Type())
	}
}

func TestLookupNested(t *testing.T) {
	m := map[string]any{
		"a": map[string]any{
			"b": map[string]any{
				"c": "deep",
			},
		},
		"flat": "value",
	}

	tests := []struct {
		key  string
		want any
	}{
		{"flat", "value"},
		{"a.b.c", "deep"},
		{"a.b", map[string]any{"c": "deep"}},
		{"missing", nil},
		{"a.missing", nil},
		{"a.b.c.d", nil},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := lookupNested(m, tt.key)
			if got == nil && tt.want == nil {
				return
			}
			if got == nil || tt.want == nil {
				t.Errorf("lookupNested(%q) = %v, want %v", tt.key, got, tt.want)
				return
			}
			// For map comparisons just check string representation
			if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", tt.want) {
				t.Errorf("lookupNested(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}
