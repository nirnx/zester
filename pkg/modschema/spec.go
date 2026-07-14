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
	// Doc is the registered documentation metadata (a detached copy — the
	// caller's retained slices cannot reach it).
	Doc Doc
	// Params is a detached, deep-copied snapshot of the derived, tag-free
	// parameter view. Mutating it affects only this snapshot — never the
	// compiled plan that Decode, Info, and the doc/schema renderers read
	// (review round 5: it previously aliased the plan's internal schema).
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
func NewSpec(module string, kind Kind, proto any, doc Doc, opts ...SpecOption) (*Spec, error) {
	if module == "" {
		return nil, fmt.Errorf("modschema: newspec: empty module name")
	}
	cs, err := Compile(proto, doc, opts...)
	if err != nil {
		return nil, err
	}
	// Stamp the module name so decode FieldErrors and unknown-key errors carry it.
	cs.module = module
	protoType := cs.protoType
	return &Spec{
		Module:    module,
		Kind:      kind,
		Doc:       cloneDoc(doc),
		Params:    cs.Schema(),
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
	// OpenParams mirrors Spec.OpenParams: a passthrough module (module.run) that
	// forwards arbitrary keys and skips unknown-key validation. It is a
	// live-registry-only property — a signal for machinery that walks the schema
	// (the compiler's names: expansion, which keeps injecting the "name"
	// passthrough selector into an OpenParams module rather than treating it as a
	// no-primary module). It is excluded from docdata.json (json:"-"): the offline
	// docs render through RenderText, which does not consult it, so carrying it in
	// the embed would only churn the artifact.
	OpenParams bool `json:"-"`
}

// Clone returns a fully detached deep copy of the ModuleInfo — Doc slices,
// parameter fields (aliases + JSON-Schema fragments), and semantic-type
// fragments. Callers serving ModuleInfo values from a long-lived cache (the
// embedded docdata, the dispatch-specials table) clone on egress so the
// framework's returned-views-are-safe-to-vandalize contract holds everywhere.
func (mi ModuleInfo) Clone() ModuleInfo {
	mi.Doc = cloneDoc(mi.Doc)
	mi.Params = cloneFields(mi.Params)
	if mi.SemTypes != nil {
		sts := make([]SemanticTypeInfo, len(mi.SemTypes))
		for i, st := range mi.SemTypes {
			if st.JSONSchema != nil {
				st.JSONSchema = cloneJSONValue(st.JSONSchema).(map[string]any)
			}
			sts[i] = st
		}
		mi.SemTypes = sts
	}
	return mi
}

// Info derives the ModuleInfo view of this Spec: its documentation, its ordered
// parameter fields, and the deduplicated set of semantic types those fields use
// (in field order). It reads only the compiled plan, so it never re-interprets
// struct tags.
func (s *Spec) Info() ModuleInfo {
	mi := ModuleInfo{
		Module:     s.Module,
		Kind:       s.Kind,
		Doc:        cloneDoc(s.Doc),
		Params:     cloneFields(s.plan.schema.Fields),
		HasSpec:    true,
		OpenParams: s.OpenParams,
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
			JSONSchema: cloneJSONValue(fp.semType.JSONSchema()).(map[string]any),
		})
	}
	return mi
}
