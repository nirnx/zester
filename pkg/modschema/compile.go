package modschema

import (
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"

	"github.com/nirnx/zester/pkg/modschema/paramtypes"
)

// fieldKind distinguishes a primitive field (coerced via the fixed table) from a
// semantic field (dispatched to a paramtypes.SemanticType).
type fieldKind uint8

const (
	kindPrimitive fieldKind = iota
	kindSemantic
)

// primKind selects the primitive coercion for a field. primInt covers every int
// and uint kind; the field's own reflect.Kind resolves range and assignment.
type primKind uint8

const (
	primString primKind = iota
	primBool
	primInt
	primFloat
	primMapAny
	primSliceAny
)

// fieldPlan is the compiled binding for one parameter. It is produced once by
// Compile and read (never re-derived) by Decode.
type fieldPlan struct {
	index   []int        // reflect field index path from the proto struct
	goType  reflect.Type // the field's Go type
	name    string       // canonical config key
	aliases []string     // alias keys, precedence after name
	usage   string       // usage:"…" help text

	kind    fieldKind
	prim    primKind                // valid when kind == kindPrimitive
	semType paramtypes.SemanticType // valid when kind == kindSemantic

	required  bool
	lazy      bool
	primary   bool
	sensitive bool

	// eagerDefault applies defaultValue when the field is absent (non-lazy only).
	eagerDefault bool
	defaultValue reflect.Value // pre-decoded, field-typed; valid when eagerDefault
	// hasDefaultLit records a declared default literal (eager OR lazy) for docs.
	hasDefaultLit bool
	defaultLit    string

	jsonSchema map[string]any
}

// CompiledSchema is the immutable result of Compile: the binding plan plus the
// derived documentation view. Decode executes this plan; docs/schema/sys.doc read
// Schema(). It is safe for concurrent use.
type CompiledSchema struct {
	protoType reflect.Type // struct type of the proto (never a pointer)
	module    string       // set by the owning Spec; "" from a bare Compile
	doc       Doc
	fields    []fieldPlan
	// knownKeys is name∪aliases across all fields (for unknown-key detection).
	knownKeys map[string]struct{}
	// knownNames is the sorted parameter names (for suggestions and reporting).
	knownNames []string
	schema     *ModuleSchema
}

// Compile is the ONLY tag-parsing pass. It walks proto's struct fields, parses
// each zester:"…" tag exactly once, resolves the decoder (primitive table or
// registered semantic type), pre-decodes eager defaults through that decoder, and
// produces the CompiledSchema. proto may be a struct value or a pointer to one.
//
// Compile errors: a non-struct proto, a duplicate parameter name, more than one
// primary, an unregistered non-primitive field type, an invalid default literal,
// or lazy declared on a required field.
func Compile(proto any, doc Doc) (*CompiledSchema, error) {
	if proto == nil {
		return nil, fmt.Errorf("modschema: compile: nil proto")
	}
	t := reflect.TypeOf(proto)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("modschema: compile: proto must be a struct, got %s", t.Kind())
	}

	cs := &CompiledSchema{
		protoType: t,
		doc:       doc,
		knownKeys: map[string]struct{}{},
	}

	seenNames := map[string]struct{}{}
	primarySeen := false
	if err := cs.walk(t, nil); err != nil {
		return nil, err
	}
	for i := range cs.fields {
		fp := &cs.fields[i]
		if _, dup := seenNames[fp.name]; dup {
			return nil, fmt.Errorf("modschema: compile: duplicate parameter name %q", fp.name)
		}
		seenNames[fp.name] = struct{}{}
		if fp.primary {
			if primarySeen {
				return nil, fmt.Errorf("modschema: compile: more than one primary parameter")
			}
			primarySeen = true
		}
	}

	// Alias collisions are compile errors: an alias equal to any canonical name
	// (its own field's or another's) or to another field's alias would make
	// multi-binding ambiguous, so it is unrepresentable (§2.2).
	aliasOwner := map[string]string{}
	for i := range cs.fields {
		fp := &cs.fields[i]
		for _, a := range fp.aliases {
			if _, isName := seenNames[a]; isName {
				return nil, fmt.Errorf("modschema: compile: alias %q on param %q collides with a parameter name", a, fp.name)
			}
			if owner, dup := aliasOwner[a]; dup {
				return nil, fmt.Errorf("modschema: compile: alias %q on param %q collides with an alias on param %q", a, fp.name, owner)
			}
			aliasOwner[a] = fp.name
		}
	}

	// Build known-key/known-name indexes and the derived schema view.
	for i := range cs.fields {
		fp := &cs.fields[i]
		cs.knownKeys[fp.name] = struct{}{}
		cs.knownNames = append(cs.knownNames, fp.name)
		for _, a := range fp.aliases {
			cs.knownKeys[a] = struct{}{}
		}
	}
	// Known names are kept sorted so UnknownKeyError.Known (a copy of this slice)
	// is sorted per its documented contract, and suggestion tie-breaks are stable.
	sort.Strings(cs.knownNames)
	cs.schema = cs.deriveSchema()
	return cs, nil
}

// walk recursively records tagged leaf fields, recursing into untagged struct
// fields (composition). prefix is the running index path.
//
// Field selection: an unexported NON-embedded field is unusable (reflect cannot
// set it) and skipped; a tagged unexported field is a declaration error. An
// anonymous (embedded) struct field is always recursed into — its promoted
// exported leaves are settable even when the embedded type itself is unexported.
func (cs *CompiledSchema) walk(t reflect.Type, prefix []int) error {
	for i := range t.NumField() {
		sf := t.Field(i)
		exported := sf.PkgPath == ""
		index := append(append([]int(nil), prefix...), i)
		tag, tagged := sf.Tag.Lookup("zester")
		if tagged && strings.TrimSpace(tag) == "-" {
			continue // explicit skip
		}

		if !exported && !sf.Anonymous {
			if tagged {
				return fmt.Errorf("modschema: compile: field %s: zester tag on an unexported field", sf.Name)
			}
			continue // unexported non-embedded field: not settable
		}

		if !tagged {
			// Untagged struct (not a registered semantic type) recurses;
			// untagged non-struct is skipped.
			if sf.Type.Kind() == reflect.Struct {
				if _, isSem := paramtypes.ForGoType(sf.Type); !isSem {
					if err := cs.walk(sf.Type, index); err != nil {
						return err
					}
				}
			}
			continue
		}
		fp, err := cs.compileField(sf, index, tag)
		if err != nil {
			return err
		}
		cs.fields = append(cs.fields, fp)
	}
	return nil
}

// compileField parses one zester tag and resolves the field's decoder + default.
func (cs *CompiledSchema) compileField(sf reflect.StructField, index []int, tag string) (fieldPlan, error) {
	fp := fieldPlan{
		index:  index,
		goType: sf.Type,
		usage:  sf.Tag.Get("usage"),
	}

	parts := strings.Split(tag, ",")
	fp.name = strings.TrimSpace(parts[0])
	if fp.name == "" {
		return fp, fmt.Errorf("modschema: compile: field %s: empty parameter name in tag %q", sf.Name, tag)
	}

	var defaultLit string
	var hasDefault bool
	for _, opt := range parts[1:] {
		opt = strings.TrimSpace(opt)
		switch {
		case opt == "":
			continue
		case opt == "primary":
			fp.primary = true
		case opt == "required":
			fp.required = true
		case opt == "lazy":
			fp.lazy = true
		case opt == "sensitive":
			fp.sensitive = true
		case strings.HasPrefix(opt, "aliases="):
			raw := strings.TrimPrefix(opt, "aliases=")
			for a := range strings.SplitSeq(raw, "|") {
				a = strings.TrimSpace(a)
				if a != "" {
					fp.aliases = append(fp.aliases, a)
				}
			}
		case strings.HasPrefix(opt, "default="):
			defaultLit = strings.TrimPrefix(opt, "default=")
			hasDefault = true
		default:
			return fp, fmt.Errorf("modschema: compile: field %s: unknown tag option %q", sf.Name, opt)
		}
	}

	if fp.lazy && fp.required {
		return fp, fmt.Errorf("modschema: compile: param %q: lazy cannot be combined with required", fp.name)
	}

	// Resolve the decoder: registered semantic type wins; else the primitive
	// table; else an unregistered non-primitive is a seal-wall error.
	if st, ok := paramtypes.ForGoType(sf.Type); ok {
		fp.kind = kindSemantic
		fp.semType = st
		fp.jsonSchema = st.JSONSchema()
	} else if pk, ok := classifyPrimitive(sf.Type); ok {
		fp.kind = kindPrimitive
		fp.prim = pk
		fp.jsonSchema = primitiveJSONSchema(pk, sf.Type)
	} else {
		return fp, fmt.Errorf("modschema: compile: param %q: unregistered non-primitive field type %s", fp.name, sf.Type)
	}

	// A sensitive parameter's JSON Schema fragment carries writeOnly (§2.5) so
	// every schema consumer marks it write-only. This covers BOTH primitive and
	// semantic fragments; the fragment is cloned because a semantic type's
	// JSONSchema() may return a shared map that must not be mutated.
	if fp.sensitive {
		fp.jsonSchema = withWriteOnly(fp.jsonSchema)
	}

	// Pre-decode the default literal through the field's own decoder (validates
	// it at compile time). A lazy field's default is documented but never applied.
	if hasDefault {
		fp.hasDefaultLit = true
		fp.defaultLit = defaultLit
		v, err := cs.decodeDefault(&fp, defaultLit)
		if err != nil {
			return fp, fmt.Errorf("modschema: compile: param %q: invalid default %q: %w", fp.name, defaultLit, err)
		}
		if !fp.lazy {
			fp.eagerDefault = true
			fp.defaultValue = v
		}
	}
	return fp, nil
}

// decodeDefault decodes a default literal into a field-typed reflect.Value using
// the same coercion Decode uses, treating the literal as a default-origin string.
func (cs *CompiledSchema) decodeDefault(fp *fieldPlan, lit string) (reflect.Value, error) {
	if fp.kind == kindSemantic {
		out, err := fp.semType.Decode(paramtypes.Input{
			Raw:    lit,
			Origin: paramtypes.OriginDefault,
			Key:    fp.name,
		})
		if err != nil {
			return reflect.Value{}, err
		}
		rv := reflect.ValueOf(out)
		if !rv.IsValid() || !rv.Type().AssignableTo(fp.goType) {
			return reflect.Value{}, fmt.Errorf("semantic type %s returned %T, want %s", fp.semType.Name(), out, fp.goType)
		}
		return rv, nil
	}
	scratch := reflect.New(fp.goType).Elem()
	if ce := coercePrimitive(fp.prim, scratch, lit); ce != nil {
		return reflect.Value{}, ce
	}
	return scratch, nil
}

// deriveSchema builds the tag-free documentation view from the plan.
func (cs *CompiledSchema) deriveSchema() *ModuleSchema {
	ms := &ModuleSchema{Doc: cs.doc}
	for i := range cs.fields {
		fp := &cs.fields[i]
		f := Field{
			Name:       fp.name,
			Aliases:    append([]string(nil), fp.aliases...),
			GoType:     fp.goType.String(),
			Usage:      fp.usage,
			Required:   fp.required,
			Primary:    fp.primary,
			Lazy:       fp.lazy,
			Sensitive:  fp.sensitive,
			HasDefault: fp.hasDefaultLit,
			JSONSchema: fp.jsonSchema,
		}
		if fp.kind == kindSemantic {
			f.SemanticType = fp.semType.Name()
		}
		if fp.hasDefaultLit && !fp.sensitive {
			f.Default = fp.defaultLit
		}
		ms.Fields = append(ms.Fields, f)
	}
	return ms
}

// Exact primitive target types. A named type (for example a semantic type) never
// equals one of these, so it is never misclassified as a primitive.
var (
	tString   = reflect.TypeFor[string]()
	tBool     = reflect.TypeFor[bool]()
	tInt      = reflect.TypeFor[int]()
	tInt8     = reflect.TypeFor[int8]()
	tInt16    = reflect.TypeFor[int16]()
	tInt32    = reflect.TypeFor[int32]()
	tInt64    = reflect.TypeFor[int64]()
	tUint     = reflect.TypeFor[uint]()
	tUint8    = reflect.TypeFor[uint8]()
	tUint16   = reflect.TypeFor[uint16]()
	tUint32   = reflect.TypeFor[uint32]()
	tUint64   = reflect.TypeFor[uint64]()
	tFloat32  = reflect.TypeFor[float32]()
	tFloat64  = reflect.TypeFor[float64]()
	tMapAny   = reflect.TypeFor[map[string]any]()
	tSliceAny = reflect.TypeFor[[]any]()
)

// classifyPrimitive maps a field type to its primitive coercion. It matches
// EXACT builtin types only; []string, map[string]string, durations, and any
// named type are deliberately not primitives (they belong to semantic types).
func classifyPrimitive(t reflect.Type) (primKind, bool) {
	switch t {
	case tString:
		return primString, true
	case tBool:
		return primBool, true
	case tInt, tInt8, tInt16, tInt32, tInt64,
		tUint, tUint8, tUint16, tUint32, tUint64:
		return primInt, true
	case tFloat32, tFloat64:
		return primFloat, true
	case tMapAny:
		return primMapAny, true
	case tSliceAny:
		return primSliceAny, true
	default:
		return 0, false
	}
}

// withWriteOnly returns a shallow copy of frag with writeOnly:true added, never
// mutating the input (a semantic type's JSONSchema() may return a shared map).
func withWriteOnly(frag map[string]any) map[string]any {
	out := make(map[string]any, len(frag)+1)
	maps.Copy(out, frag)
	out["writeOnly"] = true
	return out
}

// primitiveJSONSchema renders the JSON Schema fragment for a primitive,
// REPRESENTATION-FAITHFUL to the coercion table (§2.3): the schema accepts
// exactly the shapes the runtime decoder accepts — including the declared
// leniencies (whitespace-trimmed, case-insensitive boolean strings; trimmed
// numeric strings) — so an editor never flags a value the peel would take.
// anyOf (at least one), never oneOf (exactly one): representation branches
// legitimately overlap in JSON's type model (every integer is also a number,
// and JSON Schema's "integer" itself accepts 5.0), and oneOf's
// exactly-one-match semantics turned that overlap into a rejection of every
// integer (review finding). The decoder remains the enforcement authority for
// what a pattern cannot express (range checks).
func primitiveJSONSchema(pk primKind, t reflect.Type) map[string]any {
	switch pk {
	case primString:
		// Scalars coerce into strings via fmt.Sprint (declared, deliberate).
		return map[string]any{"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "number"},
			map[string]any{"type": "boolean"},
		}}
	case primBool:
		// bool | integer 0/1 (BD-7) | truthy/falsy string (BD-2) — the string
		// arm mirrors coerceBool's ToLower(TrimSpace(v)) exactly: any casing,
		// surrounding whitespace allowed. ECMA regex has no reliable inline
		// case-flag in JSON Schema, so the pattern spells the classes out.
		return map[string]any{"anyOf": []any{
			map[string]any{"type": "boolean"},
			map[string]any{"type": "integer", "enum": []any{0, 1}},
			map[string]any{"type": "string",
				"pattern": `^\s*([tT][rR][uU][eE]|[fF][aA][lL][sS][eE]|[yY][eE][sS]|[nN][oO]|[oO][nN]|[oO][fF][fF]|[01])\s*$`},
		}}
	case primInt:
		// JSON Schema "integer" already accepts integral numbers (5.0), which
		// is exactly the coercion table's integral-float acceptance — one
		// branch covers both. Strings: base-10, trimmed.
		return map[string]any{"anyOf": []any{
			map[string]any{"type": "integer"},
			map[string]any{"type": "string", "pattern": `^\s*[+-]?[0-9]+\s*$`},
		}}
	case primFloat:
		return map[string]any{"anyOf": []any{
			map[string]any{"type": "number"},
			map[string]any{"type": "string", "pattern": `^\s*[+-]?([0-9]*[.])?[0-9]+([eE][+-]?[0-9]+)?\s*$`},
		}}
	case primMapAny:
		return map[string]any{"type": "object"}
	case primSliceAny:
		return map[string]any{"type": "array"}
	default:
		return map[string]any{}
	}
}
