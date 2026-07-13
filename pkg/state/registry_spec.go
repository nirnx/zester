package state

import (
	"fmt"
	"sort"

	"github.com/nirnx/zester/pkg/modschema"
)

// RegisterSpec registers a self-documenting module: its compiled schema plus
// documentation metadata (spec) together with the builder that constructs its
// State. The registration key is spec.Module. Unlike Register (which silently
// overwrites, preserving legacy/Starlark late-registration semantics),
// RegisterSpec is strict: a nil spec, an empty module name, a nil builder, or a
// duplicate spec name is an error. On success the builder is also installed in
// the Build path, so Build(spec.Module, …) works exactly as before.
func (r *Registry) RegisterSpec(spec *modschema.Spec, b Builder) error {
	if spec == nil {
		return fmt.Errorf("state: register spec: nil spec")
	}
	if spec.Module == "" {
		return fmt.Errorf("state: register spec: empty module name")
	}
	if b == nil {
		return fmt.Errorf("state: register spec %q: nil builder", spec.Module)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.specs[spec.Module]; dup {
		return fmt.Errorf("state: register spec: duplicate module %q", spec.Module)
	}
	r.specs[spec.Module] = spec
	r.builders[spec.Module] = b
	return nil
}

// ReplaceSpec registers a documented module like RegisterSpec but OVERWRITES
// any existing spec and builder under the same name. It exists solely for
// dynamic, reloadable registration paths — the Starlark loader's hot-reload
// and formula-overrides-global semantics — so Describe (and therefore sys.doc
// and the offline docs) always reflects the latest loaded source. Built-in
// module registration must use the strict RegisterSpec; a conformance test
// pins that RegisterAll never calls ReplaceSpec.
func (r *Registry) ReplaceSpec(spec *modschema.Spec, b Builder) error {
	if spec == nil {
		return fmt.Errorf("state: replace spec: nil spec")
	}
	if spec.Module == "" {
		return fmt.Errorf("state: replace spec: empty module name")
	}
	if b == nil {
		return fmt.Errorf("state: replace spec %q: nil builder", spec.Module)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs[spec.Module] = spec
	r.builders[spec.Module] = b
	return nil
}

// Describe returns the ModuleInfo for a spec-registered module. It reports
// (zero, false) for a name that was registered only via Register (legacy or
// Starlark, no spec) or is not registered at all.
func (r *Registry) Describe(name string) (modschema.ModuleInfo, bool) {
	r.mu.RLock()
	spec, ok := r.specs[name]
	r.mu.RUnlock()
	if !ok {
		return modschema.ModuleInfo{}, false
	}
	return spec.Info(), true
}

// SpecNames returns the sorted names of every spec-registered module.
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

// Parse decodes raw into a fresh proto for the named spec-registered module,
// executing the SAME compiled plan the module's constructor uses, and returns
// the decode report. It is the schema-driven decode entry point (validation,
// docgen); the decoded proto is discarded — callers that need the built State
// use Build. Unknown keys are ignored (PolicyIgnore), and the state-reserved
// keys (requisites, generic attributes, compiler directives) are injected via
// Reserved so they are never mistaken for a module's own unknown parameters and
// so never land in DecodeReport.UnknownKeys. An unregistered or spec-less name
// is an error.
func (r *Registry) Parse(name, id string, raw map[string]any) (*modschema.DecodeReport, error) {
	r.mu.RLock()
	spec, ok := r.specs[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("state: parse: no spec registered for module %q", name)
	}
	dst := spec.NewParams()
	return spec.Decode(id, raw, dst, modschema.DecodeOptions{Reserved: ReservedKeySet()})
}
