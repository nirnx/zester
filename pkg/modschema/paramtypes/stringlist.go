package paramtypes

import (
	"fmt"
	"reflect"
)

// StringList is the semantic value for a list-of-strings parameter (for example
// cmd.run's args). It accepts:
//
//   - a non-empty bare string, becoming a single-element list (BD-5 third arm —
//     the legacy parsers ignored a scalar entirely, so `groups: docker` managed
//     no groups; it now enables management with one element);
//   - an EMPTY string, which is undeclared (the nil zero value), not [""] (§3);
//   - a []string, copied as-is; and
//   - a []any, with scalar elements rendered via fmt.Sprint.
//
// A NESTED element — a map or slice inside a []any — is a hard error (BD-5),
// where the legacy list parsers silently dropped it. Distinct from the primitive
// []any passthrough: StringList owns []string and flattens to strings.
type StringList []string

// stringListType is the sealed SemanticType descriptor for StringList.
type stringListType struct{}

func (stringListType) Name() string         { return "StringList" }
func (stringListType) GoType() reflect.Type { return reflect.TypeFor[StringList]() }
func (stringListType) Doc() string {
	return "A list of strings. Accepts a single string (a one-element list), a " +
		"list of strings, or a mixed scalar list whose elements are rendered to " +
		"strings. A nested list or map element is rejected rather than dropped."
}
func (stringListType) JSONSchema() map[string]any {
	// Elements mirror Decode: scalar list elements (numbers, booleans) are
	// rendered via fmt.Sprint (BD-5), so [80, 443] is runtime-valid and the
	// schema must agree; nested composites stay invalid (review-round-3 class).
	scalar := map[string]any{"anyOf": []any{
		map[string]any{"type": "string"},
		map[string]any{"type": "number"},
		map[string]any{"type": "boolean"},
	}}
	return map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "array", "items": scalar},
		},
	}
}
func (stringListType) sealed() {}

func (stringListType) Decode(in Input) (any, error) {
	switch v := in.Raw.(type) {
	case string:
		if v == "" {
			// EMPTY-STRING RULE (keystone spec §3): an empty string is undeclared
			// (the nil zero value), NOT a one-element [""] list — legacy comma-ok
			// parity. The framework intercepts "" before dispatch; this guard keeps
			// the type self-consistent for any direct caller.
			return StringList(nil), nil
		}
		// A non-empty bare string is a single-element list (BD-5 third arm): the
		// legacy list parsers ignored a scalar value entirely, so `groups: docker`
		// silently managed no groups; it now enables group management with one
		// element (this is also what makes a CLI `groups=docker` work).
		return StringList{v}, nil
	case StringList:
		return append(StringList(nil), v...), nil
	case []string:
		out := make(StringList, len(v))
		copy(out, v)
		return out, nil
	case []any:
		out := make(StringList, 0, len(v))
		for i, e := range v {
			s, err := scalarToString(e)
			if err != nil {
				return StringList{}, fmt.Errorf("paramtypes: StringList: element %d: %w", i, err)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return StringList{}, fmt.Errorf("paramtypes: StringList: cannot interpret %T as a string list", in.Raw)
	}
}
