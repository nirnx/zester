package state

import "sort"

// Reserved state keys are the config-map keys that Zester interprets itself —
// generic per-state attributes, requisite declarations, and compiler-only
// directives — as opposed to keys that a module builder consumes as its own
// parameters. The union is the single source of truth for unknown-key
// validation and for module.run's target-vs-directive disambiguation.
//
// Membership deliberately EXCLUDES "name": it is a legitimate module parameter
// (the primary-param override), not a reserved directive. Consumers that must
// also treat "name" as reserved (e.g. module.run's target resolution) add it
// locally on top of ReservedKeySet().
//
// Composition (never hand-copied): attributeKeys are consumed by
// ParseStateAttributes, requisiteKeys by ParseRequisites, and compilerKeys by
// pkg/state/compiler. The compiler cannot be imported by pkg/state, so a
// conformance test in package compiler pins that its actually-consumed keys
// (inverseForward, sameStateAlias, the names-expansion key) ⊆ ReservedKeys().

// attributeKeys are the generic Salt-parity per-state attributes consumed by
// ParseStateAttributes. This slice IS the parser's consumed key set.
var attributeKeys = []string{
	"onlyif",
	"unless",
	"order",
	"retry",
	"failhard",
	"prereq",
}

// requisiteKeys are the same-state requisite declarations consumed by
// ParseRequisites, in the order its output fields are populated
// (Require, Watch, OnChanges, OnFail). This slice IS the parser's consumed
// key set; ParseRequisites is driven from it.
var requisiteKeys = []string{
	"require",
	"watch",
	"onchanges",
	"onfail",
}

// compilerKeys are the directives consumed only by pkg/state/compiler: the
// `names:` expansion, the `listen` same-state alias, and the inverse ("_in")
// requisite forms. pkg/state does not consume these, but they are reserved
// fleet-wide so that unknown-key validation and module.run treat them
// uniformly. A conformance test in package compiler pins these against the
// compiler's real consumed keys.
var compilerKeys = []string{
	"names",
	"listen",
	"listen_in",
	"require_in",
	"watch_in",
	"onchanges_in",
	"onfail_in",
	"prereq_in",
}

// AttributeKeys returns a copy of the generic per-state attribute keys
// (onlyif, unless, order, retry, failhard, prereq) consumed by
// ParseStateAttributes.
func AttributeKeys() []string { return append([]string(nil), attributeKeys...) }

// RequisiteKeys returns a copy of the same-state requisite keys
// (require, watch, onchanges, onfail) consumed by ParseRequisites, in the
// order its output fields are populated.
func RequisiteKeys() []string { return append([]string(nil), requisiteKeys...) }

// CompilerKeys returns a copy of the compiler-only directive keys
// (names, listen, listen_in, and the `_in` inverse forms). pkg/state/compiler
// DERIVES its rewrite maps from this list rather than re-declaring the key
// strings; a compiler-package conformance test pins consumed ⊆ ReservedKeys().
func CompilerKeys() []string { return append([]string(nil), compilerKeys...) }

// ReservedKeys returns the deduplicated, sorted union of the attribute,
// requisite, and compiler key sets. It EXCLUDES "name" (a legitimate module
// parameter). Callers wire this into modschema's unknown-key validators and
// into module.run's directive-vs-target disambiguation.
func ReservedKeys() []string {
	set := ReservedKeySet()
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ReservedKeySet returns the reserved-key union as a set for O(1) membership
// tests. Same membership as ReservedKeys (excludes "name").
func ReservedKeySet() map[string]struct{} {
	set := make(map[string]struct{}, len(attributeKeys)+len(requisiteKeys)+len(compilerKeys))
	for _, keys := range [][]string{attributeKeys, requisiteKeys, compilerKeys} {
		for _, k := range keys {
			set[k] = struct{}{}
		}
	}
	return set
}
