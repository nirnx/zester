// Package localemod implements the locale.* state-module family.
package localemod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the locale.* family's slice of the built-in state-module table. The
// aggregator (pkg/state/modules) concatenates every family's Rows into the
// full table.
var Rows = []regdef.Registration{
	// locale.present — self-documenting schema (the tooling wave —
	// archive.extracted / git.cloned / git.latest / pip.installed /
	// timezone.system / locale.present: all-primitives, no semantic types
	// needed). NewLocalePresentBuilder is itself a BuildFunc, so no
	// providerBuild adapter.
	{Name: "locale.present", Spec: localePresentSpec, Build: NewLocalePresentBuilder},
}
