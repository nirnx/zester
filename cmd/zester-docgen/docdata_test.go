package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
)

func TestRenderDocdataJSON_OnlySpecdModules(t *testing.T) {
	reg := buildStateRegistry()
	mi, ok := reg.Describe("pkg.removed")
	if !ok {
		t.Fatal("pkg.removed spec not found")
	}
	raw, err := renderDocdataJSON([]modschema.ModuleInfo{mi})
	if err != nil {
		t.Fatalf("renderDocdataJSON: %v", err)
	}
	var out []modschema.ModuleInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal docdata: %v", err)
	}
	if len(out) != 1 || out[0].Module != "pkg.removed" {
		t.Fatalf("docdata = %+v, want exactly [pkg.removed]", out)
	}
}

func TestRenderDocdataJSON_RoundTripsExactly(t *testing.T) {
	reg := buildStateRegistry()
	mi, _ := reg.Describe("pkg.removed")
	raw, err := renderDocdataJSON([]modschema.ModuleInfo{mi})
	if err != nil {
		t.Fatal(err)
	}
	var out []modschema.ModuleInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out[0], mi) {
		t.Errorf("round-tripped ModuleInfo differs:\n got %+v\nwant %+v", out[0], mi)
	}
	if modschema.RenderText(out[0]) != modschema.RenderText(mi) {
		t.Error("round-tripped ModuleInfo renders differently from the original")
	}
}

func TestRenderDocdataJSON_Deterministic(t *testing.T) {
	reg := buildStateRegistry()
	mi, _ := reg.Describe("pkg.removed")
	first, err := renderDocdataJSON([]modschema.ModuleInfo{mi})
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderDocdataJSON([]modschema.ModuleInfo{mi})
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("renderDocdataJSON is not deterministic")
	}
}

// TestRenderDocdataJSON_ZeroModulesEmitsEmptyArrayNeverNull is F3's docdata
// empty-set edge: renderDocdataJSON must emit the JSON literal "[]" for zero
// spec-carrying modules, never "null" — a `var specd []modschema.ModuleInfo`
// left at its nil zero value encodes as "null", which pkg/moduledoc's
// json.Unmarshal into a []modschema.ModuleInfo happens to tolerate today, but
// the artifact must commit to the one shape it documents (a JSON array)
// regardless of population, not rely on a lenient decoder downstream.
func TestRenderDocdataJSON_ZeroModulesEmitsEmptyArrayNeverNull(t *testing.T) {
	for _, infos := range [][]modschema.ModuleInfo{nil, {}, {{Module: "file.managed", HasSpec: false}}} {
		raw, err := renderDocdataJSON(infos)
		if err != nil {
			t.Fatalf("renderDocdataJSON(%#v): %v", infos, err)
		}
		if string(raw) != "[]" {
			t.Errorf("renderDocdataJSON(%#v) = %q, want the literal \"[]\" (never \"null\")", infos, raw)
		}
	}
}

func TestRenderDocdataJSON_ExcludesNonSpecModules(t *testing.T) {
	specd := modschema.ModuleInfo{Module: "pkg.removed", HasSpec: true}
	legacy := modschema.ModuleInfo{Module: "file.managed", HasSpec: false}
	raw, err := renderDocdataJSON([]modschema.ModuleInfo{legacy, specd})
	if err != nil {
		t.Fatal(err)
	}
	var out []modschema.ModuleInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Module != "pkg.removed" {
		t.Errorf("docdata = %+v, want only the spec-carrying module", out)
	}
}
