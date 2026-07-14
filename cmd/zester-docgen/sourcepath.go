package main

import (
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/modschema"
)

// sourcePath derives the **Source**: line target for a module: the
// implementation file docgen expects it to live in, following the repo's
// established naming conventions. State modules live in family packages
// (family = the module name's first dotted segment) with per-module files
// whose dots become underscores; e.g. "pkg.removed" ->
// pkg/state/modules/pkg/pkg_removed.go. Exec and dispatch kinds are not
// migrated yet (Phase 1+); their convention is recorded here so the anatomy
// renderer has one source of truth once they are.
func sourcePath(kind modschema.Kind, module string) string {
	file := strings.ReplaceAll(module, ".", "_") + ".go"
	switch kind {
	case modschema.KindState:
		family, _, _ := strings.Cut(module, ".")
		return "pkg/state/modules/" + family + "/" + file
	case modschema.KindExec:
		return "pkg/execmod/" + file
	case modschema.KindDispatch:
		return "internal/peeld/" + file
	default:
		return fmt.Sprintf("(unknown source for kind %q)", kind)
	}
}
