package state_test

import (
	"context"
	"sync"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

type specTestProto struct {
	Name string `zester:"name,primary" usage:"the name"`
}

// fakeState is a trivial State the test builder returns.
type fakeState struct{ id string }

func (f *fakeState) Name() string           { return "fake:" + f.id }
func (f *fakeState) Reqs() state.Requisites { return state.Requisites{} }
func (f *fakeState) Check(context.Context) (state.CheckResult, error) {
	return state.CheckResult{}, nil
}
func (f *fakeState) Apply(context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{}, nil
}
func (f *fakeState) Revert(context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{}, nil
}

func newTestSpec(t *testing.T, module string) *modschema.Spec {
	t.Helper()
	s, err := modschema.NewSpec(module, modschema.KindState, specTestProto{}, modschema.Doc{Summary: "test"})
	if err != nil {
		t.Fatalf("NewSpec %s: %v", module, err)
	}
	return s
}

func testBuilder(id string, _ map[string]any) (state.State, error) {
	return &fakeState{id: id}, nil
}

func TestRegisterSpec_RegistersBothPaths(t *testing.T) {
	r := state.NewRegistry()
	spec := newTestSpec(t, "demo.mod")
	if err := r.RegisterSpec(spec, testBuilder); err != nil {
		t.Fatalf("RegisterSpec: %v", err)
	}

	// Build path works.
	st, err := r.Build("demo.mod", "x", map[string]any{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if st.Name() != "fake:x" {
		t.Errorf("Build state Name = %q", st.Name())
	}
	if !r.Has("demo.mod") {
		t.Error("Has(demo.mod) should be true")
	}

	// Describe returns the module info.
	mi, ok := r.Describe("demo.mod")
	if !ok {
		t.Fatal("Describe(demo.mod) not found")
	}
	if mi.Module != "demo.mod" || !mi.HasSpec {
		t.Errorf("Describe info = %+v", mi)
	}

	// SpecNames lists it.
	if names := r.SpecNames(); len(names) != 1 || names[0] != "demo.mod" {
		t.Errorf("SpecNames = %v", names)
	}

	// Parse executes the plan.
	rep, err := r.Parse("demo.mod", "nginx", map[string]any{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if rep == nil {
		t.Fatal("nil report")
	}
}

func TestRegisterSpec_Errors(t *testing.T) {
	r := state.NewRegistry()

	if err := r.RegisterSpec(nil, testBuilder); err == nil {
		t.Error("nil spec should error")
	}

	spec := newTestSpec(t, "demo.mod")
	if err := r.RegisterSpec(spec, nil); err == nil {
		t.Error("nil builder should error")
	}

	empty := newTestSpec(t, "x.y")
	empty.Module = ""
	if err := r.RegisterSpec(empty, testBuilder); err == nil {
		t.Error("empty module name should error")
	}

	if err := r.RegisterSpec(spec, testBuilder); err != nil {
		t.Fatalf("first RegisterSpec: %v", err)
	}
	if err := r.RegisterSpec(newTestSpec(t, "demo.mod"), testBuilder); err == nil {
		t.Error("duplicate spec module should error")
	}
}

func TestDescribe_NonSpecAndUnknown(t *testing.T) {
	r := state.NewRegistry()
	r.Register("legacy.mod", testBuilder) // plain register, no spec

	if _, ok := r.Describe("legacy.mod"); ok {
		t.Error("Describe should be false for a spec-less module")
	}
	if _, ok := r.Describe("nope.mod"); ok {
		t.Error("Describe should be false for an unknown module")
	}
	if names := r.SpecNames(); len(names) != 0 {
		t.Errorf("SpecNames should be empty, got %v", names)
	}
}

func TestParse_UnknownModule(t *testing.T) {
	r := state.NewRegistry()
	if _, err := r.Parse("nope.mod", "id", map[string]any{}); err == nil {
		t.Error("Parse of an unregistered module should error")
	}
	// A plain-registered (spec-less) module has no Parse path either.
	r.Register("legacy.mod", testBuilder)
	if _, err := r.Parse("legacy.mod", "id", map[string]any{}); err == nil {
		t.Error("Parse of a spec-less module should error")
	}
}

// TestParse_ReservedKeysNotUnknown pins that Registry.Parse injects the
// state-reserved key set: requisite declarations, generic per-state attributes,
// and compiler-only directives that live in a state config map alongside a
// module's own parameters must NOT be misreported as unknown parameters, while a
// genuinely unknown key still is.
func TestParse_ReservedKeysNotUnknown(t *testing.T) {
	r := state.NewRegistry()
	if err := r.RegisterSpec(newTestSpec(t, "demo.mod"), testBuilder); err != nil {
		t.Fatalf("RegisterSpec: %v", err)
	}

	raw := map[string]any{
		"require":    []any{"pkg.installed:nginx"},
		"watch":      []any{"file.managed:/etc/nginx/nginx.conf"},
		"onchanges":  []any{"cmd.run:build"},
		"onfail":     []any{"cmd.run:rollback"},
		"require_in": []any{"service.running:nginx"},
		"listen":     []any{"file.managed:/etc/x"},
		"order":      "first",
		"onlyif":     "test -f /etc/x",
		"prereq":     []any{"cmd.run:premigrate"},
		"names":      []any{"a", "b"},
		"bogus":      "definitely-not-a-parameter",
	}

	rep, err := r.Parse("demo.mod", "nginx", raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if rep == nil {
		t.Fatal("nil report")
	}

	reserved := state.ReservedKeySet()
	for _, k := range rep.UnknownKeys {
		if _, isReserved := reserved[k]; isReserved {
			t.Errorf("reserved key %q reported as unknown by Parse", k)
		}
	}
	if len(rep.UnknownKeys) != 1 || rep.UnknownKeys[0] != "bogus" {
		t.Errorf("UnknownKeys = %v, want exactly [bogus]", rep.UnknownKeys)
	}
}

func TestRegistrySpec_Concurrent(t *testing.T) {
	r := state.NewRegistry()
	specs := []string{"a.one", "b.two", "c.three", "d.four"}
	var wg sync.WaitGroup
	for _, name := range specs {
		wg.Go(func() {
			_ = r.RegisterSpec(newTestSpec(t, name), testBuilder)
		})
	}
	// Concurrent readers race against the writers (run under -race).
	for range 8 {
		wg.Go(func() {
			_ = r.SpecNames()
			_, _ = r.Describe("a.one")
			_ = r.Has("b.two")
		})
	}
	wg.Wait()
	if names := r.SpecNames(); len(names) != len(specs) {
		t.Errorf("SpecNames len = %d, want %d", len(names), len(specs))
	}
}
