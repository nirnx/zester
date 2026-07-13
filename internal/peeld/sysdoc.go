package peeld

import (
	"sort"

	"github.com/nirnx/zester/pkg/execmod"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state/modules"
)

// peelDocSource implements execmod.DocSource (the sys.doc seam, §7). It mirrors
// the peel's dispatch precedence when describing one module — concrete dispatch
// specials first, then the state registry, then the execmod registry — and
// merges every callable surface for the unified index. It holds the Agent so it
// reads the live registries at call time (Starlark modules registered later are
// visible).
type peelDocSource struct{ a *Agent }

// Describe returns the documentation for name following dispatch precedence:
//
//  1. A concrete dispatch special (state.apply, facts.get, event.send, …) —
//     the peel handles these itself, so they outrank a same-named registry
//     entry, exactly as execModule's dispatch does.
//  2. A migrated state module (carries a modschema.Spec). If that module is
//     ALSO an execmod function (cmd.run), AlsoExecmod is set so RenderText
//     appends the "reachable from templates via salt['<mod>']" dual-surface
//     note.
//  3. A spec-registered execmod function.
//
// Unmigrated modules (no Spec anywhere) report (zero, false): the index lists
// them, but a per-name lookup honestly says it has no documentation yet.
func (d peelDocSource) Describe(name string) (modschema.ModuleInfo, bool) {
	if mi, ok := modules.DispatchInfo(name); ok {
		return mi, true
	}
	if mi, ok := d.a.registry.Describe(name); ok {
		if d.a.execReg.Has(name) {
			mi.AlsoExecmod = true
		}
		return mi, true
	}
	if mi, ok := d.a.execReg.Describe(name); ok {
		return mi, true
	}
	return modschema.ModuleInfo{}, false
}

// Names returns the sorted, deduplicated union of every callable surface: state
// module names, execmod function names, and the concrete dispatch-special names.
func (d peelDocSource) Names() []string {
	set := map[string]struct{}{}
	for _, n := range d.a.registry.Modules() {
		set[n] = struct{}{}
	}
	for _, n := range d.a.execReg.Names() {
		set[n] = struct{}{}
	}
	for _, n := range modules.DispatchNames() {
		set[n] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// wireDocSource registers sys.doc and re-registers the merged sys.list_functions
// on the peel's execmod registry, backed by the peel's DocSource. It must be
// called after both a.registry and a.execReg are constructed. sys.doc then
// dispatches through the ordinary execmod path (read-only, so it answers during
// long state runs — see readOnlyModule), and its result string rides
// ExecResponse.Results[0].Details["result"] with no wire change.
func (a *Agent) wireDocSource() {
	src := peelDocSource{a: a}
	a.execReg.Register("sys.doc", execmod.SysDoc(src))
	a.execReg.Register("sys.list_functions", execmod.SysListFunctions(src))
}
