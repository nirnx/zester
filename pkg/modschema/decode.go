package modschema

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"

	"github.com/nirnx/zester/pkg/modschema/paramtypes"
)

// UnknownPolicy controls how Decode treats config keys that are neither a known
// parameter nor a reserved key.
type UnknownPolicy uint8

const (
	// PolicyIgnore records unknown keys in the report but does not warn or fail.
	PolicyIgnore UnknownPolicy = iota
	// PolicyWarn records and emits a warning via DecodeOptions.Warnf.
	PolicyWarn
	// PolicyError records and fails the decode with an UnknownKeyError.
	PolicyError
)

// DecodeOptions tunes a Decode call. Reserved is the set of state-reserved keys
// (state.ReservedKeySet()) that are known-but-not-parameters; ExtraReserved adds
// caller-specific reserved keys (for example the exec-layer "test"). Neither is
// reported as unknown.
type DecodeOptions struct {
	Unknown       UnknownPolicy
	Warnf         func(format string, args ...any)
	Reserved      map[string]struct{}
	ExtraReserved []string
}

// DecodeReport is the non-fatal outcome of a successful (or unknown-key-tolerant)
// Decode. UnknownKeys lists config keys that matched no parameter and no reserved
// key. LazyDefaults lists lazy parameters left unmaterialized (the module applies
// their documented default at use time).
type DecodeReport struct {
	UnknownKeys  []string
	LazyDefaults []string
}

// Decode executes the compiled plan against config, writing into dst only if
// every field succeeds (transactional). dst must be a non-nil pointer to the
// proto struct type. On any field or unknown-key error, dst is left untouched and
// the joined error is returned; the returned *DecodeReport is nil in that case.
func (cs *CompiledSchema) Decode(id string, config map[string]any, dst any, opts DecodeOptions) (*DecodeReport, error) {
	dv := reflect.ValueOf(dst)
	if dv.Kind() != reflect.Pointer || dv.IsNil() {
		return nil, fmt.Errorf("modschema: decode: dst must be a non-nil pointer, got %T", dst)
	}
	if dv.Elem().Type() != cs.protoType {
		return nil, fmt.Errorf("modschema: decode: dst is *%s, want *%s", dv.Elem().Type(), cs.protoType)
	}

	scratch := reflect.New(cs.protoType).Elem()
	report := &DecodeReport{}
	var errs []error

	// consumed tracks which config keys a parameter claimed (so they are never
	// flagged unknown even when a lower-precedence alias goes unused).
	consumed := map[string]struct{}{}

	for i := range cs.fields {
		fp := &cs.fields[i]
		field := scratch.FieldByIndex(fp.index)

		raw, key, origin, present := resolveSource(fp, config)
		if present {
			consumed[key] = struct{}{}
		}
		switch {
		case fp.primary && (!present || isEmptyString(raw)):
			// Primary fallback (§2.1): every source (name + aliases) was
			// absent/nil/empty. resolveSource already treats an empty string as
			// absent and falls through to the next source, so `!present` is the
			// live condition here — matching the universal legacy
			// `if x == "" { x = id }` idiom, so a `name: "{{ var }}"` that renders
			// empty with no alias set still falls back to the state ID. The
			// isEmptyString(raw) disjunct is a defensive backstop (unreachable while
			// resolveSource never reports an empty value present). Decodes the ID
			// with Origin: Primary; a semantic-typed primary likewise lands here.
			if err := cs.decodeInto(fp, field, id, "", paramtypes.OriginPrimary, id); err != nil {
				errs = append(errs, err)
			}
		case present:
			if err := cs.decodeInto(fp, field, raw, key, origin, id); err != nil {
				errs = append(errs, err)
			}
		case fp.lazy:
			// Documented default, never materialized; field stays zero.
			report.LazyDefaults = append(report.LazyDefaults, fp.name)
		case fp.eagerDefault:
			field.Set(fp.defaultValue)
		case fp.required:
			errs = append(errs, &FieldError{
				Module: cs.module,
				Param:  fp.name,
				Key:    fp.name,
				Kind:   ErrMissingRequired,
				Type:   fp.goType.String(),
			})
		}
	}

	// Mark every parameter alias as consumed so a present-but-unused alias is not
	// misreported as unknown.
	for i := range cs.fields {
		fp := &cs.fields[i]
		if _, ok := config[fp.name]; ok {
			consumed[fp.name] = struct{}{}
		}
		for _, a := range fp.aliases {
			if _, ok := config[a]; ok {
				consumed[a] = struct{}{}
			}
		}
	}

	unknownErrs := cs.checkUnknownKeys(config, consumed, report, opts)
	errs = append(errs, unknownErrs...)

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	// Commit only on full success.
	dv.Elem().Set(scratch)
	return report, nil
}

// resolveSource finds a field's value: canonical name first, then aliases in
// order. It returns the raw value, the key it was read from, the origin, and
// whether any source key was present. Per the §2.1/§2.2 amendment (the
// file.managed gate), a value that is absent, nil (YAML null — the `key:`
// trailing-colon shape), OR an empty string at a source FALLS THROUGH to the
// next source exactly like absence — mirroring the universal legacy
// `if x == "" { x = next }` idiom: a `name: ""` alongside a non-empty `path:`
// resolves to path (NOT the state ID), and only when every source is
// absent/empty does a primary later fall back to the ID. An empty string or
// nil therefore never reaches coercion via this path; a nil/empty canonical
// name still consults the aliases, and an all-empty match reports not-present.
func resolveSource(fp *fieldPlan, config map[string]any) (raw any, key string, origin paramtypes.InputOrigin, present bool) {
	if v, ok := config[fp.name]; ok && v != nil && !isEmptyString(v) {
		return v, fp.name, paramtypes.OriginExplicit, true
	}
	for _, a := range fp.aliases {
		if v, ok := config[a]; ok && v != nil && !isEmptyString(v) {
			return v, a, paramtypes.OriginAlias, true
		}
	}
	return nil, "", paramtypes.OriginExplicit, false
}

// isEmptyString reports whether raw is the empty string. It drives two §2.1/§3
// fallbacks: a primary parameter whose resolved value is "" falls back to the
// state ID (§2.1), and a non-primary semantic-typed parameter whose value is ""
// decodes to its undeclared zero value (§3 empty-string rule) — both mirror the
// universal legacy comma-ok idiom where an empty string reads as not-set.
func isEmptyString(raw any) bool {
	s, ok := raw.(string)
	return ok && s == ""
}

// decodeInto coerces raw into field per the field's plan, returning a FieldError
// on failure. It never mutates field on error.
func (cs *CompiledSchema) decodeInto(fp *fieldPlan, field reflect.Value, raw any, key string, origin paramtypes.InputOrigin, id string) error {
	if fp.kind == kindSemantic {
		if isEmptyString(raw) {
			// EMPTY-STRING RULE (keystone spec §3): a semantic type receiving an
			// empty string decodes to its undeclared zero value — legacy comma-ok
			// parity (`gid: ""` behaves as absent and falls through to a
			// lower-precedence source such as primary_group), mirroring §2.1's
			// empty-primary ID fallback. The scratch field already holds the zero
			// value, so leave it untouched and never invoke the type's Decode. This
			// is the single authoritative enforcement point — it is why every
			// sealed type "already conforms" without a per-type change.
			return nil
		}
		out, err := fp.semType.Decode(paramtypes.Input{
			Raw:     raw,
			Origin:  origin,
			Key:     key,
			StateID: id,
		})
		if err != nil {
			return cs.fieldError(fp, key, ErrValueInvalid, raw, err)
		}
		rv := reflect.ValueOf(out)
		if !rv.IsValid() || !rv.Type().AssignableTo(fp.goType) {
			return cs.fieldError(fp, key, ErrWrongType, raw,
				fmt.Errorf("semantic type %s returned %T, want %s", fp.semType.Name(), out, fp.goType))
		}
		field.Set(rv)
		return nil
	}
	if ce := coercePrimitive(fp.prim, field, raw); ce != nil {
		return cs.fieldError(fp, key, ce.kind, raw, errors.New(ce.msg))
	}
	return nil
}

// fieldError builds a FieldError. For a sensitive field it redacts BOTH the
// carried Value AND the wrapped cause: coercion/semantic causes interpolate the
// raw value into their messages (%q/%v), so the original cause is replaced with a
// value-free redacted error. This guarantees no substring of the secret survives
// in any rendered text or anywhere on the Unwrap chain (§2.5).
func (cs *CompiledSchema) fieldError(fp *fieldPlan, key string, kind ErrorKind, raw any, cause error) error {
	value := any(raw)
	if fp.sensitive {
		value = redactedValue
		cause = redactCause(kind)
	}
	return &FieldError{
		Module: cs.module,
		Param:  fp.name,
		Key:    keyOr(key, fp.name),
		Kind:   kind,
		Type:   fp.goType.String(),
		Value:  value,
		Err:    cause,
	}
}

// redactCause returns the value-free cause substituted for a sensitive field's
// failure. It carries the classification but never the raw value, and it wraps
// nothing, so the Unwrap chain terminates without reaching a value-bearing error.
func redactCause(kind ErrorKind) error {
	return fmt.Errorf("%s: %s", kind, redactedValue)
}

func keyOr(key, fallback string) string {
	if key == "" {
		return fallback
	}
	return key
}

// checkUnknownKeys applies the unknown-key policy over config keys not consumed
// by a parameter and not reserved.
func (cs *CompiledSchema) checkUnknownKeys(config map[string]any, consumed map[string]struct{}, report *DecodeReport, opts DecodeOptions) []error {
	var errs []error
	var unknown []string
	for k := range config {
		if _, ok := consumed[k]; ok {
			continue
		}
		if _, ok := cs.knownKeys[k]; ok {
			continue // a known parameter key (e.g. unused alias)
		}
		if _, ok := opts.Reserved[k]; ok {
			continue
		}
		if slices.Contains(opts.ExtraReserved, k) {
			continue
		}
		unknown = append(unknown, k)
	}
	sort.Strings(unknown)
	report.UnknownKeys = unknown

	if len(unknown) == 0 || opts.Unknown == PolicyIgnore {
		return nil
	}
	for _, k := range unknown {
		switch opts.Unknown {
		case PolicyWarn:
			if opts.Warnf != nil {
				opts.Warnf("modschema: %s: unknown parameter %q", moduleOr(cs.module), k)
			}
		case PolicyError:
			errs = append(errs, &UnknownKeyError{
				Module:     cs.module,
				Key:        k,
				Suggestion: cs.suggest(k),
				Known:      append([]string(nil), cs.knownNames...),
			})
		}
	}
	return errs
}

func moduleOr(m string) string {
	if m == "" {
		return "module"
	}
	return m
}

// suggest returns the nearest known parameter name within edit distance 2, or "".
func (cs *CompiledSchema) suggest(key string) string {
	best := ""
	bestDist := 3
	for _, name := range cs.knownNames {
		d := levenshtein(key, name)
		if d < bestDist {
			bestDist = d
			best = name
		}
	}
	if bestDist <= 2 {
		return best
	}
	return ""
}

// levenshtein computes the edit distance between a and b.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}
