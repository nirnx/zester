package modschema

import (
	"fmt"
	"reflect"
)

// Spec is a compiled, registrable module: its parameter structure (a compiled
// BindingPlan) plus its registered documentation metadata, joined so a registry
// can decode config maps AND describe the module from one artifact. NewSpec is
// the sole constructor; it compiles the proto exactly once, and both Decode and
// a registry's Parse execute THAT same compiled plan (§4/§5).
type Spec struct {
	// Module is the canonical module name (for example "pkg.removed"). It is the
	// registration key in both registries and is stamped into decode FieldErrors.
	Module string
	// Kind classifies the surface (state | exec | dispatch); Effects requirements
	// differ by kind.
	Kind Kind
	// Doc is the registered documentation metadata.
	Doc Doc
	// Params is the derived, tag-free parameter view shared with docs, JSON
	// Schema, and sys.doc renderers.
	Params *ModuleSchema
	// NewParams returns a fresh pointer to the proto struct. Registry.Parse and
	// the differential harness allocate a destination through it.
	NewParams func() any
	// OpenParams marks a passthrough module (module.run): unknown-key validation
	// is skipped and the surface is described as accepting arbitrary parameters.
	OpenParams bool

	// plan is the single compiled BindingPlan. Decode and Parse both execute it,
	// so a second tag interpretation cannot exist.
	plan *CompiledSchema
}

// NewSpec compiles proto's parameter declaration and joins it with doc into a
// Spec. It is the only tag-parsing entry for a registered module; compilation
// happens once here. A compile error (duplicate name, >1 primary, unregistered
// non-primitive field type, invalid default literal, lazy-on-required, alias
// collision, …) is returned verbatim. An empty module name is rejected.
func NewSpec(module string, kind Kind, proto any, doc Doc) (*Spec, error) {
	if module == "" {
		return nil, fmt.Errorf("modschema: newspec: empty module name")
	}
	cs, err := Compile(proto, doc)
	if err != nil {
		return nil, err
	}
	// Stamp the module name so decode FieldErrors and unknown-key errors carry it.
	cs.module = module
	protoType := cs.protoType
	return &Spec{
		Module:    module,
		Kind:      kind,
		Doc:       doc,
		Params:    cs.schema,
		NewParams: func() any { return reflect.New(protoType).Interface() },
		plan:      cs,
	}, nil
}

// Decode executes the compiled plan against config, writing into dst (a non-nil
// pointer to the proto struct) only if every field succeeds. It is the same
// transactional Decode as the underlying CompiledSchema. For an OpenParams
// (passthrough) spec, unknown-key validation is skipped regardless of the
// caller's policy — arbitrary parameters are legitimate for such a module.
func (s *Spec) Decode(id string, config map[string]any, dst any, opts DecodeOptions) (*DecodeReport, error) {
	if s.OpenParams {
		opts.Unknown = PolicyIgnore
	}
	return s.plan.Decode(id, config, dst, opts)
}

// SemanticTypeInfo is a documentation-facing summary of one semantic parameter
// type used by a module: its stable name, its semantic doc string, and its JSON
// Schema fragment.
type SemanticTypeInfo struct {
	Name       string
	Doc        string
	JSONSchema map[string]any
}

// ModuleInfo is the shared documentation currency of sys.doc, `zester doc`,
// docgen, and docdata. It projects a Spec's compiled plan and documentation into
// a tag-free, renderer-ready shape. HasSpec is always true for a ModuleInfo
// produced from a Spec; AlsoExecmod (cross-registry knowledge, populated by the
// docs layer) defaults false here.
type ModuleInfo struct {
	Module      string
	Kind        Kind
	Doc         Doc
	Params      []Field
	SemTypes    []SemanticTypeInfo
	HasSpec     bool
	AlsoExecmod bool
}

// Info derives the ModuleInfo view of this Spec: its documentation, its ordered
// parameter fields, and the deduplicated set of semantic types those fields use
// (in field order). It reads only the compiled plan, so it never re-interprets
// struct tags.
func (s *Spec) Info() ModuleInfo {
	mi := ModuleInfo{
		Module:  s.Module,
		Kind:    s.Kind,
		Doc:     s.Doc,
		Params:  append([]Field(nil), s.plan.schema.Fields...),
		HasSpec: true,
	}
	seen := map[string]struct{}{}
	for i := range s.plan.fields {
		fp := &s.plan.fields[i]
		if fp.kind != kindSemantic {
			continue
		}
		name := fp.semType.Name()
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		mi.SemTypes = append(mi.SemTypes, SemanticTypeInfo{
			Name:       name,
			Doc:        fp.semType.Doc(),
			JSONSchema: fp.semType.JSONSchema(),
		})
	}
	return mi
}
