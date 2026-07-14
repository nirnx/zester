// Package servicemod implements the service.* state-module family.
package servicemod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the service.* family's slice of the built-in state-module table, in
// the family's historical registration order. The aggregator
// (pkg/state/modules) concatenates every family's Rows into the full table.
var Rows = []regdef.Registration{
	// service.running / service.dead — the semantic-type pilot (tranche 0E):
	// enable migrated onto paramtypes.TriState. NewSvcRunningBuilder /
	// NewSvcDeadBuilder are themselves BuildFuncs (they thread opts into
	// spec.Decode), so no providerBuild adapter.
	{Name: "service.running", Spec: svcRunningSpec, Build: NewSvcRunningBuilder},
	{Name: "service.dead", Spec: svcDeadSpec, Build: NewSvcDeadBuilder},
	// service.enabled — self-documenting schema (name primary only; a numeric
	// name coerces, a composite name is rejected — BD-6). NewSvcEnabledBuilder is
	// itself a BuildFunc, so no providerBuild adapter.
	{Name: "service.enabled", Spec: svcEnabledSpec, Build: NewSvcEnabledBuilder},
}
