// Package pipmod implements the pip.* state-module family.
package pipmod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the pip.* family's slice of the built-in state-module table. The
// aggregator (pkg/state/modules) concatenates every family's Rows into the
// full table.
var Rows = []regdef.Registration{
	// pip.installed — self-documenting schema (the tooling wave —
	// archive.extracted / git.cloned / git.latest / pip.installed /
	// timezone.system / locale.present: all-primitives, no semantic types
	// needed). NewPipInstalledBuilder is itself a BuildFunc, so no
	// providerBuild adapter.
	{Name: "pip.installed", Spec: pipInstalledSpec, Build: NewPipInstalledBuilder},
}
