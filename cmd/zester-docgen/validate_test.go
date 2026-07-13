package main

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

func TestValidateModuleExamples_PkgRemoved(t *testing.T) {
	reg := buildStateRegistry()
	mi, ok := reg.Describe("pkg.removed")
	if !ok {
		t.Fatal("pkg.removed has no registered spec")
	}
	skips, err := validateModuleExamples(reg, nil, mi.Module, mi.Doc.Examples, sensitiveExampleKeys(mi.Params))
	if err != nil {
		t.Fatalf("validateModuleExamples: %v", err)
	}
	// The state example ("remove a package by name") is self-contained and
	// non-templated: it must NOT be skipped. The "remove-old-client" example
	// (F5) references an external requisite (require: cmd.run:stop-service),
	// so it decodes fine but is recorded-skipped for the stronger
	// Registry.Build check. The cli example must be recorded-skipped (it
	// isn't YAML).
	if len(skips) != 2 {
		t.Fatalf("skips = %+v, want exactly 2 (the requisite-example and the cli example)", skips)
	}
	reasons := map[string]bool{}
	for _, s := range skips {
		reasons[s.Reason] = true
	}
	if !reasons["cli example is not YAML"] {
		t.Errorf("skips = %+v, missing the cli-example skip", skips)
	}
	if !reasons["references external requisites, not self-contained"] {
		t.Errorf("skips = %+v, missing the requisite-example skip", skips)
	}
}

// registerTestSpec registers a throwaway single-field spec'd module into a
// fresh registry, for validate.go tests that don't need a real built-in
// module (they exercise example validation, not the built State itself).
func registerTestSpec(t *testing.T, module string, doc modschema.Doc) *state.Registry {
	t.Helper()
	type proto struct {
		Name string `zester:"name,primary" usage:"the name"`
	}
	spec, err := modschema.NewSpec(module, modschema.KindState, proto{}, doc)
	if err != nil {
		t.Fatalf("NewSpec(%s): %v", module, err)
	}
	reg := state.NewRegistry()
	builder := func(id string, config map[string]any) (state.State, error) {
		return nil, nil // never invoked by these tests
	}
	if err := reg.RegisterSpec(spec, builder); err != nil {
		t.Fatalf("RegisterSpec(%s): %v", module, err)
	}
	return reg
}

func TestValidateModuleExamples_TemplatedExampleIsRecordedSkipped(t *testing.T) {
	doc := modschema.Doc{
		Summary: "demo",
		Examples: []modschema.Example{
			{
				Title: "templated",
				Kind:  "state",
				Code:  "thing-{{ id }}:\n  demo.templated:\n    - name: {{ name }}\n",
			},
		},
	}
	reg := registerTestSpec(t, "demo.templated", doc)
	skips, err := validateModuleExamples(reg, nil, "demo.templated", doc.Examples, nil)
	if err != nil {
		t.Fatalf("validateModuleExamples: %v", err)
	}
	if len(skips) != 1 || skips[0].Reason != "templated example, not self-contained YAML" {
		t.Errorf("skips = %+v", skips)
	}
}

func TestValidateModuleExamples_NonSelfContainedIsRecordedSkipped(t *testing.T) {
	doc := modschema.Doc{
		Summary: "demo",
		Examples: []modschema.Example{
			{
				Title: "with requisite",
				Kind:  "state",
				Code: "thing:\n  demo.needsreq:\n    - name: widget\n    - require:\n" +
					"      - \"pkg.installed:nginx\"\n",
			},
		},
	}
	reg := registerTestSpec(t, "demo.needsreq", doc)
	skips, err := validateModuleExamples(reg, nil, "demo.needsreq", doc.Examples, nil)
	if err != nil {
		t.Fatalf("validateModuleExamples: %v", err)
	}
	if len(skips) != 1 || skips[0].Reason != "references external requisites, not self-contained" {
		t.Errorf("skips = %+v", skips)
	}
}

func TestValidateModuleExamples_DecodeFailureFailsGeneration(t *testing.T) {
	type badProto struct {
		Name  string `zester:"name,primary" usage:"the name"`
		Count int    `zester:"count,required" usage:"a required non-primary field"`
	}
	doc := modschema.Doc{
		Summary: "demo",
		Examples: []modschema.Example{
			{
				Title: "missing required field",
				Kind:  "state",
				Code:  "thing:\n  demo.badexample:\n    - name: widget\n", // "count" is required but absent
			},
		},
	}
	spec, err := modschema.NewSpec("demo.badexample", modschema.KindState, badProto{}, doc)
	if err != nil {
		t.Fatal(err)
	}
	reg := state.NewRegistry()
	builder := func(id string, config map[string]any) (state.State, error) { return nil, nil }
	if err := reg.RegisterSpec(spec, builder); err != nil {
		t.Fatal(err)
	}
	if _, err := validateModuleExamples(reg, nil, "demo.badexample", doc.Examples, nil); err == nil {
		t.Fatal("expected a decode failure to fail generation")
	}
}

func TestValidateModuleExamples_UnknownExampleKindErrors(t *testing.T) {
	doc := modschema.Doc{
		Summary:  "demo",
		Examples: []modschema.Example{{Title: "bad", Kind: "yaml", Code: "x: 1"}},
	}
	reg := registerTestSpec(t, "demo.badkind", doc)
	if _, err := validateModuleExamples(reg, nil, "demo.badkind", doc.Examples, nil); err == nil {
		t.Fatal("expected an error for an unknown example kind")
	}
}

func TestValidateNonStateExamples_CliOnlyRecordedSkipped(t *testing.T) {
	examples := []modschema.Example{
		{Title: "run it", Kind: "cli", Code: "zester --direct '*' test.echo hello"},
	}
	skips, err := validateNonStateExamples("test.echo", examples, nil)
	if err != nil {
		t.Fatalf("validateNonStateExamples: %v", err)
	}
	if len(skips) != 1 || skips[0].Reason != "cli example is not YAML" {
		t.Errorf("skips = %+v", skips)
	}
}

func TestValidateNonStateExamples_StateKindErrors(t *testing.T) {
	examples := []modschema.Example{
		{Title: "bad", Kind: "state", Code: "thing:\n  test.echo:\n    - name: hello\n"},
	}
	if _, err := validateNonStateExamples("test.echo", examples, nil); err == nil {
		t.Fatal("expected an error for a \"state\"-kind example on a phase-less module")
	}
}

// sensitiveProto is the fake sensitive-param spec (F2) shared by the
// sensitive-example tests below: a "password" field marked sensitive, so
// example validation can be exercised against a real compiled Spec/ModuleInfo
// without needing a real built-in module that has one yet (pkg.removed has
// none — the framework inventory's user.present.password hasn't migrated).
type sensitiveProto struct {
	Name     string `zester:"name,primary" usage:"the name"`
	Password string `zester:"password,sensitive" usage:"the password"`
}

func registerSensitiveSpec(t *testing.T, doc modschema.Doc) (*state.Registry, modschema.ModuleInfo) {
	t.Helper()
	spec, err := modschema.NewSpec("demo.sensitive", modschema.KindState, sensitiveProto{}, doc)
	if err != nil {
		t.Fatalf("NewSpec(demo.sensitive): %v", err)
	}
	reg := state.NewRegistry()
	builder := func(id string, config map[string]any) (state.State, error) { return nil, nil }
	if err := reg.RegisterSpec(spec, builder); err != nil {
		t.Fatalf("RegisterSpec(demo.sensitive): %v", err)
	}
	return reg, spec.Info()
}

// TestValidateModuleExamples_SensitiveParamInStateExampleErrors is F2's
// missing half of §8's "sensitive params never appear in examples" — a state
// example that sets a sensitive-marked parameter's YAML key is a content bug
// and must fail generation, not merely decode successfully.
func TestValidateModuleExamples_SensitiveParamInStateExampleErrors(t *testing.T) {
	doc := modschema.Doc{
		Summary: "demo",
		Examples: []modschema.Example{
			{
				Title: "leaks a password",
				Kind:  "state",
				Code:  "thing:\n  demo.sensitive:\n    - name: widget\n    - password: hunter2\n",
			},
		},
	}
	reg, mi := registerSensitiveSpec(t, doc)
	if _, err := validateModuleExamples(reg, nil, mi.Module, doc.Examples, sensitiveExampleKeys(mi.Params)); err == nil {
		t.Fatal("expected an error for a sensitive parameter set in a state example")
	}
}

// TestValidateModuleExamples_SensitiveParamInCLIExampleErrors covers the other
// representation named in F2: a cli example's key=value token naming a
// sensitive parameter must also fail generation.
func TestValidateModuleExamples_SensitiveParamInCLIExampleErrors(t *testing.T) {
	doc := modschema.Doc{
		Summary: "demo",
		Examples: []modschema.Example{
			{
				Title: "leaks a password",
				Kind:  "cli",
				Code:  "zester '*' demo.sensitive name=widget password=hunter2",
			},
		},
	}
	reg, mi := registerSensitiveSpec(t, doc)
	if _, err := validateModuleExamples(reg, nil, mi.Module, doc.Examples, sensitiveExampleKeys(mi.Params)); err == nil {
		t.Fatal("expected an error for a sensitive parameter set in a cli example")
	}
}

// TestValidateNonStateExamples_SensitiveParamErrors covers the exec/dispatch
// (phase-less) surface: every example there is "cli", so the same key=value
// sensitive check must apply.
func TestValidateNonStateExamples_SensitiveParamErrors(t *testing.T) {
	examples := []modschema.Example{
		{Title: "leaks a token", Kind: "cli", Code: "zester --direct '*' demo.exec token=s3cr3t"},
	}
	sensitive := map[string]struct{}{"token": {}}
	if _, err := validateNonStateExamples("demo.exec", examples, sensitive); err == nil {
		t.Fatal("expected an error for a sensitive parameter set in a cli example")
	}
}

func TestSensitiveExampleKeys_IncludesAliases(t *testing.T) {
	params := []modschema.Field{
		{Name: "password", Aliases: []string{"pass", "pw"}, Sensitive: true},
		{Name: "name", Sensitive: false},
	}
	keys := sensitiveExampleKeys(params)
	for _, want := range []string{"password", "pass", "pw"} {
		if _, ok := keys[want]; !ok {
			t.Errorf("sensitiveExampleKeys missing %q", want)
		}
	}
	if _, ok := keys["name"]; ok {
		t.Error("sensitiveExampleKeys must not include a non-sensitive field's name")
	}
}
