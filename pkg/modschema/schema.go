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
}

// Schema returns the derived ModuleSchema view of the compiled plan.
func (cs *CompiledSchema) Schema() *ModuleSchema { return cs.schema }
