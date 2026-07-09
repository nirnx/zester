package target

import (
	"fmt"
	"path/filepath"

	"github.com/nirnx/zester/pkg/enroll"
)

// GlobMatcher matches peel IDs using filepath.Match glob semantics.
// Supports *, ?, and [] character classes.
type GlobMatcher struct {
	pattern string
	// dotNorm is the pattern with '.' substituted exactly as peel IDs sanitize
	// it (enroll.SanitizeGlobDots), so a target written with the original
	// hostname dots (web01.pl) still matches the sanitized id (web01_pl).
	// Because a dot can never appear in a real peel id, this only ever ADDS
	// matches, never removes.
	dotNorm string
}

// NewGlobMatcher creates a GlobMatcher for the given glob pattern.
// The pattern is validated at creation time.
func NewGlobMatcher(pattern string) (*GlobMatcher, error) {
	if pattern == "" {
		return nil, fmt.Errorf("target: glob pattern must not be empty")
	}
	// Validate the pattern syntax.
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, fmt.Errorf("target: invalid glob pattern %q: %w", pattern, err)
	}
	return &GlobMatcher{pattern: pattern, dotNorm: enroll.SanitizeGlobDots(pattern)}, nil
}

func (m *GlobMatcher) Match(peelID string, _ map[string]any) bool {
	if ok, _ := filepath.Match(m.pattern, peelID); ok {
		return true
	}
	if m.dotNorm != m.pattern {
		if ok, _ := filepath.Match(m.dotNorm, peelID); ok {
			return true
		}
	}
	return false
}

func (m *GlobMatcher) String() string   { return m.pattern }
func (m *GlobMatcher) Type() TargetType { return Glob }
