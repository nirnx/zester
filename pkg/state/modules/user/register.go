// Package usermod implements the user.* state-module family.
package usermod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the user.* family's slice of the built-in state-module table, in the
// family's historical registration order. The aggregator (pkg/state/modules)
// concatenates every family's Rows into the full table.
var Rows = []regdef.Registration{
	// user.present — self-documenting schema (the semantic-type-heavy migration:
	// gid on paramtypes.GroupRef, groups/optional_groups on paramtypes.StringList,
	// a sensitive password). NewUserPresentBuilder is itself a BuildFunc (it
	// threads opts into spec.Decode), so no providerBuild adapter.
	{Name: "user.present", Spec: userPresentSpec, Build: NewUserPresentBuilder},
	// user.absent — self-documenting schema (all-primitives: name primary,
	// purge/force plain bools; nothing sensitive). NewUserAbsentBuilder is
	// itself a BuildFunc, so no providerBuild adapter.
	{Name: "user.absent", Spec: userAbsentSpec, Build: NewUserAbsentBuilder},
}
