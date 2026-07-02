package target

import (
	"fmt"
	"path/filepath"
)

// GlobMatcher matches peel IDs using filepath.Match glob semantics.
// Supports *, ?, and [] character classes.
type GlobMatcher struct {
	pattern string
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
	return &GlobMatcher{pattern: pattern}, nil
}

func (m *GlobMatcher) Match(peelID string, _ map[string]any) bool {
	ok, _ := filepath.Match(m.pattern, peelID)
	return ok
}

func (m *GlobMatcher) String() string   { return m.pattern }
func (m *GlobMatcher) Type() TargetType { return Glob }
