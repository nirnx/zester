// Package cliargs is the neutral home of the operator CLI's key=value argument
// parser. It was relocated verbatim from cmd/zester/cmd so the module-schema
// differential test harness (pkg/modschema/schematest) can exercise the REAL CLI
// ingress path without importing the cobra command tree, and so a later phase can
// unify parseModuleArgs on top of it. The CLI (cmd/zester/cmd) re-imports it.
//
// cliargs depends on the standard library only.
package cliargs

import "strings"

// ParseKeyValues parses key=value pairs from a slice and adds them to the args map.
func ParseKeyValues(pairs []string, args map[string]any) {
	for _, pair := range pairs {
		if k, v, ok := strings.Cut(pair, "="); ok {
			args[k] = v
		}
	}
}
