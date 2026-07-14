// Package cronmod implements the cron.* state-module family.
package cronmod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the cron.* family's slice of the built-in state-module table, in the
// family's historical registration order. The aggregator (pkg/state/modules)
// concatenates every family's Rows into the full table.
var Rows = []regdef.Registration{
	// cron.present / cron.absent — self-documenting schema (cron.present's
	// schedule fields carry eager default=* — a numeric minute now coerces to
	// "5", the BD-3 activation). Both builders are themselves BuildFuncs.
	{Name: "cron.present", Spec: cronPresentSpec, Build: NewCronPresentBuilder},
	{Name: "cron.absent", Spec: cronAbsentSpec, Build: NewCronAbsentBuilder},
}
