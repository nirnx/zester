// Package timezonemod implements the timezone.* state-module family.
package timezonemod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the timezone.* family's slice of the built-in state-module table.
// The aggregator (pkg/state/modules) concatenates every family's Rows into the
// full table.
var Rows = []regdef.Registration{
	// timezone.system — self-documenting schema (the tooling wave —
	// archive.extracted / git.cloned / git.latest / pip.installed /
	// timezone.system / locale.present: all-primitives, no semantic types
	// needed). NewTimezoneSystemBuilder is itself a BuildFunc, so no
	// providerBuild adapter.
	{Name: "timezone.system", Spec: timezoneSystemSpec, Build: NewTimezoneSystemBuilder},
}
