package schematest

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/cliargs"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	yaml "gopkg.in/yaml.v3"
)

// Decoder is the new decoder under permanent regression test. It decodes a config
// map into a typed value (a state proto or an exec-module shape) and returns it,
// or an error. It is any-typed so RunContract works for every migration.
//
// (When pkg/modschema.Spec lands in a later tranche, a caller wraps spec.Decode
// in this shape — schematest itself stays Spec-agnostic in this tranche because
// Spec does not exist yet.)
type Decoder func(id string, config map[string]any) (any, error)

// contractFile is the on-disk shape of testdata/contract/<module>.yaml.
type contractFile struct {
	Module string         `yaml:"module"`
	Cases  []contractCase `yaml:"cases"`
}

// contractCase is one recorded case: for a universe, either an expected
// exported-field projection (accept) or an expected error kind (reject).
type contractCase struct {
	Label     string         `yaml:"label"`
	Universe  string         `yaml:"universe"` // yaml|msgpack|cli; default yaml
	BD        string         `yaml:"bd"`
	ID        string         `yaml:"id"`
	Input     map[string]any `yaml:"input"`
	Want      map[string]any `yaml:"want"`
	WantError string         `yaml:"want_error"`
}

// RunContract replays the recorded contract cases in file against decode. Each
// case pins, per universe, either an expected exported-field projection (accept)
// or an expected error kind (reject). The cases were approved by the legacy
// comparison while it existed; RunContract keeps guarding the new decoder forever
// after the legacy constructor is deleted, so the differential proof survives as
// a permanent regression guard without keeping dead production code.
func RunContract(t *testing.T, decode Decoder, file string) {
	t.Helper()

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("schematest: read contract %s: %v", file, err)
	}
	var cf contractFile
	if err := yaml.Unmarshal(raw, &cf); err != nil {
		t.Fatalf("schematest: parse contract %s: %v", file, err)
	}
	if len(cf.Cases) == 0 {
		t.Fatalf("schematest: contract %s has no cases", file)
	}

	for _, c := range cf.Cases {
		c := c
		name := c.Label
		if name == "" {
			name = c.Universe
		}
		t.Run(name, func(t *testing.T) {
			universe := c.Universe
			if universe == "" {
				universe = UniverseYAML
			}
			config, err := contractConfig(universe, c.Input)
			if err != nil {
				t.Fatalf("schematest: contract %s/%s: build config: %v", cf.Module, c.Label, err)
			}

			got, decErr := decode(c.ID, config)

			if c.WantError != "" {
				if decErr == nil {
					t.Fatalf("[%s] %s: expected error %q, got success (%#v)", universe, c.Label, c.WantError, got)
				}
				wantKind, known := parseErrorKind(c.WantError)
				if known {
					var fe *modschema.FieldError
					if !errors.As(decErr, &fe) {
						t.Fatalf("[%s] %s: expected *FieldError(%s), got %T: %v", universe, c.Label, wantKind, decErr, decErr)
					}
					if fe.Kind != wantKind {
						t.Fatalf("[%s] %s: error kind = %s, want %s", universe, c.Label, fe.Kind, wantKind)
					}
				}
				return
			}

			if decErr != nil {
				t.Fatalf("[%s] %s: unexpected error: %v", universe, c.Label, decErr)
			}
			if err := checkProjection(got, c.Want); err != nil {
				t.Errorf("[%s] %s: %v", universe, c.Label, err)
			}
		})
	}
}

// contractConfig builds the per-universe config from a case's recorded input.
func contractConfig(universe string, input map[string]any) (map[string]any, error) {
	if input == nil {
		input = map[string]any{}
	}
	switch universe {
	case UniverseYAML:
		return cloneMap(input), nil
	case UniverseMsgpack:
		return msgpackMap(input)
	case UniverseCLI:
		out := map[string]any{}
		// The contract replay is a fixed, recorded regression guard; a
		// non-scalar input simply has no CLI token (skips reported only in the
		// live Equivalence path, which carries a *testing.T).
		tokens, _ := deriveCLITokens(input)
		cliargs.ParseKeyValues(tokens, out)
		return out, nil
	default:
		return nil, fmt.Errorf("unknown universe %q", universe)
	}
}

// msgpackMap round-trips a config map through bus.Encode/Decode.
func msgpackMap(m map[string]any) (map[string]any, error) {
	data, err := bus.Encode(m)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := bus.Decode(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// checkProjection compares a decoded value's exported fields against want. Missing
// or extra keys and value mismatches are reported. Numeric comparisons are
// kind-tolerant so a want of `3` matches an int/int64/uint field.
func checkProjection(got any, want map[string]any) error {
	if want == nil {
		return nil
	}
	proj := projectFields(got)
	for k, wv := range want {
		gv, ok := proj[k]
		if !ok {
			return fmt.Errorf("projection missing field %q (have %v)", k, sortedKeys(proj))
		}
		if !looseEqual(gv, wv) {
			return fmt.Errorf("field %q = %#v, want %#v", k, gv, wv)
		}
	}
	return nil
}

// projectFields reflects a struct (or pointer to one) into a name→value map of
// its exported fields.
func projectFields(v any) map[string]any {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return map[string]any{}
		}
		rv = rv.Elem()
	}
	out := map[string]any{}
	if rv.Kind() != reflect.Struct {
		return out
	}
	rt := rv.Type()
	for i := range rt.NumField() {
		sf := rt.Field(i)
		if sf.PkgPath != "" {
			continue // unexported
		}
		out[sf.Name] = rv.Field(i).Interface()
	}
	return out
}

// looseEqual compares two values, treating all integer kinds as equal by value
// and all float kinds likewise, and otherwise falling back to reflect.DeepEqual.
// A decoded semantic value whose Go type differs from its YAML-native literal —
// paramtypes.TriState (no scalar literal at all), paramtypes.StringList (a
// []string that YAML parses into a []any), paramtypes.TemplateFlag (a
// bool/"jinja"/truthy-string flag), and paramtypes.FileMode (an octal
// string/int) — is compared through a dedicated matcher so a contract's `want`
// can pin it in plain YAML (§3 semantic-type pilot).
func looseEqual(a, b any) bool {
	if ts, ok := a.(paramtypes.TriState); ok {
		return triStateMatches(ts, b)
	}
	if sl, ok := a.(paramtypes.StringList); ok {
		return stringListMatches(sl, b)
	}
	if sm, ok := a.(paramtypes.StringMap); ok {
		return stringMapMatches(sm, b)
	}
	if tf, ok := a.(paramtypes.TemplateFlag); ok {
		return templateFlagMatches(tf, b)
	}
	if fm, ok := a.(paramtypes.FileMode); ok {
		return fileModeMatches(fm, b)
	}
	if ai, aok := asInt64(a); aok {
		if bi, bok := asInt64(b); bok {
			return ai == bi
		}
	}
	if af, aok := asFloat64(a); aok {
		if bf, bok := asFloat64(b); bok {
			return af == bf
		}
	}
	return reflect.DeepEqual(a, b)
}

// triStateMatches compares a decoded paramtypes.TriState against a contract's
// YAML-expressed want. want may be a bool (the TriState must be Declared() with
// that value) or a map carrying "declared" (bool, default true) and optionally
// "value" (bool) — the map form makes an undeclared TriState expressible in a
// fixture as `{declared: false}`, which no bare scalar can spell.
func triStateMatches(ts paramtypes.TriState, want any) bool {
	switch w := want.(type) {
	case bool:
		return ts.Declared() && ts.Value() == w
	case map[string]any:
		declared := true
		if dv, ok := w["declared"]; ok {
			db, ok := dv.(bool)
			if !ok {
				return false
			}
			declared = db
		}
		if ts.Declared() != declared {
			return false
		}
		if !declared {
			return true
		}
		vv, ok := w["value"]
		if !ok {
			return false
		}
		vb, ok := vv.(bool)
		return ok && ts.Value() == vb
	default:
		return false
	}
}

// stringListMatches compares a decoded paramtypes.StringList against a contract's
// YAML-expressed want. yaml.v3 parses a fixture's `want` list into a []any of
// scalars (a bare scalar becomes a one-element list), so each element is compared
// by its string form — `want: [dev, "123"]` matches whether YAML parsed an
// element as a string or an int, which is exactly StringList's own scalar-sprint
// contract (BD-5). A non-list, non-scalar want cannot match.
func stringListMatches(sl paramtypes.StringList, want any) bool {
	var wantElems []any
	switch w := want.(type) {
	case []any:
		wantElems = w
	case string:
		wantElems = []any{w}
	default:
		return false
	}
	if len(sl) != len(wantElems) {
		return false
	}
	for i := range sl {
		if sl[i] != fmt.Sprint(wantElems[i]) {
			return false
		}
	}
	return true
}

// stringMapMatches compares a decoded paramtypes.StringMap against a contract's
// YAML-expressed want. yaml.v3 parses a fixture's `want` map into a
// map[string]any of scalars, so each value is compared by its string form —
// `want: {A: "1", B: 2}` matches whether YAML parsed a value as a string or an
// int, which is exactly StringMap's own scalar-sprint contract. A non-map want
// cannot match. (StringMap is the file.keyvalue key_values type; it is the third
// semantic type — after StringList and TemplateFlag/FileMode — to gain a
// contract matcher as its first migrated consumer lands.)
func stringMapMatches(sm paramtypes.StringMap, want any) bool {
	wm, ok := want.(map[string]any)
	if !ok {
		return false
	}
	if len(sm) != len(wm) {
		return false
	}
	for k, wv := range wm {
		v, ok := sm[k]
		if !ok || v != fmt.Sprint(wv) {
			return false
		}
	}
	return true
}

// templateFlagMatches compares a decoded paramtypes.TemplateFlag against a
// contract's YAML-expressed want. want may be a bool (the flag must be
// Declared() with that Enabled() value) or a map carrying "declared" (bool,
// default true) — the map form `{declared: false}` expresses an undeclared flag,
// which no bare scalar can spell. It mirrors triStateMatches.
func templateFlagMatches(tf paramtypes.TemplateFlag, want any) bool {
	switch w := want.(type) {
	case bool:
		return tf.Declared() && tf.Enabled() == w
	case map[string]any:
		declared := true
		if dv, ok := w["declared"]; ok {
			db, ok := dv.(bool)
			if !ok {
				return false
			}
			declared = db
		}
		if tf.Declared() != declared {
			return false
		}
		if !declared {
			return true
		}
		vv, ok := w["value"]
		if !ok {
			return false
		}
		vb, ok := vv.(bool)
		return ok && tf.Enabled() == vb
	default:
		return false
	}
}

// fileModeMatches compares a decoded paramtypes.FileMode against a contract's
// YAML-expressed want. want may be an octal string ("0644", "4755") or an octal
// integer (a YAML `mode: 0644` literal) — decoded through FileMode's OWN sealed
// decoder so the matcher never re-implements the octal/special-bit parsing — and
// the two modes are compared on the managed facets via FileMode.Equal. A map
// `{declared: false}` expresses the undeclared (lazy-default) FileMode, which no
// scalar can spell.
func fileModeMatches(fm paramtypes.FileMode, want any) bool {
	if m, ok := want.(map[string]any); ok {
		if dv, ok := m["declared"]; ok {
			if db, ok := dv.(bool); ok {
				return fm.Declared() == db && !db
			}
		}
		return false
	}
	st, ok := paramtypes.ForGoType(reflect.TypeOf(paramtypes.FileMode{}))
	if !ok {
		return false
	}
	out, err := st.Decode(paramtypes.Input{Raw: want})
	if err != nil {
		return false
	}
	wm, ok := out.(paramtypes.FileMode)
	if !ok {
		return false
	}
	return fm.Declared() && fm.Equal(wm.Mode())
}

func asInt64(v any) (int64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(rv.Uint()), true
	default:
		return 0, false
	}
}

func asFloat64(v any) (float64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	default:
		return 0, false
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// parseErrorKind maps a contract's want_error string to a modschema.ErrorKind.
// It reports known=false for an unrecognized token (the case then only requires
// that some error occurred).
func parseErrorKind(s string) (modschema.ErrorKind, bool) {
	switch s {
	case "missing_required":
		return modschema.ErrMissingRequired, true
	case "wrong_type":
		return modschema.ErrWrongType, true
	case "value_invalid":
		return modschema.ErrValueInvalid, true
	default:
		return modschema.ErrOther, false
	}
}
