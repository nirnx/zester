package modschema_test

import (
	"errors"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
)

type specProto struct {
	Name    string              `zester:"name,primary" usage:"the name"`
	Enabled paramtypes.TriState `zester:"enabled" usage:"toggle"`
}

type dupSemProto struct {
	Name string              `zester:"name,primary" usage:"the name"`
	A    paramtypes.TriState `zester:"a" usage:"first flag"`
	B    paramtypes.TriState `zester:"b" usage:"second flag"`
}

func mustNewSpec(t *testing.T, proto any, doc modschema.Doc) *modschema.Spec {
	t.Helper()
	s, err := modschema.NewSpec("my.mod", modschema.KindState, proto, doc)
	if err != nil {
		t.Fatalf("NewSpec: %v", err)
	}
	return s
}

func TestNewSpec_FieldsPopulated(t *testing.T) {
	doc := modschema.Doc{Summary: "a module", Description: "does things"}
	s := mustNewSpec(t, specProto{}, doc)

	if s.Module != "my.mod" {
		t.Errorf("Module = %q, want my.mod", s.Module)
	}
	if s.Kind != modschema.KindState {
		t.Errorf("Kind = %q", s.Kind)
	}
	if s.Doc.Summary != "a module" {
		t.Errorf("Doc.Summary = %q", s.Doc.Summary)
	}
	if s.Params == nil || len(s.Params.Fields) != 2 {
		t.Fatalf("Params: got %+v", s.Params)
	}
	if s.OpenParams {
		t.Error("OpenParams should default false")
	}
}

func TestNewSpec_EmptyModuleRejected(t *testing.T) {
	if _, err := modschema.NewSpec("", modschema.KindState, specProto{}, modschema.Doc{}); err == nil {
		t.Fatal("expected error for empty module name")
	}
}

func TestNewSpec_CompileErrorPropagates(t *testing.T) {
	// Two primaries is a compile error; NewSpec must surface it.
	type twoPrimary struct {
		A string `zester:"a,primary"`
		B string `zester:"b,primary"`
	}
	if _, err := modschema.NewSpec("bad.mod", modschema.KindState, twoPrimary{}, modschema.Doc{}); err == nil {
		t.Fatal("expected compile error for two primaries")
	}
}

func TestSpec_NewParams_FreshInstances(t *testing.T) {
	s := mustNewSpec(t, specProto{}, modschema.Doc{})
	p1 := s.NewParams()
	p2 := s.NewParams()
	if p1 == p2 {
		t.Fatal("NewParams must return distinct instances")
	}
	if _, ok := p1.(*specProto); !ok {
		t.Fatalf("NewParams returned %T, want *specProto", p1)
	}
}

func TestSpec_Decode_ExecutesPlan(t *testing.T) {
	s := mustNewSpec(t, specProto{}, modschema.Doc{})
	var p specProto
	rep, err := s.Decode("web", map[string]any{"name": "nginx", "enabled": true}, &p, modschema.DecodeOptions{})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if rep == nil {
		t.Fatal("nil report on success")
	}
	if p.Name != "nginx" {
		t.Errorf("Name = %q", p.Name)
	}
	if !p.Enabled.Declared() || !p.Enabled.Value() {
		t.Errorf("Enabled = %+v, want declared true", p.Enabled)
	}
}

func TestSpec_Decode_PrimaryFallsBackToID(t *testing.T) {
	s := mustNewSpec(t, specProto{}, modschema.Doc{})
	var p specProto
	if _, err := s.Decode("nginx", map[string]any{}, &p, modschema.DecodeOptions{}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if p.Name != "nginx" {
		t.Errorf("Name = %q, want id fallback nginx", p.Name)
	}
}

func TestSpec_Decode_FieldErrorCarriesModule(t *testing.T) {
	s := mustNewSpec(t, specProto{}, modschema.Doc{})
	var p specProto
	// A composite value for the string primary is a wrong-type error.
	_, err := s.Decode("web", map[string]any{"name": []any{"x"}}, &p, modschema.DecodeOptions{})
	if err == nil {
		t.Fatal("expected a decode error")
	}
	var fe *modschema.FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("expected *FieldError, got %T", err)
	}
	if fe.Module != "my.mod" {
		t.Errorf("FieldError.Module = %q, want my.mod", fe.Module)
	}
}

func TestSpec_OpenParams_SkipsUnknownKeyValidation(t *testing.T) {
	s := mustNewSpec(t, specProto{}, modschema.Doc{})

	// Baseline: with PolicyError an unknown key fails.
	var p specProto
	_, err := s.Decode("web", map[string]any{"name": "nginx", "bogus": "x"}, &p, modschema.DecodeOptions{Unknown: modschema.PolicyError})
	if err == nil {
		t.Fatal("expected PolicyError to reject an unknown key")
	}

	// OpenParams forces PolicyIgnore regardless of the caller's policy.
	s.OpenParams = true
	var q specProto
	if _, err := s.Decode("web", map[string]any{"name": "nginx", "bogus": "x"}, &q, modschema.DecodeOptions{Unknown: modschema.PolicyError}); err != nil {
		t.Fatalf("OpenParams should skip unknown-key validation, got %v", err)
	}
	if q.Name != "nginx" {
		t.Errorf("Name = %q", q.Name)
	}
}

func TestSpec_Info_ProjectsPlan(t *testing.T) {
	doc := modschema.Doc{Summary: "s", Effects: modschema.Effects{Check: "c", Apply: "a"}}
	s := mustNewSpec(t, specProto{}, doc)
	mi := s.Info()

	if mi.Module != "my.mod" || mi.Kind != modschema.KindState {
		t.Errorf("Info Module/Kind = %q/%q", mi.Module, mi.Kind)
	}
	if !mi.HasSpec {
		t.Error("Info.HasSpec should be true")
	}
	if mi.AlsoExecmod {
		t.Error("Info.AlsoExecmod should default false")
	}
	if len(mi.Params) != 2 {
		t.Fatalf("Info.Params len = %d", len(mi.Params))
	}
	if len(mi.SemTypes) != 1 || mi.SemTypes[0].Name != "TriState" {
		t.Fatalf("Info.SemTypes = %+v", mi.SemTypes)
	}
	if mi.SemTypes[0].Doc == "" || mi.SemTypes[0].JSONSchema == nil {
		t.Errorf("SemTypeInfo missing Doc/JSONSchema: %+v", mi.SemTypes[0])
	}
	if mi.Doc.Effects.Check != "c" {
		t.Errorf("Info.Doc.Effects.Check = %q", mi.Doc.Effects.Check)
	}
}

func TestSpec_Info_DedupesSemTypes(t *testing.T) {
	s := mustNewSpec(t, dupSemProto{}, modschema.Doc{})
	mi := s.Info()
	if len(mi.SemTypes) != 1 {
		t.Fatalf("two fields of one semantic type must dedupe to 1 SemType, got %d: %+v", len(mi.SemTypes), mi.SemTypes)
	}
}

// TestInfoAndSchemaAreDeepCopies pins review-round-4 item 6: the compiled plan
// is shared, long-lived state (registries, docgen, sys.doc render
// concurrently), so Info() must hand out fully detached values — mutating any
// part of a returned view (param fields, alias slices, JSON-Schema fragments,
// doc slices, semantic-type fragments) must not leak into the next call's
// result.
func TestInfoAndSchemaAreDeepCopies(t *testing.T) {
	callerDoc := modschema.Doc{
		Summary:  "demo",
		Examples: []modschema.Example{{Title: "t", Kind: "cli", Code: "c"}},
		Notes:    []modschema.Note{{Level: "info", Body: "b"}},
		SeeAlso:  []string{"other.module"},
	}
	spec, err := modschema.NewSpec("demo.copy", modschema.KindState, specProto{}, callerDoc)
	if err != nil {
		t.Fatal(err)
	}

	// Round 5: the INPUT boundary is sealed too — the caller retains its doc
	// and mutates it after registration; the spec must not see it.
	callerDoc.Examples[0].Title = "CALLER-VANDALIZED"
	callerDoc.SeeAlso[0] = "CALLER-VANDALIZED"

	// Round 5: Spec.Params is a detached snapshot, not an alias of the plan.
	for i := range spec.Params.Fields {
		spec.Params.Fields[i].Usage = "PARAMS-VANDALIZED"
		for k := range spec.Params.Fields[i].JSONSchema {
			spec.Params.Fields[i].JSONSchema[k] = "PARAMS-VANDALIZED"
		}
	}
	spec.Params.Doc.Summary = "PARAMS-VANDALIZED"

	mi := spec.Info()
	if mi.Doc.Examples[0].Title != "t" || mi.Doc.SeeAlso[0] != "other.module" {
		t.Fatalf("caller-retained doc mutation reached the spec: %+v", mi.Doc)
	}
	if mi.Doc.Summary != "demo" {
		t.Fatalf("Params snapshot mutation reached the plan doc: %q", mi.Doc.Summary)
	}
	for _, f := range mi.Params {
		if f.Usage == "PARAMS-VANDALIZED" {
			t.Fatalf("Params snapshot mutation reached the plan (field %q)", f.Name)
		}
		for k, v := range f.JSONSchema {
			if v == "PARAMS-VANDALIZED" {
				t.Fatalf("Params snapshot mutation reached the plan (field %q key %q)", f.Name, k)
			}
		}
	}
	// Vandalize every mutable region of the returned view.
	mi.Doc.Examples[0].Title = "VANDALIZED"
	mi.Doc.Notes[0].Body = "VANDALIZED"
	mi.Doc.SeeAlso[0] = "VANDALIZED"
	for i := range mi.Params {
		mi.Params[i].Usage = "VANDALIZED"
		for k := range mi.Params[i].JSONSchema {
			mi.Params[i].JSONSchema[k] = "VANDALIZED"
		}
		mi.Params[i].JSONSchema["injected"] = true
		if len(mi.Params[i].Aliases) > 0 {
			mi.Params[i].Aliases[0] = "VANDALIZED"
		}
	}
	for i := range mi.SemTypes {
		for k := range mi.SemTypes[i].JSONSchema {
			mi.SemTypes[i].JSONSchema[k] = "VANDALIZED"
		}
	}

	fresh := spec.Info()
	if fresh.Doc.Examples[0].Title != "t" || fresh.Doc.Notes[0].Body != "b" || fresh.Doc.SeeAlso[0] != "other.module" {
		t.Fatalf("Doc mutated through a returned Info view: %+v", fresh.Doc)
	}
	for _, f := range fresh.Params {
		if f.Usage == "VANDALIZED" {
			t.Fatalf("param %q usage mutated through a returned Info view", f.Name)
		}
		if _, injected := f.JSONSchema["injected"]; injected {
			t.Fatalf("param %q JSON-Schema fragment mutated through a returned Info view", f.Name)
		}
		for k, v := range f.JSONSchema {
			if v == "VANDALIZED" {
				t.Fatalf("param %q JSON-Schema key %q mutated through a returned Info view", f.Name, k)
			}
		}
	}
	for _, st := range fresh.SemTypes {
		for k, v := range st.JSONSchema {
			if v == "VANDALIZED" {
				t.Fatalf("semantic type %q fragment key %q mutated through a returned Info view", st.Name, k)
			}
		}
	}
}
