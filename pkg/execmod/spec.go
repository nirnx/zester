package execmod

import (
	"fmt"
	"sort"

	"github.com/nirnx/zester/pkg/modschema"
)

// RegisterSpec registers a self-documenting execution function: its compiled
// schema plus documentation metadata (spec) together with the implementation
// fn. The registration key is spec.Module. Unlike Register (which silently
// replaces), RegisterSpec is strict: a nil spec, an empty module name, a nil
// fn, or a duplicate spec name is an error. On success fn is also installed in
// the Call path, so Call(spec.Module, …) works exactly as before.
func (r *Registry) RegisterSpec(spec *modschema.Spec, fn Func) error {
	if spec == nil {
		return fmt.Errorf("execmod: register spec: nil spec")
	}
	if spec.Module == "" {
		return fmt.Errorf("execmod: register spec: empty module name")
	}
	if fn == nil {
		return fmt.Errorf("execmod: register spec %q: nil function", spec.Module)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.specs[spec.Module]; dup {
		return fmt.Errorf("execmod: register spec: duplicate module %q", spec.Module)
	}
	r.specs[spec.Module] = spec
	r.funcs[spec.Module] = fn
	return nil
}

// Describe returns the ModuleInfo for a spec-registered function. It reports
// (zero, false) for a name that was registered only via Register (no spec) or
// is not registered at all.
func (r *Registry) Describe(name string) (modschema.ModuleInfo, bool) {
	r.mu.RLock()
	spec, ok := r.specs[name]
	r.mu.RUnlock()
	if !ok {
		return modschema.ModuleInfo{}, false
	}
	return spec.Info(), true
}

// SpecNames returns the sorted names of every spec-registered function.
func (r *Registry) SpecNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.specs))
	for n := range r.specs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
