// Package hostmod implements the host.* state-module family.
package hostmod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the host.* family's slice of the built-in state-module table, in the
// family's historical registration order. The aggregator (pkg/state/modules)
// concatenates every family's Rows into the full table.
var Rows = []regdef.Registration{
	// host.present / host.absent — self-documenting schema (the per-field ALIAS
	// exemplar: the hosts-file path binds `config` with a `path` alias and an
	// eager default=/etc/hosts). Both builders are themselves BuildFuncs, so no
	// providerBuild adapter.
	{Name: "host.present", Spec: hostPresentSpec, Build: NewHostPresentBuilder},
	{Name: "host.absent", Spec: hostAbsentSpec, Build: NewHostAbsentBuilder},
}
