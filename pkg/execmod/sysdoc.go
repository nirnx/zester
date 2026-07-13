package execmod

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
)

// DocSource supplies module documentation to sys.doc and sys.list_functions
// (keystone spec §7). It is the seam that keeps execmod from importing
// pkg/state: the peel implements it with knowledge of the dispatch specials,
// the state registry, and the execmod registry, and mirrors dispatch precedence
// (specials → state registry → execmod) in Describe, while Names returns the
// merged set of every callable surface for the unified index.
type DocSource interface {
	// Describe returns the ModuleInfo for a single module name following
	// dispatch precedence, or (zero, false) when nothing documents it.
	Describe(name string) (modschema.ModuleInfo, bool)
	// Names returns every callable surface name (state modules, execmod
	// functions, and concrete dispatch specials), sorted and deduplicated.
	Names() []string
}

// SysDoc builds the sys.doc execution function over src. With a bare invocation
// (no name) it returns the unified index — every documented/callable surface,
// one per line. With a module name (bare positional or name=) it renders that
// module's documentation through modschema.RenderText, the SAME renderer used
// by `zester doc` and docgen, so the live and embedded views agree. The result
// rides ExecResponse.Results[0].Details["result"] like every other execmod
// function — no wire change.
func SysDoc(src DocSource) Func {
	return func(_ context.Context, _ *exec.ModuleContext, args map[string]any) (string, error) {
		if src == nil {
			return "", fmt.Errorf("sys.doc: no documentation source configured")
		}
		name := argStr(args, "name", "module", "__id__")
		if name == "" {
			return strings.Join(src.Names(), "\n"), nil
		}
		mi, ok := src.Describe(name)
		if !ok {
			return "", fmt.Errorf("sys.doc: no documentation for %q", name)
		}
		return modschema.RenderText(mi), nil
	}
}

// SysListFunctions builds the merged sys.list_functions function over src:
// unlike the DefaultRegistry fallback (which lists only this registry's execmod
// functions), it lists every callable surface across the state registry, the
// execmod registry, and the concrete dispatch specials (spec §7). The peel
// re-registers sys.list_functions with this variant once its DocSource is wired.
func SysListFunctions(src DocSource) Func {
	return func(_ context.Context, _ *exec.ModuleContext, _ map[string]any) (string, error) {
		if src == nil {
			return "", fmt.Errorf("sys.list_functions: no documentation source configured")
		}
		return strings.Join(src.Names(), "\n"), nil
	}
}
