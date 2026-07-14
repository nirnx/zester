// Package mountmod implements the mount.* state-module family.
package mountmod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the mount.* family's slice of the built-in state-module table. The
// aggregator (pkg/state/modules) concatenates every family's Rows into the
// full table.
var Rows = []regdef.Registration{
	// mount.mounted — self-documenting schema (parameter-decode-ONLY migration:
	// Check/Apply/Revert and the deferred live-facet policy are unchanged;
	// dump/pass are plain ints, persist an eager default=true bool).
	// NewMountMountedBuilder is itself a BuildFunc, so no providerBuild adapter.
	{Name: "mount.mounted", Spec: mountMountedSpec, Build: NewMountMountedBuilder},
}
