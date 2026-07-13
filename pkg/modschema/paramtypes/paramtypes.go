// Package paramtypes defines the sealed vocabulary of named semantic parameter
// types used by the module-schema framework (github.com/nirnx/zester/pkg/
// modschema).
//
// A semantic type owns the decoding, validation, semantic documentation, and
// JSON Schema metadata for a polymorphic module parameter (for example an octal
// file mode that accepts either a string or a sized integer). The framework
// carries no per-type semantics: it walks a module's field declarations and,
// for any non-primitive field, dispatches to the semantic type registered for
// that Go type.
//
// The [SemanticType] interface is SEALED: the unexported sealed method means
// only this package can implement it. Semantic types therefore cannot be
// defined inline in a module package or in any foreign package — they live
// exclusively here and register themselves into the package registry. The
// concrete six-type vocabulary — FileMode, TriState, GroupRef, TemplateFlag,
// StringList, StringMap — lives alongside this interface in the package (one
// file per type) and is pinned by TestParamtypesConformance.
//
// paramtypes depends on the standard library only. It never imports modschema
// (which would create an import cycle); modschema imports paramtypes.
package paramtypes

import "reflect"

// InputOrigin records how a value reached a semantic type's Decode: whether it
// came from the canonical (name) key, an alias key, the synthesized state ID, or
// a declared default literal. A semantic type is free to ignore Origin, but it
// is available so declared-ness semantics under a future default= are
// well-defined (for example a tri-state that must distinguish an explicit value
// from a defaulted one).
type InputOrigin uint8

const (
	// OriginExplicit means the value came from the parameter's canonical name key.
	OriginExplicit InputOrigin = iota
	// OriginAlias means the value came from one of the parameter's alias keys.
	OriginAlias
	// OriginPrimary means the parameter is the primary and the value is the
	// synthesized state ID (no source key was present).
	OriginPrimary
	// OriginDefault means the value is the parameter's declared default literal.
	OriginDefault
)

// String renders the origin for diagnostics.
func (o InputOrigin) String() string {
	switch o {
	case OriginExplicit:
		return "explicit"
	case OriginAlias:
		return "alias"
	case OriginPrimary:
		return "primary"
	case OriginDefault:
		return "default"
	default:
		return "unknown"
	}
}

// Input is the sole argument to [SemanticType.Decode]. Raw is the value taken
// from the config map (or, for OriginPrimary/OriginDefault, the synthesized
// state ID / declared default literal). Key is the config key the value was read
// from (empty for primary/default). StateID is the state ID, provided so a
// semantic type can reference it if needed. Absent fields never reach Decode —
// the framework decides when a default applies; a zero value is undeclared.
type Input struct {
	Raw     any
	Origin  InputOrigin
	Key     string
	StateID string
}

// SemanticType is a named, sealed semantic parameter type. Every method is pure
// and safe to call on a value receiver; Decode never applies module-level
// defaults (that is the framework's job — see Input).
type SemanticType interface {
	// Name is the stable identifier used as the JSON Schema $defs key and the
	// docs anchor. It is unique across the registry.
	Name() string
	// GoType is the dispatch key: the Go type of struct fields that use this
	// semantic type, and the concrete type Decode returns. It is unique across
	// the registry.
	GoType() reflect.Type
	// Decode converts an Input into a value of GoType. It is pure and must not
	// apply module defaults.
	Decode(in Input) (any, error)
	// Doc is the semantic documentation string surfaced in MDX, sys.doc, and the
	// JSON Schema description.
	Doc() string
	// JSONSchema returns the accepted representations and constraints as a JSON
	// Schema fragment; its constraints are the type's validation surface.
	JSONSchema() map[string]any

	// sealed is unexported so only this package can implement SemanticType.
	sealed()
}
