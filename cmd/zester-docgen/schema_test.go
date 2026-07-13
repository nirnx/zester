package main

// Schema artifact tests. jsonschema/v6 is a test-only dependency
// (maintainer-approved 2026-07-12, keystone spec §9.4) — used here, in a
// _test.go file, to prove the emitted artifact is actually a valid,
// functioning draft 2020-12 schema; cmd/zester-docgen's production code path
// never imports it.

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func compileArtifact(t *testing.T, raw []byte) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("unmarshal schema artifact: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaID, doc); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	sch, err := c.Compile(schemaID)
	if err != nil {
		t.Fatalf("compile schema artifact: %v", err)
	}
	return sch
}

func jsonInstance(t *testing.T, v map[string]any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal instance: %v", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("unmarshal instance: %v", err)
	}
	return inst
}

func TestRenderModuleSchemaArtifact_ValidDraft202012(t *testing.T) {
	reg := buildStateRegistry()
	mi, ok := reg.Describe("pkg.removed")
	if !ok {
		t.Fatal("pkg.removed spec not found")
	}
	raw, err := renderModuleSchemaArtifact([]modschema.ModuleInfo{mi})
	if err != nil {
		t.Fatalf("renderModuleSchemaArtifact: %v", err)
	}
	sch := compileArtifact(t, raw)

	valid := map[string]any{
		"remove-telnet": map[string]any{
			"pkg.removed": []any{map[string]any{"name": "telnet"}},
		},
	}
	if err := sch.Validate(jsonInstance(t, valid)); err != nil {
		t.Errorf("expected valid pkg.removed instance to pass: %v", err)
	}

	// A NUMERIC name is a valid representation (the runtime sprints scalars
	// into string params, BD-6) — the representation-faithful schema accepts
	// it. A COMPOSITE name is what both the runtime (wrong_type) and the
	// schema reject.
	invalidType := map[string]any{
		"remove-telnet": map[string]any{
			"pkg.removed": []any{map[string]any{"name": []any{"a", "b"}}}, // composite: rejected everywhere
		},
	}
	if err := sch.Validate(jsonInstance(t, invalidType)); err == nil {
		t.Error("expected a composite pkg.removed.name to fail validation")
	}

	// A not-yet-migrated module under a different state ID must still be
	// accepted (the "not" fallback branch) — migrating one module must never
	// make the artifact reject configs for the other 46.
	unmigrated := map[string]any{
		"web-config": map[string]any{
			"file.managed": []any{map[string]any{"path": "/etc/nginx.conf"}, map[string]any{"mode": "0644"}},
		},
	}
	if err := sch.Validate(jsonInstance(t, unmigrated)); err != nil {
		t.Errorf("expected an unmigrated module's config to be unconstrained: %v", err)
	}
}

func TestRenderModuleSchemaArtifact_Deterministic(t *testing.T) {
	reg := buildStateRegistry()
	mi, _ := reg.Describe("pkg.removed")
	infos := []modschema.ModuleInfo{mi}
	first, err := renderModuleSchemaArtifact(infos)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderModuleSchemaArtifact(infos)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("renderModuleSchemaArtifact is not deterministic")
	}
}

func TestRenderModuleSchemaArtifact_OnlySpecdModulesAppear(t *testing.T) {
	reg := buildStateRegistry()
	mi, _ := reg.Describe("pkg.removed")
	raw, err := renderModuleSchemaArtifact([]modschema.ModuleInfo{mi})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	defs := doc["$defs"].(map[string]any)["modules"].(map[string]any)
	if len(defs) != 1 {
		t.Errorf("$defs.modules has %d entries, want exactly 1 (pkg.removed): %v", len(defs), defs)
	}
	if _, ok := defs["pkg.removed"]; !ok {
		t.Error("$defs.modules missing pkg.removed")
	}
}

// fakeSpec compiles a throwaway single-field state Spec for schema-artifact
// tests that need a migrated module WITHOUT pulling in a full state.Registry
// (renderModuleSchemaArtifact only ever consumes []modschema.ModuleInfo).
func fakeSpec(t *testing.T, module string, proto any, doc modschema.Doc) modschema.ModuleInfo {
	t.Helper()
	if doc.Effects.Check == "" {
		doc.Effects.Check = "checks something"
	}
	if doc.Effects.Apply == "" {
		doc.Effects.Apply = "applies something"
	}
	spec, err := modschema.NewSpec(module, modschema.KindState, proto, doc)
	if err != nil {
		t.Fatalf("NewSpec(%s): %v", module, err)
	}
	return spec.Info()
}

// TestRenderModuleSchemaArtifact_TwoMigratedModules_OneOfIsExclusive is F1's
// regression test: renderModuleSchemaArtifact's catch-all branch is an allOf
// of PER-NAME exclusions ("NONE of the migrated names is present"), not a
// single {"not":{"required":[allNames]}} ("not ALL of the migrated names are
// present"). The single-not form is only ever wrong once there are >=2
// migrated modules — with exactly one migrated module (as pkg.removed alone
// exercised in TestRenderModuleSchemaArtifact_ValidDraft202012), "not one
// name present" and "not all names present" coincide, so the bug needed two
// fake migrated specs to surface: a valid instance of EITHER real module
// must match its own named branch (and only it — oneOf, not anyOf), and a
// third, unmigrated-only module must match only the catch-all.
func TestRenderModuleSchemaArtifact_TwoMigratedModules_OneOfIsExclusive(t *testing.T) {
	type fakeAProto struct {
		Name string `zester:"name,primary" usage:"a's name"`
	}
	type fakeBProto struct {
		Path string `zester:"path,primary" usage:"b's path"`
	}
	miA := fakeSpec(t, "demo.a", fakeAProto{}, modschema.Doc{Summary: "demo a"})
	miB := fakeSpec(t, "demo.b", fakeBProto{}, modschema.Doc{Summary: "demo b"})

	raw, err := renderModuleSchemaArtifact([]modschema.ModuleInfo{miA, miB})
	if err != nil {
		t.Fatal(err)
	}
	sch := compileArtifact(t, raw)

	validA := map[string]any{"id-a": map[string]any{"demo.a": []any{map[string]any{"name": "x"}}}}
	if err := sch.Validate(jsonInstance(t, validA)); err != nil {
		t.Errorf("F1 regression: a valid demo.a instance must match exactly one oneOf branch: %v", err)
	}
	validB := map[string]any{"id-b": map[string]any{"demo.b": []any{map[string]any{"path": "/y"}}}}
	if err := sch.Validate(jsonInstance(t, validB)); err != nil {
		t.Errorf("F1 regression: a valid demo.b instance must match exactly one oneOf branch: %v", err)
	}
	unmigrated := map[string]any{"id-c": map[string]any{"demo.c": []any{map[string]any{"foo": "bar"}}}}
	if err := sch.Validate(jsonInstance(t, unmigrated)); err != nil {
		t.Errorf("an unmigrated-only module must still match ONLY the catch-all branch: %v", err)
	}
}

// TestRenderModuleSchemaArtifact_ZeroModulesOmitsStateMapConstraint is F3's
// schema-side empty-set edge: with ZERO migrated specs there is nothing to
// constrain, so the state-map value must carry NO oneOf at all — never the
// always-false {"not":{"required":[]}} (an empty "required" is trivially
// satisfied by any object, so its "not" is always false, which would reject
// every state's every module unconditionally). $defs.modules and
// $defs.semanticTypes must still be present, non-null, empty objects.
func TestRenderModuleSchemaArtifact_ZeroModulesOmitsStateMapConstraint(t *testing.T) {
	raw, err := renderModuleSchemaArtifact(nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	addlProps, ok := doc["additionalProperties"].(map[string]any)
	if !ok {
		t.Fatalf("additionalProperties is not an object: %#v", doc["additionalProperties"])
	}
	if _, hasOneOf := addlProps["oneOf"]; hasOneOf {
		t.Errorf("additionalProperties carries a oneOf constraint with zero migrated modules: %v", addlProps)
	}
	defs, ok := doc["$defs"].(map[string]any)
	if !ok {
		t.Fatal("$defs missing")
	}
	moduleDefs, ok := defs["modules"].(map[string]any)
	if !ok || len(moduleDefs) != 0 {
		t.Errorf("$defs.modules = %#v, want a non-null empty object", defs["modules"])
	}
	semTypeDefs, ok := defs["semanticTypes"].(map[string]any)
	if !ok || len(semTypeDefs) != 0 {
		t.Errorf("$defs.semanticTypes = %#v, want a non-null empty object", defs["semanticTypes"])
	}

	// The artifact must still compile and leave every module fully
	// unconstrained (proves the omitted oneOf, not just its absence in the
	// raw JSON, actually behaves as "no constraint" under a real validator).
	sch := compileArtifact(t, raw)
	anyModule := map[string]any{
		"web-config": map[string]any{
			"file.managed": []any{map[string]any{"path": "/etc/nginx.conf"}, map[string]any{"mode": "0644"}},
		},
	}
	if err := sch.Validate(jsonInstance(t, anyModule)); err != nil {
		t.Errorf("zero-migrated-module schema must leave every module unconstrained: %v", err)
	}
}

// TestModuleParamSchema_SemanticTypeRefsSharedDefs is F4's regression test:
// a semantic-typed field's property must be a $ref into the shared
// $defs.semanticTypes entry, not an inlined copy of the type's JSONSchema
// fragment — so the shared $defs entry is load-bearing (actually referenced)
// rather than merely present. Exercised with TriState because it is the
// semantic type 0E's service.running/service.dead conversion will use next;
// this proves the $ref renders AND resolves correctly before that tranche
// depends on it.
func TestModuleParamSchema_SemanticTypeRefsSharedDefs(t *testing.T) {
	type fakeTriStateProto struct {
		Enable paramtypes.TriState `zester:"enable" usage:"enable the thing"`
	}
	mi := fakeSpec(t, "demo.tristate", fakeTriStateProto{}, modschema.Doc{Summary: "demo"})

	raw, err := renderModuleSchemaArtifact([]modschema.ModuleInfo{mi})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	defs := doc["$defs"].(map[string]any)
	moduleDef := defs["modules"].(map[string]any)["demo.tristate"].(map[string]any)
	// The module def models the real .zy shape: an ARRAY of param maps — the
	// per-param properties live on the items schema.
	items := moduleDef["items"].(map[string]any)
	props := items["properties"].(map[string]any)
	enableProp, ok := props["enable"].(map[string]any)
	if !ok {
		t.Fatalf("properties.enable missing or not an object: %#v", props["enable"])
	}
	if got := enableProp["$ref"]; got != "#/$defs/semanticTypes/TriState" {
		t.Errorf("properties.enable.$ref = %v, want \"#/$defs/semanticTypes/TriState\"", got)
	}
	if got := enableProp["description"]; got != "enable the thing" {
		t.Errorf("properties.enable.description = %v, want the field's usage text", got)
	}
	if len(enableProp) != 2 {
		t.Errorf("properties.enable must carry ONLY $ref+description, not an inlined fragment: %#v", enableProp)
	}
	semTypeDefs := defs["semanticTypes"].(map[string]any)
	if _, ok := semTypeDefs["TriState"]; !ok {
		t.Fatal("$defs.semanticTypes.TriState is missing — the $ref has nothing to resolve against")
	}

	// The $ref must be load-bearing: the compiled artifact enforces
	// TriState's real accepted-representation constraints through it.
	sch := compileArtifact(t, raw)
	validBool := map[string]any{"thing": map[string]any{"demo.tristate": []any{map[string]any{"enable": true}}}}
	if err := sch.Validate(jsonInstance(t, validBool)); err != nil {
		t.Errorf("expected a bool enable to validate through the $ref: %v", err)
	}
	validString := map[string]any{"thing": map[string]any{"demo.tristate": []any{map[string]any{"enable": "yes"}}}}
	if err := sch.Validate(jsonInstance(t, validString)); err != nil {
		t.Errorf("expected a truthy-string enable to validate through the $ref: %v", err)
	}
	invalidArray := map[string]any{"thing": map[string]any{"demo.tristate": []any{map[string]any{"enable": []any{1, 2}}}}}
	if err := sch.Validate(jsonInstance(t, invalidArray)); err == nil {
		t.Error("expected an array enable to fail validation (TriState accepts bool/string/integer only)")
	}
}

// TestRenderModuleSchemaArtifact_RequiredRejectsEmptyAndNull pins review
// round 3: the runtime treats an empty-string or null value as ABSENT (source
// fall-through / YAML-null rules), so a required param present with source:
// "" still missing-required errors — the contains clause must demand a
// NON-empty, non-null value, not mere key presence.
func TestRenderModuleSchemaArtifact_RequiredRejectsEmptyAndNull(t *testing.T) {
	type proto struct {
		Name   string `zester:"name,primary" usage:"n"`
		Source string `zester:"source,required" usage:"s"`
	}
	mi := fakeSpec(t, "demo.req", proto{}, modschema.Doc{Summary: "demo"})
	raw, err := renderModuleSchemaArtifact([]modschema.ModuleInfo{mi})
	if err != nil {
		t.Fatal(err)
	}
	sch := compileArtifact(t, raw)

	valid := map[string]any{"id": map[string]any{"demo.req": []any{map[string]any{"source": "/x"}}}}
	if err := sch.Validate(jsonInstance(t, valid)); err != nil {
		t.Errorf("real source value must satisfy required: %v", err)
	}
	empty := map[string]any{"id": map[string]any{"demo.req": []any{map[string]any{"source": ""}}}}
	if err := sch.Validate(jsonInstance(t, empty)); err == nil {
		t.Error("source: \"\" must NOT satisfy required (runtime treats it as absent)")
	}
	null := map[string]any{"id": map[string]any{"demo.req": []any{map[string]any{"source": nil}}}}
	if err := sch.Validate(jsonInstance(t, null)); err == nil {
		t.Error("source: null must NOT satisfy required (runtime treats it as absent)")
	}
	missing := map[string]any{"id": map[string]any{"demo.req": []any{map[string]any{"name": "x"}}}}
	if err := sch.Validate(jsonInstance(t, missing)); err == nil {
		t.Error("an absent source must fail required")
	}
}
