package paramtypes_test

// Fixture ↔ JSON-Schema agreement (keystone spec §9.4 / CI gate 4).
//
// For every semantic type, each ACCEPTING TypeFixture's input must validate
// against the type's emitted JSONSchema(); each REJECTING fixture must fail
// validation WHERE the schema can express the rejection — here, when the input's
// top-level JSON type is not among the schema's accepted types (a structural
// mismatch such as a list fed to FileMode). Value-level rejections (an octal
// digit out of 0-7, a non-0/1 integer) are semantic, not schema-expressible, and
// are not required to fail.
//
// jsonschema/v6 is a test-only dependency (maintainer-approved 2026-07-12); it
// settles as a direct test dependency once `go mod tidy` runs.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	yaml "gopkg.in/yaml.v3"
)

func TestFixtureSchemaAgreement(t *testing.T) {
	for _, st := range paramtypes.All() {
		st := st
		fixtures, ok := typeFixtures[st.Name()]
		if !ok {
			t.Fatalf("no fixtures for type %q", st.Name())
		}
		t.Run(st.Name(), func(t *testing.T) {
			sch := compileSchema(t, st.JSONSchema())
			accepted := acceptedTypes(st.JSONSchema())

			for _, fx := range fixtures {
				fx := fx
				// EMPTY-STRING RULE (§3): an empty string is the framework's
				// "undeclared" sentinel, intercepted BEFORE the type's
				// schema-described coercion. It is intentionally NOT a schema-valid
				// declared value (a group name has minLength 1, an octal mode is
				// non-empty, …), so an accepting empty-string fixture is exempt from
				// fixture↔schema agreement — the schema describes declared values,
				// not the not-provided sentinel.
				if s, ok := fx.YAML.(string); ok && s == "" && !fx.WantErr {
					continue
				}
				input := yamlParse(t, fx.YAML)
				inst := jsonInstance(t, input)
				err := sch.Validate(inst)

				if !fx.WantErr {
					if err != nil {
						t.Errorf("%s/%s: accepting fixture failed schema validation: %v", st.Name(), fx.Label, err)
					}
					continue
				}

				// Rejecting fixture.
				jt := jsonKind(input)
				if !typeCompatible(jt, accepted) {
					// Structurally expressible: the schema MUST reject it.
					if err == nil {
						t.Errorf("%s/%s: rejecting fixture has incompatible JSON type %q but schema accepted it (accepted: %v)", st.Name(), fx.Label, jt, accepted)
					}
					continue
				}
				// Type-compatible. When EVERY schema branch that accepts this JSON
				// type is value-constrained (a keyword beyond "type" — maximum,
				// items, additionalProperties, …), the value has no unconstrained
				// branch to satisfy, so the rejection IS schema-expressible and the
				// schema MUST reject it — a hard assertion, not a tolerant note.
				if schemaExpressibleRejection(st.JSONSchema(), jt) {
					if err == nil {
						t.Errorf("%s/%s: rejecting fixture is schema-expressible (every %q-accepting branch is value-constrained) but schema accepted it", st.Name(), fx.Label, jt)
					}
					continue
				}
				// An unconstrained branch accepts this type: the rejection is
				// purely value-level (semantic), not schema-expressible; note it.
				if err == nil {
					t.Logf("%s/%s: rejecting fixture not schema-expressible (semantic rejection)", st.Name(), fx.Label)
				}
			}
		})
	}
}

// compileSchema JSON-encodes a schema map and compiles it with jsonschema/v6.
func compileSchema(t *testing.T, schemaMap map[string]any) *jsonschema.Schema {
	t.Helper()
	raw, err := json.Marshal(schemaMap)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("mem://schema.json", doc); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	sch, err := c.Compile("mem://schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return sch
}

// yamlParse runs a value through a real yaml.v3 round-trip, matching the
// harness's YAML ingress leg.
func yamlParse(t *testing.T, v any) any {
	t.Helper()
	data, err := yaml.Marshal(v)
	if err != nil {
		t.Fatalf("yaml marshal %#v: %v", v, err)
	}
	var out any
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("yaml unmarshal %q: %v", data, err)
	}
	return out
}

// jsonInstance normalizes a value into the JSON value shape the validator expects
// (numbers become json.Number, preserving integrality).
func jsonInstance(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json marshal %#v: %v", v, err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("json unmarshal %q: %v", raw, err)
	}
	return inst
}

// acceptedTypes collects the JSON "type" values a schema accepts at the top level
// and within its oneOf/anyOf/allOf branches.
func acceptedTypes(schema map[string]any) map[string]bool {
	out := map[string]bool{}
	addSchemaTypes(out, schema)
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		branches, ok := schema[key].([]any)
		if !ok {
			continue
		}
		for _, b := range branches {
			if bm, ok := b.(map[string]any); ok {
				addSchemaTypes(out, bm)
			}
		}
	}
	return out
}

func addSchemaTypes(out map[string]bool, m map[string]any) {
	switch tv := m["type"].(type) {
	case string:
		out[tv] = true
	case []any:
		for _, x := range tv {
			if s, ok := x.(string); ok {
				out[s] = true
			}
		}
	}
}

// jsonKind reports the JSON type name of a yaml-parsed Go value.
func jsonKind(v any) string {
	if v == nil {
		return "null"
	}
	switch reflect.ValueOf(v).Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Map, reflect.Struct:
		return "object"
	default:
		return "unknown"
	}
}

// typeCompatible reports whether a JSON type is among a schema's accepted types,
// treating integer as an acceptable "number" (JSON Schema integers are numbers).
func typeCompatible(jt string, accepted map[string]bool) bool {
	if accepted[jt] {
		return true
	}
	if jt == "integer" && accepted["number"] {
		return true
	}
	return false
}

// typeBranches returns a schema's alternative sub-schemas (its oneOf/anyOf/allOf
// branches), or the schema itself as a single branch when it declares none.
func typeBranches(schema map[string]any) []map[string]any {
	var branches []map[string]any
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		arr, ok := schema[key].([]any)
		if !ok {
			continue
		}
		for _, b := range arr {
			if bm, ok := b.(map[string]any); ok {
				branches = append(branches, bm)
			}
		}
	}
	if len(branches) == 0 {
		return []map[string]any{schema}
	}
	return branches
}

// branchAcceptsType reports whether a schema branch's declared type includes jt,
// treating integer as an acceptable number (mirroring typeCompatible).
func branchAcceptsType(branch map[string]any, jt string) bool {
	types := map[string]bool{}
	addSchemaTypes(types, branch)
	if types[jt] {
		return true
	}
	return jt == "integer" && types["number"]
}

// branchConstrained reports whether a branch carries a value constraint — any
// keyword other than "type" (maximum, minimum, pattern, enum, items,
// additionalProperties, minLength, …).
func branchConstrained(branch map[string]any) bool {
	for k := range branch {
		if k != "type" {
			return true
		}
	}
	return false
}

// schemaExpressibleRejection reports whether a rejecting fixture whose JSON type
// is jt should be caught by the schema's own constraints: at least one branch
// accepts jt and EVERY jt-accepting branch is value-constrained, so the value
// has no unconstrained branch to validate against. When true the schema must
// reject the fixture; when false the rejection is value-level (semantic) only.
func schemaExpressibleRejection(schema map[string]any, jt string) bool {
	matched := 0
	for _, b := range typeBranches(schema) {
		if !branchAcceptsType(b, jt) {
			continue
		}
		matched++
		if !branchConstrained(b) {
			return false
		}
	}
	return matched > 0
}
