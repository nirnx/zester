package state_test

import (
	"os"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// altSpecProto gives the replacement spec a visibly different schema so the
// test can assert Describe serves the NEW spec after ReplaceSpec.
type altSpecProto struct {
	Name  string `zester:"name,primary" usage:"the name"`
	Extra string `zester:"extra" usage:"replacement-only param"`
}

// TestReplaceSpec_InsertAndOverwrite pins the dynamic-registration seam added
// for the Starlark loader: ReplaceSpec inserts like RegisterSpec on first use
// and OVERWRITES spec+builder on re-registration (hot-reload / formula
// override), so Describe always reflects the latest load — where the strict
// RegisterSpec must keep refusing duplicates.
func TestReplaceSpec_InsertAndOverwrite(t *testing.T) {
	r := state.NewRegistry()

	first := newTestSpec(t, "star.mod")
	if err := r.ReplaceSpec(first, testBuilder); err != nil {
		t.Fatalf("initial ReplaceSpec: %v", err)
	}
	if mi, ok := r.Describe("star.mod"); !ok || len(mi.Params) != 1 {
		t.Fatalf("after insert: ok=%v params=%d, want ok with 1 param", ok, len(mi.Params))
	}

	second, err := modschema.NewSpec("star.mod", modschema.KindState, altSpecProto{}, modschema.Doc{Summary: "reloaded"})
	if err != nil {
		t.Fatalf("NewSpec alt: %v", err)
	}
	replacedBuilderRan := false
	if err := r.ReplaceSpec(second, func(id string, cfg map[string]any) (state.State, error) {
		replacedBuilderRan = true
		return testBuilder(id, cfg)
	}); err != nil {
		t.Fatalf("overwriting ReplaceSpec: %v", err)
	}

	mi, ok := r.Describe("star.mod")
	if !ok || mi.Doc.Summary != "reloaded" || len(mi.Params) != 2 {
		t.Fatalf("after replace: ok=%v summary=%q params=%d, want reloaded/2 — Describe must serve the NEW spec", ok, mi.Doc.Summary, len(mi.Params))
	}
	if _, err := r.Build("star.mod", "x", map[string]any{"name": "x"}); err != nil {
		t.Fatalf("Build after replace: %v", err)
	}
	if !replacedBuilderRan {
		t.Fatal("Build after replace ran the OLD builder — ReplaceSpec must overwrite the builder too")
	}

	// The strict path stays strict.
	if err := r.RegisterSpec(newTestSpec(t, "star.mod"), testBuilder); err == nil {
		t.Fatal("RegisterSpec accepted a duplicate — strictness must be unchanged")
	}
}

// TestReplaceSpec_Validation mirrors RegisterSpec's argument validation.
func TestReplaceSpec_Validation(t *testing.T) {
	r := state.NewRegistry()
	if err := r.ReplaceSpec(nil, testBuilder); err == nil {
		t.Fatal("nil spec accepted")
	}
	if err := r.ReplaceSpec(newTestSpec(t, "a.b"), nil); err == nil {
		t.Fatal("nil builder accepted")
	}
}

// TestBuiltinsNeverReplaceSpec is the conformance pin promised in
// ReplaceSpec's doc comment: the built-in registration table must use the
// strict RegisterSpec only. (Source-scan style, like the reserved-key pins.)
func TestBuiltinsNeverReplaceSpec(t *testing.T) {
	src, err := os.ReadFile("modules/register.go")
	if err != nil {
		t.Fatalf("read modules/register.go: %v", err)
	}
	if strings.Contains(string(src), ".ReplaceSpec(") {
		t.Fatal("pkg/state/modules/register.go calls ReplaceSpec — built-ins must register via the strict RegisterSpec only")
	}
}

// TestPlainRegisterClearsStaleSpec pins the review fix: a plain Register is an
// UNDOCUMENTED registration, so it removes any spec previously stored under the
// name — after a Starlark hot-reload whose doc re-capture fails, Describe must
// report not-documented rather than serving the OLD spec for the NEW builder.
func TestPlainRegisterClearsStaleSpec(t *testing.T) {
	r := state.NewRegistry()
	if err := r.ReplaceSpec(newTestSpec(t, "star.mod"), testBuilder); err != nil {
		t.Fatalf("seed spec: %v", err)
	}
	if _, ok := r.Describe("star.mod"); !ok {
		t.Fatal("seed spec not visible")
	}
	r.Register("star.mod", testBuilder) // capture-failure fallback path
	if _, ok := r.Describe("star.mod"); ok {
		t.Fatal("Describe served a stale spec after a plain Register — must be cleared")
	}
	if !r.Has("star.mod") {
		t.Fatal("builder must survive the spec clearing")
	}
}

// TestUnregister pins the removal seam added for Starlark reload
// reconciliation: Unregister removes BOTH the builder and the spec (a removed
// module is neither callable nor documented) and reports prior presence.
func TestUnregister(t *testing.T) {
	r := state.NewRegistry()
	if err := r.ReplaceSpec(newTestSpec(t, "star.mod"), testBuilder); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !r.Unregister("star.mod") {
		t.Fatal("Unregister reported absent for a registered module")
	}
	if r.Has("star.mod") {
		t.Error("builder survived Unregister")
	}
	if _, ok := r.Describe("star.mod"); ok {
		t.Error("spec survived Unregister")
	}
	if _, err := r.Build("star.mod", "x", map[string]any{"name": "x"}); err == nil {
		t.Error("Build still constructs an unregistered module")
	}
	if r.Unregister("star.mod") {
		t.Error("second Unregister reported presence")
	}
	if r.Unregister("never.was") {
		t.Error("Unregister of an unknown name reported presence")
	}
}

// TestBuiltinsNeverUnregister is the conformance pin promised in Unregister's
// doc comment: the built-in registration table must never remove modules.
func TestBuiltinsNeverUnregister(t *testing.T) {
	src, err := os.ReadFile("modules/register.go")
	if err != nil {
		t.Fatalf("read modules/register.go: %v", err)
	}
	if strings.Contains(string(src), ".Unregister(") {
		t.Fatal("pkg/state/modules/register.go calls Unregister — built-ins must never be unregistered")
	}
}
