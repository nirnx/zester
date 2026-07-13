// Package modschema is the module-schema framework: it compiles a module's
// struct-tag parameter declaration and documentation metadata into a single
// CompiledSchema, then decodes config maps against that compiled plan.
//
// The design is "compile once, decode from the plan": Compile performs the ONLY
// tag-parsing pass and produces a BindingPlan (per field: index path, canonical
// name, aliases, resolved decoder, pre-decoded eager default, and the
// required/lazy/primary/sensitive flags) plus a derived ModuleSchema view for
// docs, JSON Schema, and sys.doc. Every consumer reads the same compiled plan, so
// a second tag interpretation cannot exist.
//
// Decode executes the plan transactionally: a config map is decoded into a
// scratch instance and the caller's destination is written only if every field
// succeeds. Primitive coercion follows a fixed table; non-primitive fields
// dispatch to a sealed semantic type via paramtypes.ForGoType.
//
// modschema depends on the standard library and pkg/modschema/paramtypes only.
// It never imports pkg/state or internal/*.
package modschema

// Kind classifies a documented surface. Effects requirements differ by kind
// (validated in a later tranche): state surfaces document Check+Apply (Revert
// optional), exec and dispatch surfaces document Execution.
type Kind string

const (
	// KindState is an idempotent state module (Check/Apply/Revert phases).
	KindState Kind = "state"
	// KindExec is an imperative remote-execution module.
	KindExec Kind = "exec"
	// KindDispatch is a peel dispatch surface (facts.*, settings.*, …).
	KindDispatch Kind = "dispatch"
)

// Doc is a module's registered documentation metadata. Description is CommonMark
// rendered between the Source line and the Parameters table. Divergences carries
// behavioral-difference IDs (BD-…) surfaced in the generated page.
type Doc struct {
	Summary     string
	Description string // CommonMark
	Effects     Effects
	Examples    []Example
	Notes       []Note
	Divergences []string
	SeeAlso     []string
}

// Effects describes what a module does in each phase. State modules populate
// Check and Apply (Revert optional); exec and dispatch modules populate
// Execution. Kind-conditional coverage is enforced in a later tranche.
type Effects struct {
	Check     string
	Apply     string
	Revert    string
	Execution string
}

// Example is a documented usage example. Kind is "cli" or "state"; Code is the
// literal example body rendered in a fenced block.
type Example struct {
	Title       string
	Kind        string // "cli" | "state"
	Explanation string
	Code        string
}

// Note is a rendered callout. Level is the callout severity (for example "info"
// or "warning"); Body is CommonMark.
type Note struct {
	Level string
	Title string
	Body  string
}
