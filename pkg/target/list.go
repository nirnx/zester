package target

import (
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/enroll"
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
		if p == "" {
			continue
		}
		peels[p] = struct{}{}
		// A list entry written with the original hostname dots (web01.pl) also
		// matches the sanitized peel ID (web01_pl) — same normalization as the
		// glob matcher. A real ID can never contain '.', so the extra entry
		// only ever ADDS matches. (Entries are exact IDs, so the sanitizer's
		// character-class handling never applies here.)
		if norm := enroll.SanitizeGlobDots(p); norm != p {
			peels[norm] = struct{}{}
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
