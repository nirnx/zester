package modschema

// ModuleSchema is the derived, tag-free view of a compiled module: the ordered
// list of parameter fields plus the module documentation. It is the shared
// currency of the docs, JSON Schema export, and sys.doc renderers, all of which
// read this rather than re-interpreting struct tags.
type ModuleSchema struct {
	// Fields are the module's parameters in declaration (plan) order.
	Fields []Field
	// Doc is the module's registered documentation metadata.
	Doc Doc
}

// Field is one decoded parameter as seen by documentation and schema consumers.
// A sensitive field never renders its Default and is flagged so every renderer
// redacts it.
type Field struct {
	// Name is the canonical config key.
	Name string
	// Aliases are additional accepted keys, in precedence order after Name.
	Aliases []string
	// GoType is the field's Go type as a display string (for example "string",
	// "int8", "paramtypes.FileMode").
	GoType string
	// SemanticType is the registered semantic type's Name for a semantic field,
	// or "" for a primitive field.
	SemanticType string
	// Usage is the help text from the usage:"…" tag.
	Usage string
	// Required reports whether the field is required.
	Required bool
	// Primary reports whether the field receives the state ID when no source key
	// is present.
	Primary bool
	// Lazy reports whether the field's default is documented but never
	// materialized (the module applies it at use time).
	Lazy bool
	// Sensitive reports whether the field's value is redacted everywhere.
	Sensitive bool
	// HasDefault reports whether a default literal was declared (eager or lazy).
	HasDefault bool
	// Default is the human-rendered default. It is empty for sensitive fields and
	// for fields without a declared default.
	Default string
	// JSONSchema is the field's JSON Schema fragment.
	JSONSchema map[string]any
	// DeclaredBy is the Go struct type that declared the field (§13): a family
	// parameter component's type for componentized parameters, the module's
	// own proto type otherwise. Consumed by the vocabulary gate; additive.
	DeclaredBy string `json:",omitempty"`
	// DefaultMemberSupplied / RequiredMemberSupplied mirror the component's
	// memberdefault/memberrequired declarations (§13): the dimension varies
	// per member BY CONTRACT, and the vocabulary gate excludes it from the
	// in-family signature when every participant shares the component.
	DefaultMemberSupplied  bool `json:",omitempty"`
	RequiredMemberSupplied bool `json:",omitempty"`
}

// Schema returns the derived ModuleSchema view of the compiled plan as a DEEP
// COPY: the compiled plan is shared, long-lived state (registries, docgen,
// sys.doc render concurrently), and handing out the internal pointer let a
// consumer mutate later docs/schema output or race other readers (review
// round 4).
func (cs *CompiledSchema) Schema() *ModuleSchema {
	return &ModuleSchema{
		Doc:    cloneDoc(cs.schema.Doc),
		Fields: cloneFields(cs.schema.Fields),
	}
}

// cloneDoc deep-copies a Doc's slice fields (their elements are all-scalar
// structs, so a per-slice copy fully detaches the clone).
func cloneDoc(d Doc) Doc {
	d.Examples = append([]Example(nil), d.Examples...)
	d.Notes = append([]Note(nil), d.Notes...)
	d.Divergences = append([]string(nil), d.Divergences...)
	d.SeeAlso = append([]string(nil), d.SeeAlso...)
	return d
}

// Clone returns a deep copy of the Doc: its slice fields are detached (their
// elements are all-scalar structs). It is the exported seam for any package
// handing out documentation derived from long-lived shared state — the
// framework contract (pinned by TestInfoAndSchemaAreDeepCopies) is that every
// returned documentation view is safe to vandalize.
func (d Doc) Clone() Doc {
	return cloneDoc(d)
}

// cloneFields deep-copies a Field slice including each JSONSchema fragment.
func cloneFields(in []Field) []Field {
	out := append([]Field(nil), in...)
	for i := range out {
		out[i].Aliases = append([]string(nil), out[i].Aliases...)
		if out[i].JSONSchema != nil {
			out[i].JSONSchema = cloneJSONValue(out[i].JSONSchema).(map[string]any)
		}
	}
	return out
}

// cloneJSONValue deep-copies the JSON-shaped values (maps, slices, scalars)
// that schema fragments are built from.
func cloneJSONValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = cloneJSONValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = cloneJSONValue(val)
		}
		return out
	default:
		return v
	}
}
