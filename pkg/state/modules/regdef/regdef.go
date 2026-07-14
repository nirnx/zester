// Package regdef holds the registration row shape shared by every state-module
// family package (pkg/state/modules/<family>): the builder-shape types, the
// Registration row, and the MustSpec compile-at-init helper. Family packages
// export their rows as `var Rows = []regdef.Registration{...}`; the
// pkg/state/modules aggregator concatenates them into the full built-in table.
package regdef

import (
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// BuildFunc constructs a state.Builder from the injected providers and the
// decode policy. Every module built through this shape threads DecodeOptions
// into its spec.Decode call.
type BuildFunc func(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder

// PlainBuildFunc constructs a state.Builder that needs no exec providers but
// still threads the decode policy (reserved keys, unknown-key handling) into its
// spec.Decode call. The test.* helpers use this shape: no ModuleContext, but a
// real self-documenting decode.
type PlainBuildFunc func(opts modschema.DecodeOptions) state.Builder

// Registration is one row of the built-in state-module table. Exactly one of the
// three builder shapes is set:
//
//   - Build             — needs the ModuleContext (providers) and decode policy.
//   - BuildPlain        — needs no providers, but threads the decode policy (the
//     test.* helpers).
//   - BuildWithRegistry — needs the registry itself (module.run dispatches to it).
//
// Spec is non-nil once a module is migrated to a self-documenting schema; the
// row is then RegisterSpec'd (name = Spec.Module) instead of plain-Register'd.
type Registration struct {
	Name string
	Spec *modschema.Spec

	Build             BuildFunc
	BuildPlain        PlainBuildFunc
	BuildWithRegistry func(reg *state.Registry) state.Builder
}

// MustSpec compiles a module spec at package init, panicking on a compile error
// — an invalid schema declaration is a programming error, caught at load, never
// a runtime condition.
func MustSpec(module string, kind modschema.Kind, proto any, doc modschema.Doc, opts ...modschema.SpecOption) *modschema.Spec {
	s, err := modschema.NewSpec(module, kind, proto, doc, opts...)
	if err != nil {
		panic(fmt.Sprintf("modules: spec %s: %v", module, err))
	}
	return s
}
