package target

import (
	"fmt"
	"regexp"
	"strings"
)

// PCREMatcher matches peel IDs using Go's regexp package (RE2 syntax).
// Note: Go uses RE2, not PCRE. Most PCRE patterns work, but backreferences
// and lookahead/lookbehind assertions are not supported. The "E@" prefix
// is stripped if present.
type PCREMatcher struct {
	re      *regexp.Regexp
	pattern string
}

// NewPCREMatcher compiles a regex pattern and returns a PCREMatcher.
// The "E@" prefix is stripped if present.
func NewPCREMatcher(pattern string) (*PCREMatcher, error) {
	raw := strings.TrimPrefix(pattern, "E@")
	if raw == "" {
		return nil, fmt.Errorf("target: pcre pattern must not be empty")
	}
	re, err := regexp.Compile(raw)
	if err != nil {
		return nil, fmt.Errorf("target: invalid regex pattern %q: %w", raw, err)
	}
	return &PCREMatcher{re: re, pattern: pattern}, nil
}

func (m *PCREMatcher) Match(peelID string, _ map[string]any) bool {
	return m.re.MatchString(peelID)
}

func (m *PCREMatcher) String() string   { return m.pattern }
func (m *PCREMatcher) Type() TargetType { return PCRE }
