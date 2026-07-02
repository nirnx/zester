package facts

import (
	"testing"
)

func TestFactsGet(t *testing.T) {
	f := Facts{
		"os": map[string]any{
			"name": "linux",
			"arch": "amd64",
		},
		"network": map[string]any{
			"hostname": "web-01",
			"ipv4":     []string{"10.0.0.1"},
		},
	}

	tests := []struct {
		key    string
		want   any
		exists bool
	}{
		{"os.name", "linux", true},
		{"os.arch", "amd64", true},
		{"network.hostname", "web-01", true},
		{"missing", nil, false},
		{"os.missing", nil, false},
		{"", nil, false},
		{"os.name.deep", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, ok := f.Get(tt.key)
			if ok != tt.exists {
				t.Errorf("Get(%q) exists: got %v, want %v", tt.key, ok, tt.exists)
			}
			if tt.exists && got != tt.want {
				t.Errorf("Get(%q): got %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestSplitDotPath(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"os.name", []string{"os", "name"}},
		{"network.hostname", []string{"network", "hostname"}},
		{"single", []string{"single"}},
		{"a.b.c.d", []string{"a", "b", "c", "d"}},
		{"", nil},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := splitDotPath(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("splitDotPath(%q): got %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitDotPath(%q)[%d]: got %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestFlattenFacts(t *testing.T) {
	input := map[string]any{
		"os": map[string]any{
			"name": "linux",
			"arch": "amd64",
		},
		"cpu": map[string]any{
			"count": 8,
		},
	}

	result := flattenFacts(input, "")

	expected := map[string]string{
		"os.name":   "linux",
		"os.arch":   "amd64",
		"cpu.count": "8",
	}

	for k, v := range expected {
		if result[k] != v {
			t.Errorf("flattenFacts[%q]: got %q, want %q", k, result[k], v)
		}
	}
}

func TestToString(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want string
	}{
		{"string", "hello", "hello"},
		{"int", 42, "42"},
		{"int64", int64(100), "100"},
		{"uint64", uint64(200), "200"},
		{"float_int", float64(3), "3"},
		{"bool_true", true, "true"},
		{"bool_false", false, "false"},
		{"zero", 0, "0"},
		{"negative", -5, "-5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toString(tt.val)
			if got != tt.want {
				t.Errorf("toString(%v): got %q, want %q", tt.val, got, tt.want)
			}
		})
	}
}
