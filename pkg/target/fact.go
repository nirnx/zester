package target

import (
	"fmt"
	"strconv"
	"strings"
)

// FactMatcher matches peels based on their facts data.
// Expressions use the format "G@key:value" or "I@key:value".
// Nested keys use dot notation: "G@os.family:debian".
// Comparison operators are supported: >=, <=, >, <, !=, =.
type FactMatcher struct {
	key      string
	op       compareOp
	value    string
	original string
}

type compareOp int

const (
	opEqual compareOp = iota
	opNotEqual
	opGT
	opGTE
	opLT
	opLTE
)

// NewFactMatcher parses a fact expression like "G@os:ubuntu" or
// "G@cpu_count:>=4". The "G@" and "I@" prefixes are stripped if present.
func NewFactMatcher(expr string) (*FactMatcher, error) {
	original := expr
	expr = strings.TrimPrefix(expr, "G@")
	expr = strings.TrimPrefix(expr, "I@")

	colonIdx := strings.Index(expr, ":")
	if colonIdx < 0 {
		return nil, fmt.Errorf("target: fact expression must contain ':' separator: %q", original)
	}

	key := expr[:colonIdx]
	valExpr := expr[colonIdx+1:]

	if key == "" {
		return nil, fmt.Errorf("target: fact key must not be empty: %q", original)
	}

	op, value := parseCompareOp(valExpr)

	return &FactMatcher{
		key:      key,
		op:       op,
		value:    value,
		original: original,
	}, nil
}

func (m *FactMatcher) Match(_ string, facts map[string]any) bool {
	if facts == nil {
		return false
	}

	val := lookupNested(facts, m.key)
	if val == nil {
		return false
	}

	actual := fmt.Sprintf("%v", val)
	return m.compare(actual)
}

func (m *FactMatcher) String() string   { return m.original }
func (m *FactMatcher) Type() TargetType { return Fact }

func (m *FactMatcher) compare(actual string) bool {
	switch m.op {
	case opEqual:
		return strings.EqualFold(actual, m.value)
	case opNotEqual:
		return !strings.EqualFold(actual, m.value)
	case opGT, opGTE, opLT, opLTE:
		return numericCompare(actual, m.value, m.op)
	default:
		return actual == m.value
	}
}

func parseCompareOp(s string) (compareOp, string) {
	if strings.HasPrefix(s, ">=") {
		return opGTE, s[2:]
	}
	if strings.HasPrefix(s, "<=") {
		return opLTE, s[2:]
	}
	if strings.HasPrefix(s, "!=") {
		return opNotEqual, s[2:]
	}
	if strings.HasPrefix(s, ">") {
		return opGT, s[1:]
	}
	if strings.HasPrefix(s, "<") {
		return opLT, s[1:]
	}
	return opEqual, s
}

func numericCompare(actual, expected string, op compareOp) bool {
	a, errA := strconv.ParseFloat(actual, 64)
	e, errE := strconv.ParseFloat(expected, 64)
	if errA != nil || errE != nil {
		// Fall back to lexicographic comparison.
		cmp := strings.Compare(actual, expected)
		switch op {
		case opGT:
			return cmp > 0
		case opGTE:
			return cmp >= 0
		case opLT:
			return cmp < 0
		case opLTE:
			return cmp <= 0
		default:
			return false
		}
	}
	switch op {
	case opGT:
		return a > e
	case opGTE:
		return a >= e
	case opLT:
		return a < e
	case opLTE:
		return a <= e
	default:
		return false
	}
}

// lookupNested traverses a nested map using dot-separated keys.
// For example, "os.family" looks up map["os"].(map[string]any)["family"].
func lookupNested(m map[string]any, key string) any {
	parts := strings.Split(key, ".")
	var current any = m
	for _, part := range parts {
		cm, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = cm[part]
		if !ok {
			return nil
		}
	}
	return current
}
