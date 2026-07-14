// Package sysctlmod implements the sysctl.* state-module family.
package sysctlmod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the sysctl.* family's slice of the built-in state-module table. The
// aggregator (pkg/state/modules) concatenates every family's Rows into the
// full table.
var Rows = []regdef.Registration{
	// sysctl.present — self-documenting schema (value required, persist an eager
	// default=true bool; the require-file-provider-when-persist rule stays in the
	// builder tail). NewSysctlPresentBuilder is itself a BuildFunc.
	{Name: "sysctl.present", Spec: sysctlPresentSpec, Build: NewSysctlPresentBuilder},
}
