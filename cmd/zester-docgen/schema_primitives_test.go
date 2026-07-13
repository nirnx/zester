package main

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
)

// TestSchemaAcceptsEveryRuntimeValidPrimitiveRepresentation is the coverage
// gap the second review round exposed: the fixture<->schema agreement gate
// covered SEMANTIC types only, so a primitive-fragment bug (the oneOf integer
// trap: every JSON integer matched both the integer and integral-number
// branches, and oneOf's exactly-one semantics rejected it) shipped unseen.
// This test pins the invariant directly for primitives: every value the
// runtime decoder ACCEPTS for a bool/int/float/string param must validate
// against the generated artifact, and runtime-REJECTED shapes must fail.
func TestSchemaAcceptsEveryRuntimeValidPrimitiveRepresentation(t *testing.T) {
	type proto struct {
		Flag  bool    `zester:"flag" usage:"a bool"`
		Count int     `zester:"count" usage:"an int"`
		Ratio float64 `zester:"ratio" usage:"a float"`
		Label string  `zester:"label" usage:"a string"`
	}
	mi := fakeSpec(t, "demo.prims", proto{}, modschema.Doc{Summary: "demo"})
	raw, err := renderModuleSchemaArtifact([]modschema.ModuleInfo{mi})
	if err != nil {
		t.Fatal(err)
	}
	sch := compileArtifact(t, raw)

	accept := []map[string]any{
		{"flag": true}, {"flag": 0}, {"flag": 1},
		{"flag": "true"}, {"flag": " TRUE "}, {"flag": "Off"}, {"flag": "  no\t"},
		{"count": 5}, {"count": 5.0}, {"count": "-42"}, {"count": " 42 "},
		{"ratio": 1.5}, {"ratio": 3}, {"ratio": " 2.5 "}, {"ratio": "1e3"},
		// strconv.ParseFloat forms (review round 3): trailing dot, hex float,
		// underscore separators.
		{"ratio": "1."}, {"ratio": "0x1p2"}, {"ratio": "1_0.5"}, {"ratio": ".5"},
		// Round 4: exact ParseFloat grammar — underscores are legal between
		// digits ANYWHERE (exponent included), and 0x may be followed by an
		// underscore before the first hex digit; hex-fraction exponents too.
		{"ratio": "1e1_0"}, {"ratio": "0x_1p2"}, {"ratio": "0x.8p1"}, {"ratio": "0x1.8p-2"},
		{"label": "x"}, {"label": 12345}, {"label": true},
	}
	for _, params := range accept {
		inst := map[string]any{"id": map[string]any{"demo.prims": []any{params}}}
		if err := sch.Validate(jsonInstance(t, inst)); err != nil {
			t.Errorf("runtime-valid %v rejected by the schema: %v", params, err)
		}
	}

	reject := []map[string]any{
		{"flag": 2}, {"flag": "banana"},
		{"count": "0x1f"}, {"count": 1.5}, {"count": []any{1}},
		{"ratio": "not-a-number"},
		// floatValue rejects non-finite results even though ParseFloat parses them.
		{"ratio": "inf"}, {"ratio": "NaN"},
		// Round 4: ParseFloat REJECTS these underscore/hex misuses, so the
		// schema must too — doubled or trailing underscores, a bare hex
		// exponent with no mantissa digits, an underscore next to the dot.
		{"ratio": "1__0"}, {"ratio": "10_"}, {"ratio": "0xp1"}, {"ratio": "1._5"}, {"ratio": "_10"},
		{"label": []any{"a"}},
	}
	for _, params := range reject {
		inst := map[string]any{"id": map[string]any{"demo.prims": []any{params}}}
		if err := sch.Validate(jsonInstance(t, inst)); err == nil {
			t.Errorf("runtime-rejected %v accepted by the schema", params)
		}
	}
}
