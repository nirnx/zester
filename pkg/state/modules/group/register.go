// Package groupmod implements the group.* state-module family.
package groupmod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the group.* family's slice of the built-in state-module table, in
// the family's historical registration order. The aggregator
// (pkg/state/modules) concatenates every family's Rows into the full table.
var Rows = []regdef.Registration{
	// group.present / group.absent — self-documenting schema (group.present's
	// gid is a PLAIN int; members/addusers/delusers on paramtypes.StringList).
	// Both builders are themselves BuildFuncs, so no providerBuild adapter.
	{Name: "group.present", Spec: groupPresentSpec, Build: NewGroupPresentBuilder},
	{Name: "group.absent", Spec: groupAbsentSpec, Build: NewGroupAbsentBuilder},
}
