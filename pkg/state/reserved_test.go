package state

import (
	"reflect"
	"sort"
	"testing"
)

// TestReservedKeys_Membership pins the reserved-key union to the exact
// composition of the three source slices: it must be the deduplicated, sorted
// union of attributeKeys ∪ requisiteKeys ∪ compilerKeys, and it must EXCLUDE
// "name" (a legitimate module parameter). ReservedKeySet must agree with
// ReservedKeys.
func TestReservedKeys_Membership(t *testing.T) {
	want := map[string]struct{}{}
	for _, ks := range [][]string{attributeKeys, requisiteKeys, compilerKeys} {
		for _, k := range ks {
			want[k] = struct{}{}
		}
	}

	// ReservedKeySet agrees with the union.
	if got := ReservedKeySet(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReservedKeySet() = %v, want %v", got, want)
	}

	// ReservedKeys is the sorted slice form of the same set.
	list := ReservedKeys()
	if !sort.StringsAreSorted(list) {
		t.Errorf("ReservedKeys() not sorted: %v", list)
	}
	if len(list) != len(want) {
		t.Errorf("ReservedKeys() len = %d, want %d (%v)", len(list), len(want), list)
	}
	for _, k := range list {
		if _, ok := want[k]; !ok {
			t.Errorf("ReservedKeys() has unexpected key %q", k)
		}
	}

	// "name" is NOT reserved (module.run adds it locally).
	if _, ok := ReservedKeySet()["name"]; ok {
		t.Error(`ReservedKeySet() must EXCLUDE "name"`)
	}
}

// TestReservedKeys_ExpectedComposition pins the concrete membership of each
// source slice, so an accidental add/remove of a reserved key is a visible
// test failure (belt against silent drift, e.g. a "name" creeping in).
func TestReservedKeys_ExpectedComposition(t *testing.T) {
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"attributeKeys", AttributeKeys(), []string{"onlyif", "unless", "order", "retry", "failhard", "prereq"}},
		{"requisiteKeys", RequisiteKeys(), []string{"require", "watch", "onchanges", "onfail"}},
		{"compilerKeys", CompilerKeys(), []string{"names", "listen", "listen_in", "require_in", "watch_in", "onchanges_in", "onfail_in", "prereq_in"}},
	}
	for _, tc := range cases {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// TestReservedKeys_AccessorsCopy verifies the exported accessors return
// independent copies, so a consumer cannot mutate the canonical slices.
func TestReservedKeys_AccessorsCopy(t *testing.T) {
	a := AttributeKeys()
	if len(a) == 0 {
		t.Fatal("AttributeKeys() empty")
	}
	a[0] = "MUTATED"
	if AttributeKeys()[0] == "MUTATED" {
		t.Error("AttributeKeys() must return a copy, not the backing slice")
	}
	// Same for the others.
	CompilerKeys()[0] = "MUTATED"
	if CompilerKeys()[0] == "MUTATED" {
		t.Error("CompilerKeys() must return a copy")
	}
	RequisiteKeys()[0] = "MUTATED"
	if RequisiteKeys()[0] == "MUTATED" {
		t.Error("RequisiteKeys() must return a copy")
	}
}

// TestParseStateAttributes_ConsumesAttributeKeys is the slice↔parser pin for
// ParseStateAttributes. Because several keys need bespoke coercion, the parser
// names them structurally rather than looping attributeKeys; this test pins
// that the parser reacts to EXACTLY the attributeKeys set — every attribute key
// materializes a non-zero attribute, and no key outside the set (nor "name")
// does.
func TestParseStateAttributes_ConsumesAttributeKeys(t *testing.T) {
	// A value + "is it set?" predicate per attribute key.
	set := map[string]struct {
		val any
		on  func(StateAttributes) bool
	}{
		"onlyif":   {"true", func(a StateAttributes) bool { return len(a.Onlyif) > 0 }},
		"unless":   {"true", func(a StateAttributes) bool { return len(a.Unless) > 0 }},
		"order":    {5, func(a StateAttributes) bool { return a.hasOrder && a.Order == 5 }},
		"retry":    {3, func(a StateAttributes) bool { return a.RetryAttempts == 3 }},
		"failhard": {true, func(a StateAttributes) bool { return a.FailHard }},
		"prereq":   {"pkg.installed:x", func(a StateAttributes) bool { return len(a.Prereq) > 0 }},
	}

	// The keys covered here MUST equal attributeKeys exactly.
	if len(set) != len(attributeKeys) {
		t.Fatalf("test covers %d keys, attributeKeys has %d — update the test", len(set), len(attributeKeys))
	}
	for _, k := range attributeKeys {
		if _, ok := set[k]; !ok {
			t.Fatalf("attributeKeys has %q but the test does not exercise it", k)
		}
	}

	// Empty config → zero attributes.
	if !ParseStateAttributes(map[string]any{}).IsZero() {
		t.Fatal("empty config must parse to zero attributes")
	}

	// Each attribute key, in isolation, materializes its attribute.
	for k, tc := range set {
		attrs := ParseStateAttributes(map[string]any{k: tc.val})
		if attrs.IsZero() {
			t.Errorf("key %q did not materialize any attribute", k)
			continue
		}
		if !tc.on(attrs) {
			t.Errorf("key %q did not set its expected attribute: %+v", k, attrs)
		}
	}

	// Non-attribute keys (requisites, compiler directives, "name") must NOT
	// materialize an attribute.
	for _, k := range []string{"require", "watch", "onchanges", "onfail", "names", "listen", "require_in", "name", "path", "content"} {
		if _, isAttr := set[k]; isAttr {
			continue
		}
		if !ParseStateAttributes(map[string]any{k: "true"}).IsZero() {
			t.Errorf("non-attribute key %q wrongly materialized an attribute", k)
		}
	}
}

// TestParseRequisites_ConsumesRequisiteKeys pins the slice-driven parser:
// requisiteKeys is consumed in order, positionally paired with the output
// fields (Require, Watch, OnChanges, OnFail). A distinct ref per key proves the
// positional mapping.
func TestParseRequisites_ConsumesRequisiteKeys(t *testing.T) {
	if want := []string{"require", "watch", "onchanges", "onfail"}; !reflect.DeepEqual(requisiteKeys, want) {
		t.Fatalf("requisiteKeys = %v, want %v (order is load-bearing for ParseRequisites)", requisiteKeys, want)
	}

	config := map[string]any{
		"require":   "pkg.installed:req",
		"watch":     "pkg.installed:wat",
		"onchanges": "pkg.installed:onc",
		"onfail":    "pkg.installed:onf",
	}
	r := ParseRequisites(config)
	fields := map[string][]string{
		"require":   r.Require,
		"watch":     r.Watch,
		"onchanges": r.OnChanges,
		"onfail":    r.OnFail,
	}
	suffix := map[string]string{"require": "req", "watch": "wat", "onchanges": "onc", "onfail": "onf"}
	for _, key := range requisiteKeys {
		got := fields[key]
		wantRef := "pkg.installed:" + suffix[key]
		if len(got) != 1 || got[0] != wantRef {
			t.Errorf("requisite %q → %v, want [%q]", key, got, wantRef)
		}
	}

	// Empty config → all-empty requisites.
	if empty := ParseRequisites(map[string]any{}); len(empty.AllDeps()) != 0 {
		t.Errorf("empty config produced deps: %v", empty.AllDeps())
	}
}
