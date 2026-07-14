// Package archivemod implements the archive.* state-module family.
package archivemod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the archive.* family's slice of the built-in state-module table. The
// aggregator (pkg/state/modules) concatenates every family's Rows into the
// full table.
var Rows = []regdef.Registration{
	// archive.extracted — self-documenting schema (the tooling wave —
	// archive.extracted / git.cloned / git.latest / pip.installed /
	// timezone.system / locale.present: all-primitives, no semantic types
	// needed). NewArchiveExtractedBuilder is itself a BuildFunc, so no
	// providerBuild adapter.
	{Name: "archive.extracted", Spec: archiveExtractedSpec, Build: NewArchiveExtractedBuilder},
}
