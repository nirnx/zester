package paramtypes

import (
	"fmt"
	"reflect"
	"strings"
)

// This file holds the small, stdlib-only coercion helpers shared by the sealed
// semantic types. They deliberately mirror the framework's primitive-coercion
// contract (pkg/modschema/coerce.go) but cannot import it (that would invert the
// import graph), so the boolean string set and scalar rules are duplicated here.

// truthyStrings / falsyStrings are the case-insensitive boolean string set,
// matching the framework's primitive bool coercion.
var (
	truthyStrings = map[string]bool{"true": true, "yes": true, "1": true, "on": true}
	falsyStrings  = map[string]bool{"false": true, "no": true, "0": true, "off": true}
)

// parseBoolish coerces a raw value to a bool the way the framework's primitive
// bool table does: bool as-is; the truthy/falsy string set (case-insensitive,
// whitespace-trimmed); integer 0 or 1 only; floats rejected.
func parseBoolish(raw any) (bool, error) {
	switch v := raw.(type) {
	case bool:
		return v, nil
	case string:
		return parseBoolishString(v)
	}
	rv := reflect.ValueOf(raw)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i := rv.Int()
		if i == 0 {
			return false, nil
		}
		if i == 1 {
			return true, nil
		}
		return false, fmt.Errorf("integer %d is not a boolean (only 0 or 1)", i)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u := rv.Uint()
		if u == 0 {
			return false, nil
		}
		if u == 1 {
			return true, nil
		}
		return false, fmt.Errorf("integer %d is not a boolean (only 0 or 1)", u)
	case reflect.Float32, reflect.Float64:
		return false, fmt.Errorf("floats are not accepted as a boolean")
	default:
		return false, fmt.Errorf("cannot interpret %T as a boolean", raw)
	}
}

// parseBoolishString applies the boolean string set to a single string.
// BoolishStringPattern is the JSON-Schema (ECMA) regex for the EXACT set of
// strings parseBoolishString accepts: the truthy/falsy words in any casing,
// surrounded by optional whitespace (ToLower + TrimSpace semantics — ECMA
// regex in JSON Schema has no reliable inline case flag, so the classes are
// spelled out). Shared by TriState, TemplateFlag, and the framework's
// primitive bool fragment so the three schema surfaces cannot drift from the
// one decoder.
const BoolishStringPattern = `^\s*([tT][rR][uU][eE]|[fF][aA][lL][sS][eE]|[yY][eE][sS]|[nN][oO]|[oO][nN]|[oO][fF][fF]|[01])\s*$`

func parseBoolishString(s string) (bool, error) {
	low := strings.ToLower(strings.TrimSpace(s))
	if truthyStrings[low] {
		return true, nil
	}
	if falsyStrings[low] {
		return false, nil
	}
	return false, fmt.Errorf("%q is not a boolean", s)
}

// scalarToString renders a scalar value to a string via fmt.Sprint (strings pass
// through unchanged). A composite value — map, slice, array, struct, pointer, or
// nil — is rejected: it has no meaningful flat-string form, and silently dropping
// or stringifying it is the class of bug StringList/StringMap must surface.
func scalarToString(v any) (string, error) {
	if v == nil {
		return "", fmt.Errorf("nil is not a scalar value")
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return fmt.Sprint(v), nil
	default:
		return "", fmt.Errorf("%T is not a scalar value", v)
	}
}

// intFromReflect extracts an int from any signed/unsigned integer kind. It
// reports false for non-integer kinds and for unsigned values that overflow int.
func intFromReflect(raw any) (int, bool) {
	rv := reflect.ValueOf(raw)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u := rv.Uint()
		if u > uint64(^uint(0)>>1) {
			return 0, false
		}
		return int(u), true
	default:
		return 0, false
	}
}

// isAllDigits reports whether s is non-empty and consists solely of ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
