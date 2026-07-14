// Package modulemod implements the module.* state-module family.
package modulemod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the module.* family's slice of the built-in state-module table. The
// aggregator (pkg/state/modules) concatenates every family's Rows into the
// full table — this family LAST, so module.run registers after every target it
// may dispatch to.
var Rows = []regdef.Registration{
	// module.run — self-documenting schema (OpenParams: a passthrough with no
	// fixed parameters; its dynamic target/key handling is unchanged). It
	// captures the registry so it can invoke any other module by name, and must
	// be registered after the targets it may dispatch to.
	{Name: "module.run", Spec: moduleRunSpec, BuildWithRegistry: NewModuleRunBuilder},
}
