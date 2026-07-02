package template

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nikolalohinski/gonja/v2/exec"
)

// registerFilters adds Zester-specific filters to the filter set.
func registerFilters(filters *exec.FilterSet) {
	filters.Register("settings_decrypt", settingsDecryptFilter)
	filters.Register("yaml_encode", yamlEncodeFilter)
	filters.Register("to_json", toJSONFilter)
	filters.Register("json", toJSONFilter) // Salt uses |json, Zester has |to_json
	filters.Register("regex_match", regexMatchFilter)
	filters.Register("dict_merge", dictMergeFilter)

	// Override gonja's built-in length filter to recognize types with a
	// Len() int method (e.g. *MutableList). Without this, gonja's Value.Len()
	// falls through to the default case for pointer-to-struct types and returns 0.
	filters.Replace("length", zesterLengthFilter) //nolint:errcheck
}

// lenner is satisfied by any type that has a Len() int method (e.g. *MutableList).
type lenner interface {
	Len() int
}

// zesterLengthFilter extends gonja's built-in length filter to recognize
// types with a Len() int method. This allows {{ mlist_var | length }} to
// work correctly for *MutableList and similar wrapper types.
func zesterLengthFilter(e *exec.Evaluator, in *exec.Value, params *exec.VarArgs) *exec.Value {
	if in.IsError() {
		return in
	}
	if obj, ok := in.Interface().(lenner); ok {
		return exec.AsValue(obj.Len())
	}
	// Fall back to gonja's default behavior for slices, maps, strings.
	return exec.AsValue(in.Len())
}

// settingsDecryptFilter is a placeholder filter for template-side decryption.
// In practice, decryption happens on the peel side after settings are received.
// This filter passes through the value unchanged during master-side rendering.
func settingsDecryptFilter(e *exec.Evaluator, in *exec.Value, params *exec.VarArgs) *exec.Value {
	return in
}

// yamlEncodeFilter converts a value to a YAML-compatible string representation.
// For simple values it returns the string form; for complex types it returns
// a JSON representation (which is valid YAML).
func yamlEncodeFilter(e *exec.Evaluator, in *exec.Value, params *exec.VarArgs) *exec.Value {
	if in.IsNil() {
		return exec.AsValue("null")
	}
	val := in.Interface()
	switch v := val.(type) {
	case string:
		return exec.AsValue(v)
	case bool:
		if v {
			return exec.AsValue("true")
		}
		return exec.AsValue("false")
	case int, int64, float64:
		return exec.AsValue(fmt.Sprintf("%v", v))
	default:
		data, err := json.Marshal(val)
		if err != nil {
			return exec.AsValue(fmt.Sprintf("%v", val))
		}
		return exec.AsValue(string(data))
	}
}

// toJSONFilter serializes a value to a JSON string.
func toJSONFilter(e *exec.Evaluator, in *exec.Value, params *exec.VarArgs) *exec.Value {
	if in.IsNil() {
		return exec.AsValue("null")
	}
	data, err := json.Marshal(in.Interface())
	if err != nil {
		return exec.AsValue("")
	}
	return exec.AsValue(string(data))
}

// regexMatchFilter checks if the input string matches a regex pattern.
// Usage: {{ value | regex_match("pattern") }}
func regexMatchFilter(e *exec.Evaluator, in *exec.Value, params *exec.VarArgs) *exec.Value {
	if len(params.Args) < 1 {
		return exec.AsValue(false)
	}
	pattern := params.Args[0].String()
	input := in.String()

	matched := strings.Contains(input, pattern)
	return exec.AsValue(matched)
}

// dictMergeFilter merges a dict into the input dict.
// Usage: {{ base_dict | dict_merge(override_dict) }}
func dictMergeFilter(e *exec.Evaluator, in *exec.Value, params *exec.VarArgs) *exec.Value {
	if in.IsNil() || len(params.Args) < 1 {
		return in
	}

	base, ok := in.Interface().(map[string]any)
	if !ok {
		return in
	}

	override, ok := params.Args[0].Interface().(map[string]any)
	if !ok {
		return in
	}

	result := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range override {
		result[k] = v
	}
	return exec.AsValue(result)
}
