// Package cmdmod implements the cmd.* state-module family.
package cmdmod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the cmd.* family's slice of the built-in state-module table. The
// aggregator (pkg/state/modules) concatenates every family's Rows into the
// full table.
var Rows = []regdef.Registration{
	// cmd.run — self-documenting schema (command primary; args on
	// paramtypes.StringList, env on paramtypes.StringMap; the
	// require-file-provider-when-creates rule stays in the builder tail).
	// NewCmdRunBuilder is itself a BuildFunc, so no providerBuild adapter.
	{Name: "cmd.run", Spec: cmdRunSpec, Build: NewCmdRunBuilder},
}
