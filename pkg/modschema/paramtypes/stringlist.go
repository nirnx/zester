package paramtypes

import (
	"fmt"
	"reflect"
)

// StringList is the semantic value for a list-of-strings parameter (for example
// cmd.run's args). It accepts:
//
//   - a bare string, becoming a single-element list;
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
func (stringListType) GoType() reflect.Type { return reflect.TypeOf(StringList(nil)) }
func (stringListType) Doc() string {
	return "A list of strings. Accepts a single string (a one-element list), a " +
		"list of strings, or a mixed scalar list whose elements are rendered to " +
		"strings. A nested list or map element is rejected rather than dropped."
}
func (stringListType) JSONSchema() map[string]any {
	return map[string]any{
		"oneOf": []any{
			map[string]any{"type": "string"},
			map[string]any{
				"type":  "array",
				"items": map[string]any{"type": []any{"string", "integer", "number", "boolean"}},
			},
		},
	}
}
func (stringListType) sealed() {}

func (stringListType) Decode(in Input) (any, error) {
	switch v := in.Raw.(type) {
	case string:
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
