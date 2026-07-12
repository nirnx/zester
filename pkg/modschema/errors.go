package modschema

import (
	"fmt"
	"strings"
)

// redactedValue is the placeholder substituted for a sensitive parameter's value
// in any diagnostic (FieldError, warnings, reports).
const redactedValue = "<redacted>"

// ErrorKind classifies a per-field decode failure.
type ErrorKind uint8

const (
	// ErrOther is the zero value: an unclassified error.
	ErrOther ErrorKind = iota
	// ErrMissingRequired means a required parameter had no value.
	ErrMissingRequired
	// ErrWrongType means the value's type cannot be coerced to the target at all.
	ErrWrongType
	// ErrValueInvalid means the value's type is acceptable but the value itself is
	// out of range, unparseable, or semantically rejected.
	ErrValueInvalid
)

// String renders the kind for diagnostics.
func (k ErrorKind) String() string {
	switch k {
	case ErrMissingRequired:
		return "missing_required"
	case ErrWrongType:
		return "wrong_type"
	case ErrValueInvalid:
		return "value_invalid"
	default:
		return "error"
	}
}

// FieldError is a typed per-field decode failure. Value is the offending config
// value, redacted to redactedValue when the field is sensitive. Type is the
// target type's display name.
type FieldError struct {
	Module string
	Param  string
	Key    string
	Kind   ErrorKind
	Type   string
	Value  any
	Err    error
}

// Error formats the field error without exposing a redacted value.
func (e *FieldError) Error() string {
	var b strings.Builder
	b.WriteString("modschema: ")
	if e.Module != "" {
		b.WriteString(e.Module)
		b.WriteByte(' ')
	}
	fmt.Fprintf(&b, "param %q", e.Param)
	if e.Key != "" && e.Key != e.Param {
		fmt.Fprintf(&b, " (key %q)", e.Key)
	}
	if e.Kind == ErrMissingRequired {
		b.WriteString(": required parameter missing")
		return b.String()
	}
	fmt.Fprintf(&b, ": %s: cannot decode value %v", e.Kind, e.Value)
	if e.Type != "" {
		fmt.Fprintf(&b, " into %s", e.Type)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	return b.String()
}

// Unwrap exposes the underlying coercion error for errors.Is/As.
func (e *FieldError) Unwrap() error { return e.Err }

// UnknownKeyError reports a config key that is neither a known parameter (name or
// alias) nor a reserved key. Suggestion is the nearest known parameter within
// edit distance 2, or "". Known is the sorted list of known parameter names.
type UnknownKeyError struct {
	Module     string
	Key        string
	Suggestion string
	Known      []string
}

// Error formats the unknown-key error, inlining the edit-distance suggestion.
func (e *UnknownKeyError) Error() string {
	var b strings.Builder
	b.WriteString("modschema: ")
	if e.Module != "" {
		b.WriteString(e.Module)
		b.WriteByte(' ')
	}
	fmt.Fprintf(&b, "unknown parameter %q", e.Key)
	if e.Suggestion != "" {
		fmt.Fprintf(&b, " (did you mean %q?)", e.Suggestion)
	}
	return b.String()
}
