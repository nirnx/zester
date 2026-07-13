package paramtypes

import (
	"fmt"
	"maps"
	"reflect"
	"sort"
)

// StringMap is the semantic value for a string→string map parameter (for example
// cmd.run's env). It accepts a map whose values are scalars (rendered via
// fmt.Sprint); a COMPOSITE value — a nested map or slice — is a hard error, never
// silently dropped or stringified into Go syntax.
//
// A StringMap has no faithful CLI key=value spelling, so its fixtures are
// CLI-skipped with a reason.
type StringMap map[string]string

// stringMapType is the sealed SemanticType descriptor for StringMap.
type stringMapType struct{}

func (stringMapType) Name() string         { return "StringMap" }
func (stringMapType) GoType() reflect.Type { return reflect.TypeFor[StringMap]() }
func (stringMapType) Doc() string {
	return "A map of string keys to string values. Scalar values are rendered to " +
		"strings; a nested map or list value is rejected. Not expressible as a CLI " +
		"key=value argument."
}
func (stringMapType) JSONSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": map[string]any{"type": []any{"string", "integer", "number", "boolean"}},
	}
}
func (stringMapType) sealed() {}

func (stringMapType) Decode(in Input) (any, error) {
	switch v := in.Raw.(type) {
	case StringMap:
		out := make(StringMap, len(v))
		maps.Copy(out, v)
		return out, nil
	case map[string]string:
		out := make(StringMap, len(v))
		maps.Copy(out, v)
		return out, nil
	case map[string]any:
		out := make(StringMap, len(v))
		// Sort keys so a composite-value error is deterministic across runs.
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s, err := scalarToString(v[k])
			if err != nil {
				return StringMap{}, fmt.Errorf("paramtypes: StringMap: key %q: %w", k, err)
			}
			out[k] = s
		}
		return out, nil
	default:
		return StringMap{}, fmt.Errorf("paramtypes: StringMap: cannot interpret %T as a string map", in.Raw)
	}
}
