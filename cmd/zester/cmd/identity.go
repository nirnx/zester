package cmd

import (
	"os"
	"os/user"
)

// userCurrent is an indirection over user.Current so tests can exercise the
// fallback paths.
var userCurrent = user.Current

// currentOperator returns the identity of the operator running the CLI, used
// for audit trails (job dispatch, enrollment approvals). Resolution order:
// os/user.Current().Username, then the USER environment variable, then
// "unknown".
func currentOperator() string {
	if u, err := userCurrent(); err == nil && u.Username != "" {
		return u.Username
	}
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	return "unknown"
}
