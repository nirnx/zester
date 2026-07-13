package schematest_test

// Self-tests for the differential harness. They use a toy proto and a toy legacy
// parser (no real modules) to prove: an equivalence agreement passes; an
// UNDECLARED divergence is reported (must-fail); an IntentionalDiff with an empty
// Changelog is rejected while one with a changelog + a passing Expect suppresses
// the divergence; and the permanent contract runner replays recorded cases.

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	"github.com/nirnx/zester/pkg/modschema/schematest"
)

// ---- toy protos ----

type toyProto struct {
	Name string `zester:"name,primary" usage:"the name"`
	Path string `zester:"path" usage:"a path"`
}

type toyContractProto struct {
	Name  string `zester:"name,primary" usage:"the name"`
	Path  string `zester:"path" usage:"a path"`
	Count int    `zester:"count" usage:"a count"`
}

func mustCompile(t *testing.T, proto any) *modschema.CompiledSchema {
	t.Helper()
	cs, err := modschema.Compile(proto, modschema.Doc{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return cs
}

// ---- RunTypeFixtures smoke test against a real registered type ----

func TestRunTypeFixtures_Smoke(t *testing.T) {
	st, ok := paramtypes.Lookup("TriState")
	if !ok {
		t.Fatal("TriState not registered")
	}
	schematest.RunTypeFixtures(t, st, []schematest.TypeFixture{
		{Label: "bool-true", YAML: true, CLI: "true", Want: paramtypes.NewTriState(true)},
		{Label: "string-yes", YAML: "yes", CLI: "yes", Want: paramtypes.NewTriState(true)},
		{Label: "int-zero", YAML: 0, CLI: "0", Want: paramtypes.NewTriState(false)},
		{Label: "reject", YAML: "maybe", CLI: "maybe", WantErr: true, WantErrKind: modschema.ErrValueInvalid},
	})
}

// ---- Equivalence: agreement passes ----

func TestEquivalence_AgreementPasses(t *testing.T) {
	cs := mustCompile(t, toyProto{})
	decoded := func(id string, c map[string]any) (any, error) {
		var p toyProto
		if _, err := cs.Decode(id, c, &p, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return p, nil
	}
	legacy := func(id string, c map[string]any) (any, error) {
		p := toyProto{}
		p.Name, _ = c["name"].(string)
		if p.Name == "" {
			p.Name = id
		}
		p.Path, _ = c["path"].(string)
		return p, nil
	}
	eq := schematest.Equivalence{
		Module:     "toy",
		ID:         "nginx",
		YAMLSource: "path: /etc/nginx.conf\n",
		Legacy:     legacy,
		Decoded:    decoded,
		Compare: func(l, d any) error {
			if !reflect.DeepEqual(l, d) {
				return fmt.Errorf("%#v != %#v", l, d)
			}
			return nil
		},
	}
	// String-only params agree across yaml/msgpack/cli, so RunEquivalence records
	// no failures.
	schematest.RunEquivalence(t, eq)
	if probs := schematest.CheckEquivalence(eq); len(probs) != 0 {
		t.Fatalf("expected no problems, got %v", probs)
	}
}

// divergingEquivalence returns an Equivalence whose legacy and new decoders never
// agree in any universe (used by the must-fail and diff-contract tests).
func divergingEquivalence() schematest.Equivalence {
	return schematest.Equivalence{
		Module:     "toy",
		ID:         "x",
		YAMLSource: "name: nginx\n",
		Legacy:     func(id string, c map[string]any) (any, error) { return "LEGACY", nil },
		Decoded:    func(id string, c map[string]any) (any, error) { return "NEW", nil },
		Compare: func(l, d any) error {
			if l != d {
				return fmt.Errorf("mismatch %v != %v", l, d)
			}
			return nil
		},
	}
}

// ---- Equivalence: undeclared divergence must fail ----

func TestEquivalence_UndeclaredDivergenceFails(t *testing.T) {
	probs := schematest.CheckEquivalence(divergingEquivalence())
	if len(probs) == 0 {
		t.Fatal("expected undeclared divergence to be reported, got none")
	}
	for _, p := range probs {
		if !strings.Contains(p.Error(), "undeclared divergence") {
			t.Errorf("problem does not mention undeclared divergence: %v", p)
		}
	}
}

// ---- Equivalence: IntentionalDiff changelog is required ----

func TestEquivalence_DiffRequiresChangelog(t *testing.T) {
	eq := divergingEquivalence()
	eq.Diffs = []schematest.IntentionalDiff{{
		BD:        "BD-TEST",
		Universe:  "", // all universes
		Reason:    "toy divergence",
		Changelog: "", // MISSING — must be flagged
		Expect:    func(l, d schematest.DiffOutcome) error { return nil },
	}}
	probs := schematest.CheckEquivalence(eq)
	if len(probs) == 0 {
		t.Fatal("expected empty-changelog violation to be reported")
	}
	found := false
	for _, p := range probs {
		if strings.Contains(p.Error(), "Changelog") {
			found = true
		}
	}
	if !found {
		t.Errorf("no problem mentioned the missing Changelog: %v", probs)
	}
}

// ---- Equivalence: a declared diff with a changelog + passing Expect is clean ----

func TestEquivalence_DeclaredDiffSuppresses(t *testing.T) {
	eq := divergingEquivalence()
	eq.Diffs = []schematest.IntentionalDiff{{
		BD:        "BD-TEST",
		Universe:  "*",
		Reason:    "toy divergence",
		Changelog: "CHANGELOG.md#unreleased",
		Expect: func(l, d schematest.DiffOutcome) error {
			if l.Value == d.Value {
				return fmt.Errorf("expected the values to diverge, both were %v", l.Value)
			}
			return nil
		},
	}}
	if probs := schematest.CheckEquivalence(eq); len(probs) != 0 {
		t.Fatalf("declared diff with changelog + passing Expect should be clean, got %v", probs)
	}
}

// ---- RunContract: replays recorded cases ----

func TestRunContract_Toy(t *testing.T) {
	cs := mustCompile(t, toyContractProto{})
	decode := func(id string, c map[string]any) (any, error) {
		var p toyContractProto
		if _, err := cs.Decode(id, c, &p, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return p, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/toymod.yaml")
}
