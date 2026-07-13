package paramtypes

import (
	"fmt"
	"reflect"
)

// TriState is the semantic value for a three-valued boolean parameter: unset,
// true, or false. It replaces the ad-hoc "Enable bool + hasEnable bool" pair
// (e.g. service.running's enable) with a single field whose Declared() bit tells
// the module whether the operator expressed an intent at all — so a module can
// distinguish "leave the unit's enablement alone" from "explicitly disable it".
//
// It accepts the same representations as a primitive bool: a real bool, the
// truthy/falsy string set (true/yes/1/on, false/no/0/off, case-insensitive), and
// integer 0 or 1. Accepting the string forms is BD-2 (a CLI `enable=true` arrives
// as the string "true").
//
// Integer coercion is BD-7 and is deliberately narrow: the integer 1 is
// declared-true and 0 is declared-false (across every signed/unsigned int kind —
// msgpack yields sized kinds such as int8/uint16 for small magnitudes), and ANY
// other integer (for example 2) is a typed value error, never silently ignored.
type TriState struct {
	declared bool
	value    bool
}

// NewTriState returns a declared TriState. The zero TriState is undeclared.
func NewTriState(v bool) TriState { return TriState{declared: true, value: v} }

// Declared reports whether a value was supplied.
func (t TriState) Declared() bool { return t.declared }

// Value returns the supplied boolean; it is meaningful only when Declared.
func (t TriState) Value() bool { return t.value }

// ValueOr returns the supplied boolean, or def when no value was supplied.
func (t TriState) ValueOr(def bool) bool {
	if t.declared {
		return t.value
	}
	return def
}

// triStateType is the sealed SemanticType descriptor for TriState.
type triStateType struct{}

func (triStateType) Name() string         { return "TriState" }
func (triStateType) GoType() reflect.Type { return reflect.TypeFor[TriState]() }
func (triStateType) Doc() string {
	return "A three-valued boolean: unset, true, or false. Accepts a bool; the " +
		"truthy/falsy string set (true/yes/1/on, false/no/0/off, case-insensitive); " +
		"or an integer, where 1 is declared-true and 0 is declared-false — any other " +
		"integer (for example 2) is a typed error, never silently ignored. The unset " +
		"state lets a module tell \"not specified\" apart from an explicit false."
}
func (triStateType) JSONSchema() map[string]any {
	// anyOf (representation union); the string arm is the SHARED boolish
	// pattern: Decode lowercases and trims, so " TRUE " is valid and the
	// schema must agree (review finding).
	return map[string]any{
		"anyOf": []any{
			map[string]any{"type": "boolean"},
			map[string]any{"type": "string", "pattern": BoolishStringPattern},
			// BD-7: only 0 and 1 are meaningful integers — 0 is declared-false,
			// 1 is declared-true, any other integer is rejected.
			map[string]any{"type": "integer", "enum": []any{0, 1}},
		},
	}
}
func (triStateType) sealed() {}

func (triStateType) Decode(in Input) (any, error) {
	b, err := parseBoolish(in.Raw)
	if err != nil {
		return TriState{}, fmt.Errorf("paramtypes: TriState: %w", err)
	}
	return TriState{declared: true, value: b}, nil
}
