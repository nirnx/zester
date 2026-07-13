package starmod

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/nirnx/zester/pkg/modschema"
	"go.starlark.net/starlark"
)

// paramsGlobalName is the module-global parameter-declaration dict applied to
// every apply function that does not declare its own <fn>_params dict.
const paramsGlobalName = "PARAMS"

// paramsGlobalSuffix names the per-function parameter-declaration dict: an apply
// function `configured` opts in via a module global `configured_params`.
const paramsGlobalSuffix = "_params"

// paramDecl is one parsed parameter declaration from a PARAMS / <fn>_params
// dict, optionally enriched with usage text from the docstring Args: section.
type paramDecl struct {
	name       string
	typ        string // starlark-facing type word: str|bool|int|float|list|dict
	required   bool
	hasDefault bool
	def        string // scalar default literal (only for scalar-typed params)
	usage      string
	aliases    []string
}

// collectParamDicts extracts the module-global PARAMS dict and every
// <fn>_params dict from a file's Starlark globals. A global is a parameter
// declaration only when it is a *starlark.Dict; anything else under those names
// is ignored (never a hard error — a misnamed value simply leaves the module
// self-documented as open-params).
func collectParamDicts(globals starlark.StringDict) (moduleParams map[string]any, fnParams map[string]map[string]any) {
	fnParams = map[string]map[string]any{}
	for name, val := range globals {
		dict, ok := val.(*starlark.Dict)
		if !ok {
			continue
		}
		converted, ok := StarlarkToGo(dict).(map[string]any)
		if !ok {
			continue
		}
		switch {
		case name == paramsGlobalName:
			moduleParams = converted
		case strings.HasSuffix(name, paramsGlobalSuffix) && len(name) > len(paramsGlobalSuffix):
			fn := strings.TrimSuffix(name, paramsGlobalSuffix)
			fnParams[fn] = converted
		}
	}
	return moduleParams, fnParams
}

// buildStarSpec compiles a self-documenting modschema.Spec for one Starlark
// apply function. paramsDict is the resolved declaration dict (the function's
// own <fn>_params, else the module-global PARAMS, else nil).
//
// The spec's parameter structure is a SYNTHETIC proto struct built from the
// declaration via reflect.StructOf and compiled through the ordinary
// modschema.NewSpec path — so NewParams returns a real (non-nil) destination,
// Registry.Parse/Describe/Decode all work, and no framework change is needed for
// a dynamically-typed source language. OpenParams is set true UNLESS a non-empty
// declaration dict was present (declared params opt the module into unknown-key
// warnings; §5/§7 of the keystone spec).
func buildStarSpec(moduleName string, fn *starlark.Function, paramsDict map[string]any) (*modschema.Spec, error) {
	summary, description, args := parseDocstring(fn.Doc())

	var decls []paramDecl
	declared := false
	if paramsDict != nil {
		decls = parseParamDecls(paramsDict, args)
		declared = len(decls) > 0
	}
	if !declared {
		// Open-params module: surface any docstring-documented parameters for
		// display only (string-typed, never validated).
		decls = argsOnlyDecls(args)
	}

	protoType, err := buildProtoType(decls)
	if err != nil {
		return nil, err
	}

	doc := modschema.Doc{Summary: summary, Description: description}
	if note, ok := sourceNote(fn); ok {
		doc.Notes = append(doc.Notes, note)
	}

	protoPtr := reflect.New(protoType).Interface()
	spec, err := modschema.NewSpec(moduleName, modschema.KindState, protoPtr, doc)
	if err != nil {
		return nil, err
	}
	// OpenParams is a public field; NewSpec leaves it false. A module that
	// declared no parameter dict accepts arbitrary keys (Starlark reads the raw
	// config), so unknown-key validation is skipped for it.
	spec.OpenParams = !declared
	return spec, nil
}

// sourceNote captures the function's source location (from fn.Position(), the
// pinned starlark-go API) as an informational Doc note so provenance is visible
// in sys.doc / `zester doc` output.
func sourceNote(fn *starlark.Function) (modschema.Note, bool) {
	pos := fn.Position()
	if !pos.IsValid() {
		return modschema.Note{}, false
	}
	loc := pos.Filename()
	if pos.Line > 0 {
		loc = fmt.Sprintf("%s:%d", pos.Filename(), pos.Line)
	}
	if loc == "" {
		return modschema.Note{}, false
	}
	return modschema.Note{Level: "info", Title: "Source", Body: loc}, true
}

// parseParamDecls turns a declaration dict into ordered paramDecls. Each value
// is either a help string (shorthand) or a dict with optional keys type,
// required, default, usage/help, aliases. Docstring Args: text fills usage when
// the dict did not supply it. Order is name-sorted for deterministic schemas.
func parseParamDecls(paramsDict map[string]any, args map[string]string) []paramDecl {
	names := make([]string, 0, len(paramsDict))
	for k := range paramsDict {
		names = append(names, k)
	}
	sort.Strings(names)

	decls := make([]paramDecl, 0, len(names))
	for _, name := range names {
		d := paramDecl{name: name}
		switch v := paramsDict[name].(type) {
		case string:
			d.usage = v
		case map[string]any:
			d.typ = stringOf(v["type"])
			d.required = boolOf(v["required"])
			d.usage = firstNonEmpty(stringOf(v["usage"]), stringOf(v["help"]))
			d.aliases = stringListOf(v["aliases"])
			if raw, ok := v["default"]; ok {
				if lit, ok := scalarLiteral(raw); ok {
					d.hasDefault = true
					d.def = lit
				}
			}
		}
		if d.usage == "" {
			d.usage = args[name] // docstring Args: fallback
		}
		decls = append(decls, d)
	}
	return decls
}

// argsOnlyDecls builds display-only, string-typed declarations from a
// docstring's Args: section for an open-params module. These populate the
// documentation view but are never validated.
func argsOnlyDecls(args map[string]string) []paramDecl {
	if len(args) == 0 {
		return nil
	}
	names := make([]string, 0, len(args))
	for k := range args {
		names = append(names, k)
	}
	sort.Strings(names)
	decls := make([]paramDecl, 0, len(names))
	for _, name := range names {
		decls = append(decls, paramDecl{name: name, usage: args[name]})
	}
	return decls
}

// buildProtoType assembles a synthetic struct type from param declarations,
// carrying zester:/usage: tags that modschema.Compile reads exactly like a
// hand-written proto. Field names are synthetic (P0, P1, …); the canonical
// config key comes from the tag, never the Go field name.
func buildProtoType(decls []paramDecl) (reflect.Type, error) {
	fields := make([]reflect.StructField, 0, len(decls))
	for i, d := range decls {
		gt := goTypeFor(d.typ)
		fields = append(fields, reflect.StructField{
			Name: fmt.Sprintf("P%d", i),
			Type: gt,
			Tag:  reflect.StructTag(buildFieldTag(d, gt)),
		})
	}
	// reflect.StructOf panics on a malformed field set; recover into an error so
	// a pathological declaration degrades to plain registration rather than
	// crashing the loader.
	var st reflect.Type
	var perr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				perr = fmt.Errorf("starmod: build proto: %v", r)
			}
		}()
		st = reflect.StructOf(fields)
	}()
	if perr != nil {
		return nil, perr
	}
	return st, nil
}

// buildFieldTag renders the zester:/usage: struct tag for one declaration. A
// default is emitted only for a scalar-typed param whose literal is comma-free
// (the tag grammar splits options on commas); required is emitted only when no
// default is present (a default implies the param is optional).
func buildFieldTag(d paramDecl, gt reflect.Type) string {
	zval := d.name
	if len(d.aliases) > 0 {
		zval += ",aliases=" + strings.Join(d.aliases, "|")
	}
	emitDefault := d.hasDefault && isScalarType(gt) && !strings.Contains(d.def, ",")
	switch {
	case emitDefault:
		zval += ",default=" + d.def
	case d.required:
		zval += ",required"
	}
	return fmt.Sprintf("zester:%s usage:%s", strconv.Quote(zval), strconv.Quote(d.usage))
}

// goTypeFor maps a starlark-facing type word to a Go primitive type. Unknown or
// empty words map to string (documented lenient default).
func goTypeFor(typ string) reflect.Type {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "bool", "boolean":
		return reflect.TypeFor[bool]()
	case "int", "integer":
		return reflect.TypeFor[int]()
	case "float", "number":
		return reflect.TypeFor[float64]()
	case "list", "array":
		return reflect.TypeFor[[]any]()
	case "dict", "map", "object":
		return reflect.TypeFor[map[string]any]()
	default: // "", "str", "string", or anything unrecognized
		return reflect.TypeFor[string]()
	}
}

// isScalarType reports whether a synthetic field type can carry a `default=`
// literal decoded from a string (string/bool/int/float).
func isScalarType(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Float64:
		return true
	default:
		return false
	}
}

// --- docstring parsing (Google style) ---

// parseDocstring splits a function docstring into a one-line summary, a
// CommonMark description paragraph, and a name→text map from an Args:/Arguments:
// section. It normalizes PEP 257-style indentation first.
func parseDocstring(doc string) (summary, description string, args map[string]string) {
	args = map[string]string{}
	if strings.TrimSpace(doc) == "" {
		return "", "", args
	}
	lines := dedent(strings.Split(doc, "\n"))

	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) {
		return "", "", args
	}
	summary = strings.TrimSpace(lines[i])
	i++

	var desc []string
	argsStart := -1
	for j := i; j < len(lines); j++ {
		if isArgsHeader(strings.TrimSpace(lines[j])) {
			argsStart = j
			break
		}
		desc = append(desc, lines[j])
	}
	description = strings.TrimSpace(strings.Join(desc, "\n"))
	if argsStart >= 0 {
		parseArgsSection(lines[argsStart+1:], args)
	}
	return summary, description, args
}

// parseArgsSection reads "name: description" entries under an Args: header.
// Continuation lines (more-indented, no leading "name:") append to the current
// entry. A blank line, a dedent below the entry indent, or a new "Word:"
// section header ends the section.
func parseArgsSection(lines []string, args map[string]string) {
	argIndent := -1
	cur := ""
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			break
		}
		indent := leadingSpaces(ln)
		trimmed := strings.TrimSpace(ln)
		if argIndent == -1 {
			argIndent = indent
		}
		if indent < argIndent {
			break
		}
		if indent == argIndent && isSectionHeader(trimmed) {
			break
		}
		if indent > argIndent && cur != "" {
			args[cur] = strings.TrimSpace(args[cur] + " " + trimmed)
			continue
		}
		name, text, ok := splitArgLine(trimmed)
		if !ok {
			if cur != "" {
				args[cur] = strings.TrimSpace(args[cur] + " " + trimmed)
			}
			continue
		}
		cur = name
		args[name] = text
	}
}

// splitArgLine parses "name (type): description" or "name: description",
// returning the bare parameter name and the description.
func splitArgLine(s string) (name, text string, ok bool) {
	idx := strings.Index(s, ":")
	if idx <= 0 {
		return "", "", false
	}
	left := strings.TrimSpace(s[:idx])
	text = strings.TrimSpace(s[idx+1:])
	// Strip an optional "(type)" annotation and take the first token as the name.
	if p := strings.IndexAny(left, " ("); p >= 0 {
		left = left[:p]
	}
	if left == "" || !isIdentLike(left) {
		return "", "", false
	}
	return left, text, true
}

// isArgsHeader reports whether a line is the Args: section header.
func isArgsHeader(t string) bool {
	return strings.EqualFold(t, "Args:") || strings.EqualFold(t, "Arguments:")
}

// isSectionHeader reports whether a line is a Google-style section header such
// as "Returns:" or "Raises:" (a single capitalized word followed by a colon).
func isSectionHeader(t string) bool {
	if !strings.HasSuffix(t, ":") {
		return false
	}
	word := strings.TrimSuffix(t, ":")
	if word == "" {
		return false
	}
	for _, r := range word {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return word[0] >= 'A' && word[0] <= 'Z'
}

// isIdentLike reports whether s is a plausible parameter identifier.
func isIdentLike(s string) bool {
	for _, r := range s {
		if !(r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// dedent applies PEP 257 docstring dedentation: the first line is trimmed as-is
// (it follows the opening quotes), and the common leading indentation of the
// remaining non-empty lines is stripped from every subsequent line.
func dedent(lines []string) []string {
	minIndent := -1
	for _, ln := range lines[1:] {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		n := leadingSpaces(ln)
		if minIndent == -1 || n < minIndent {
			minIndent = n
		}
	}
	out := make([]string, len(lines))
	out[0] = strings.TrimRight(strings.TrimLeft(lines[0], " \t"), " \t")
	for i := 1; i < len(lines); i++ {
		ln := lines[i]
		if minIndent > 0 {
			if leadingSpaces(ln) >= minIndent {
				ln = ln[minIndent:]
			} else {
				ln = strings.TrimLeft(ln, " \t")
			}
		}
		out[i] = strings.TrimRight(ln, " \t")
	}
	return out
}

// leadingSpaces counts leading space/tab runes (a tab counts as one column,
// sufficient for space-indented Starlark source).
func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r == ' ' || r == '\t' {
			n++
			continue
		}
		break
	}
	return n
}

// --- small value helpers ---

func stringOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func boolOf(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func stringListOf(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range list {
		if s, ok := e.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// scalarLiteral renders a scalar Go value (as produced by StarlarkToGo) into a
// tag default literal. Non-scalars (lists, dicts, nil) return ok=false.
func scalarLiteral(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case uint64:
		return strconv.FormatUint(t, 10), true
	case int:
		return strconv.Itoa(t), true
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), true
	default:
		return "", false
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
