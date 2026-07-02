package target

import (
	"fmt"
	"strings"
)

// ListMatcher matches peels against an explicit comma-separated list.
// Expressions use the format "L@web01,web02,web03". The "L@" prefix
// is stripped if present.
type ListMatcher struct {
	peels    map[string]struct{}
	original string
}

// NewListMatcher parses a list expression into a set of peel IDs.
func NewListMatcher(expr string) (*ListMatcher, error) {
	original := expr
	expr = strings.TrimPrefix(expr, "L@")
	if expr == "" {
		return nil, fmt.Errorf("target: list expression must not be empty")
	}

	parts := strings.Split(expr, ",")
	peels := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			peels[p] = struct{}{}
		}
	}

	if len(peels) == 0 {
		return nil, fmt.Errorf("target: list expression contains no valid peel IDs: %q", original)
	}

	return &ListMatcher{peels: peels, original: original}, nil
}

func (m *ListMatcher) Match(peelID string, _ map[string]any) bool {
	_, ok := m.peels[peelID]
	return ok
}

func (m *ListMatcher) String() string   { return m.original }
func (m *ListMatcher) Type() TargetType { return List }
