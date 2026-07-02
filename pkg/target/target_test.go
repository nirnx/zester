package target

import (
	"context"
	"testing"
)

type mockLister struct {
	peels map[string]map[string]any
}

func (m *mockLister) ListPeels(_ context.Context) ([]string, error) {
	ids := make([]string, 0, len(m.peels))
	for id := range m.peels {
		ids = append(ids, id)
	}
	return ids, nil
}

func (m *mockLister) GetFacts(_ context.Context, peelID string) (map[string]any, error) {
	f, ok := m.peels[peelID]
	if !ok {
		return nil, nil
	}
	return f, nil
}

func TestResolve(t *testing.T) {
	lister := &mockLister{
		peels: map[string]map[string]any{
			"web-01": {"os": "ubuntu", "env": "prod"},
			"web-02": {"os": "ubuntu", "env": "prod"},
			"web-03": {"os": "centos", "env": "staging"},
			"db-01":  {"os": "ubuntu", "env": "prod"},
			"db-02":  {"os": "centos", "env": "staging"},
		},
	}

	tests := []struct {
		name     string
		expr     string
		tt       TargetType
		wantMin  int
		wantMax  int
		contains string
	}{
		{"glob all", "*", Glob, 5, 5, ""},
		{"glob web", "web*", Glob, 3, 3, "web-01"},
		{"pcre db", "E@^db-", PCRE, 2, 2, "db-01"},
		{"fact os ubuntu", "G@os:ubuntu", Fact, 3, 3, ""},
		{"list specific", "L@web-01,db-01", List, 2, 2, "web-01"},
		{"compound", "web* and G@os:ubuntu", Compound, 2, 2, ""},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Resolve(ctx, tt.expr, tt.tt, lister)
			if err != nil {
				t.Fatalf("Resolve(%q, %v): %v", tt.expr, tt.tt, err)
			}
			if len(result) < tt.wantMin || len(result) > tt.wantMax {
				t.Errorf("Resolve(%q) returned %d results, want [%d, %d]: %v", tt.expr, len(result), tt.wantMin, tt.wantMax, result)
			}
			if tt.contains != "" {
				found := false
				for _, r := range result {
					if r == tt.contains {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Resolve(%q) result should contain %q: %v", tt.expr, tt.contains, result)
				}
			}
		})
	}
}

func TestDetectType(t *testing.T) {
	tests := []struct {
		expr string
		want TargetType
	}{
		{"web*", Glob},
		{"*", Glob},
		{"web-01", Glob},
		{"E@web-\\d+", PCRE},
		{"G@os:ubuntu", Fact},
		{"I@role:web", Settings},
		{"L@web-01,web-02", List},
		{"web* and G@os:ubuntu", Compound},
		{"web* or db*", Compound},
		{"not web*", Compound},
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			if got := DetectType(tt.expr); got != tt.want {
				t.Errorf("DetectType(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

// mockBulkLister implements both PeelLister and BulkPeelLister.
type mockBulkLister struct {
	mockLister
	bulkCalled bool
}

func (m *mockBulkLister) ListPeelsWithFacts(_ context.Context) (map[string]map[string]any, error) {
	m.bulkCalled = true
	return m.peels, nil
}

func TestResolveBulk(t *testing.T) {
	peels := map[string]map[string]any{
		"web-01": {"os": "ubuntu", "env": "prod"},
		"web-02": {"os": "ubuntu", "env": "prod"},
		"web-03": {"os": "centos", "env": "staging"},
		"db-01":  {"os": "ubuntu", "env": "prod"},
		"db-02":  {"os": "centos", "env": "staging"},
	}

	ctx := context.Background()

	t.Run("bulk path used for fact targeting", func(t *testing.T) {
		lister := &mockBulkLister{mockLister: mockLister{peels: peels}}
		result, err := Resolve(ctx, "G@os:ubuntu", Fact, lister)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if !lister.bulkCalled {
			t.Error("expected bulk path to be used for Fact targeting")
		}
		if len(result) != 3 {
			t.Errorf("expected 3 ubuntu peels, got %d: %v", len(result), result)
		}
	})

	t.Run("bulk path used for compound targeting", func(t *testing.T) {
		lister := &mockBulkLister{mockLister: mockLister{peels: peels}}
		result, err := Resolve(ctx, "web* and G@os:ubuntu", Compound, lister)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if !lister.bulkCalled {
			t.Error("expected bulk path to be used for Compound targeting")
		}
		if len(result) != 2 {
			t.Errorf("expected 2 ubuntu web peels, got %d: %v", len(result), result)
		}
	})

	t.Run("bulk path NOT used for glob targeting", func(t *testing.T) {
		lister := &mockBulkLister{mockLister: mockLister{peels: peels}}
		result, err := Resolve(ctx, "web*", Glob, lister)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if lister.bulkCalled {
			t.Error("bulk path should not be used for Glob targeting")
		}
		if len(result) != 3 {
			t.Errorf("expected 3 web peels, got %d: %v", len(result), result)
		}
	})

	t.Run("results match per-peel path", func(t *testing.T) {
		// Verify bulk and per-peel paths produce identical results.
		bulkLister := &mockBulkLister{mockLister: mockLister{peels: peels}}
		perPeelLister := &mockLister{peels: peels}

		bulkResult, err := Resolve(ctx, "G@os:centos", Fact, bulkLister)
		if err != nil {
			t.Fatalf("bulk Resolve: %v", err)
		}
		perPeelResult, err := Resolve(ctx, "G@os:centos", Fact, perPeelLister)
		if err != nil {
			t.Fatalf("per-peel Resolve: %v", err)
		}

		if len(bulkResult) != len(perPeelResult) {
			t.Fatalf("bulk returned %d, per-peel returned %d", len(bulkResult), len(perPeelResult))
		}

		bulkSet := make(map[string]bool)
		for _, id := range bulkResult {
			bulkSet[id] = true
		}
		for _, id := range perPeelResult {
			if !bulkSet[id] {
				t.Errorf("per-peel result %q not found in bulk result", id)
			}
		}
	})
}

func TestTargetTypeString(t *testing.T) {
	tests := []struct {
		tt   TargetType
		want string
	}{
		{Glob, "glob"},
		{PCRE, "pcre"},
		{Fact, "fact"},
		{Settings, "settings"},
		{Compound, "compound"},
		{List, "list"},
		{TargetType(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.tt.String(); got != tt.want {
				t.Errorf("TargetType(%d).String() = %q, want %q", tt.tt, got, tt.want)
			}
		})
	}
}
