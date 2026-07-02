package facts

import (
	"testing"
)

func TestIndexUpdateAndMatch(t *testing.T) {
	idx := NewIndex()

	idx.Update("web-01", Facts{
		"os": map[string]any{
			"name": "linux",
			"arch": "amd64",
		},
		"network": map[string]any{
			"hostname": "web-01",
		},
	})

	idx.Update("web-02", Facts{
		"os": map[string]any{
			"name": "linux",
			"arch": "arm64",
		},
		"network": map[string]any{
			"hostname": "web-02",
		},
	})

	idx.Update("db-01", Facts{
		"os": map[string]any{
			"name": "freebsd",
			"arch": "amd64",
		},
		"network": map[string]any{
			"hostname": "db-01",
		},
	})

	tests := []struct {
		name    string
		key     string
		pattern string
		want    []string
	}{
		{
			"exact_os_linux",
			"os.name", "linux",
			[]string{"web-01", "web-02"},
		},
		{
			"exact_os_freebsd",
			"os.name", "freebsd",
			[]string{"db-01"},
		},
		{
			"glob_hostname_web",
			"network.hostname", "web-*",
			[]string{"web-01", "web-02"},
		},
		{
			"glob_hostname_all",
			"network.hostname", "*",
			[]string{"db-01", "web-01", "web-02"},
		},
		{
			"exact_arch_amd64",
			"os.arch", "amd64",
			[]string{"db-01", "web-01"},
		},
		{
			"no_match",
			"os.name", "windows",
			[]string{},
		},
		{
			"glob_question_mark",
			"network.hostname", "web-0?",
			[]string{"web-01", "web-02"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := idx.Match(tt.key, tt.pattern)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("Match(%q, %q): got %v, want %v", tt.key, tt.pattern, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("Match(%q, %q)[%d]: got %q, want %q", tt.key, tt.pattern, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIndexRemove(t *testing.T) {
	idx := NewIndex()

	idx.Update("web-01", Facts{
		"os": map[string]any{"name": "linux"},
	})
	idx.Update("web-02", Facts{
		"os": map[string]any{"name": "linux"},
	})

	got := idx.Match("os.name", "linux")
	if len(got) != 2 {
		t.Fatalf("before remove: got %d peels, want 2", len(got))
	}

	idx.Remove("web-01")

	got = idx.Match("os.name", "linux")
	if len(got) != 1 || got[0] != "web-02" {
		t.Fatalf("after remove: got %v, want [web-02]", got)
	}
}

func TestIndexUpdateReplacesOldFacts(t *testing.T) {
	idx := NewIndex()

	idx.Update("web-01", Facts{
		"os": map[string]any{"name": "linux"},
	})

	got := idx.Match("os.name", "linux")
	if len(got) != 1 {
		t.Fatalf("initial: got %v, want [web-01]", got)
	}

	// Update with different OS.
	idx.Update("web-01", Facts{
		"os": map[string]any{"name": "freebsd"},
	})

	got = idx.Match("os.name", "linux")
	if len(got) != 0 {
		t.Fatalf("after update: linux should match 0, got %v", got)
	}

	got = idx.Match("os.name", "freebsd")
	if len(got) != 1 || got[0] != "web-01" {
		t.Fatalf("after update: freebsd should match web-01, got %v", got)
	}
}

func TestIndexPeelIDs(t *testing.T) {
	idx := NewIndex()

	idx.Update("web-02", Facts{"os": map[string]any{"name": "linux"}})
	idx.Update("web-01", Facts{"os": map[string]any{"name": "linux"}})
	idx.Update("db-01", Facts{"os": map[string]any{"name": "linux"}})

	ids := idx.PeelIDs()
	want := []string{"db-01", "web-01", "web-02"}
	if len(ids) != len(want) {
		t.Fatalf("PeelIDs: got %v, want %v", ids, want)
	}
	for i, id := range ids {
		if id != want[i] {
			t.Errorf("PeelIDs[%d]: got %q, want %q", i, id, want[i])
		}
	}
}

func TestIndexGetPeelFacts(t *testing.T) {
	idx := NewIndex()

	idx.Update("web-01", Facts{
		"os": map[string]any{
			"name": "linux",
			"arch": "amd64",
		},
	})

	facts := idx.GetPeelFacts("web-01")
	if facts == nil {
		t.Fatal("GetPeelFacts returned nil")
	}
	if facts["os.name"] != "linux" {
		t.Errorf("os.name: got %q, want %q", facts["os.name"], "linux")
	}
	if facts["os.arch"] != "amd64" {
		t.Errorf("os.arch: got %q, want %q", facts["os.arch"], "amd64")
	}

	// Nonexistent peel.
	if idx.GetPeelFacts("missing") != nil {
		t.Error("expected nil for missing peel")
	}
}

func TestIndexMatchKeyGlob(t *testing.T) {
	idx := NewIndex()

	idx.Update("web-01", Facts{
		"network": map[string]any{
			"hostname": "web-01",
			"fqdn":     "web-01.example.com",
		},
	})
	idx.Update("db-01", Facts{
		"network": map[string]any{
			"hostname": "db-01",
			"fqdn":     "db-01.example.com",
		},
	})

	// Match any network.* key with web-* value.
	got := idx.MatchKeyGlob("network.*", "web-*")
	if len(got) != 1 || got[0] != "web-01" {
		t.Fatalf("MatchKeyGlob: got %v, want [web-01]", got)
	}

	// Match all with any value.
	got = idx.MatchKeyGlob("network.hostname", "*")
	if len(got) != 2 {
		t.Fatalf("MatchKeyGlob all: got %v, want 2 peels", got)
	}
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern string
		str     string
		want    bool
	}{
		{"*", "anything", true},
		{"*", "", true},
		{"web-*", "web-01", true},
		{"web-*", "db-01", false},
		{"*-01", "web-01", true},
		{"*-01", "web-02", false},
		{"web-0?", "web-01", true},
		{"web-0?", "web-10", false},
		{"linux", "linux", true},
		{"linux", "freebsd", false},
		{"*.example.com", "web-01.example.com", true},
		{"*.example.com", "web-01.other.com", false},
		{"web-*-prod", "web-01-prod", true},
		{"web-*-prod", "web-01-staging", false},
		{"?", "a", true},
		{"?", "ab", false},
		{"", "", true},
		{"", "a", false},
		{"**", "anything", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.str, func(t *testing.T) {
			got := globMatch(tt.pattern, tt.str)
			if got != tt.want {
				t.Errorf("globMatch(%q, %q): got %v, want %v", tt.pattern, tt.str, got, tt.want)
			}
		})
	}
}
