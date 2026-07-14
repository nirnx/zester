// Package sshauthmod implements the ssh_auth.* state-module family.
package sshauthmod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the ssh_auth.* family's slice of the built-in state-module table, in
// the family's historical registration order. The aggregator
// (pkg/state/modules) concatenates every family's Rows into the full table.
var Rows = []regdef.Registration{
	// ssh_auth.present / ssh_auth.absent — self-documenting schema (enc eager
	// default=ssh-rsa; the name-TrimSpace and require-user-OR-config cross-field
	// rule stay in the builder tail — module logic, not schema; key material is
	// PUBLIC, so nothing is sensitive). Both builders are themselves BuildFuncs.
	{Name: "ssh_auth.present", Spec: sshAuthPresentSpec, Build: NewSSHAuthPresentBuilder},
	{Name: "ssh_auth.absent", Spec: sshAuthAbsentSpec, Build: NewSSHAuthAbsentBuilder},
}
