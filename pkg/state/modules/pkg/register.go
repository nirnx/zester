// Package pkgmod implements the pkg.* state-module family.
package pkgmod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the pkg.* family's slice of the built-in state-module table, in the
// family's historical registration order. The aggregator (pkg/state/modules)
// concatenates every family's Rows into the full table.
var Rows = []regdef.Registration{
	// pkg.installed — self-documenting schema (all-primitives migration wave).
	// NewPkgInstalledBuilder is itself a BuildFunc, so no adapter.
	{Name: "pkg.installed", Spec: pkgInstalledSpec, Build: NewPkgInstalledBuilder},
	// pkg.removed — pilot #1: self-documenting schema. NewPkgRemovedBuilder is
	// itself a BuildFunc (it threads opts into spec.Decode), so no adapter.
	{Name: "pkg.removed", Spec: pkgRemovedSpec, Build: NewPkgRemovedBuilder},
	// pkg.latest / pkg.purged — self-documenting schema (all-primitives
	// migration wave). Both builders are themselves BuildFuncs, so no adapter.
	{Name: "pkg.latest", Spec: pkgLatestSpec, Build: NewPkgLatestBuilder},
	{Name: "pkg.purged", Spec: pkgPurgedSpec, Build: NewPkgPurgedBuilder},
}
