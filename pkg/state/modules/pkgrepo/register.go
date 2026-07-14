// Package pkgrepomod implements the pkgrepo.* state-module family.
package pkgrepomod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the pkgrepo.* family's slice of the built-in state-module table. The
// aggregator (pkg/state/modules) concatenates every family's Rows into the
// full table.
var Rows = []regdef.Registration{
	// pkgrepo.managed — self-documenting schema (a parameter-decode-only
	// migration: Check/Apply/Revert and the deferred in-place key-rotation
	// detection are unchanged; humanname is a lazy DERIVED default assigned in
	// the builder tail, enabled/gpgcheck/refresh eager default=true bools).
	// NewPkgrepoManagedBuilder is itself a BuildFunc, so no providerBuild adapter.
	{Name: "pkgrepo.managed", Spec: pkgrepoManagedSpec, Build: NewPkgrepoManagedBuilder},
}
