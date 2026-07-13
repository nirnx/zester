package execmod_test

import (
	"context"
	"sync"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/execmod"
	"github.com/nirnx/zester/pkg/modschema"
)

type execSpecProto struct {
	Name string `zester:"name,primary" usage:"the name"`
}

func newExecSpec(t *testing.T, module string) *modschema.Spec {
	t.Helper()
	s, err := modschema.NewSpec(module, modschema.KindExec, execSpecProto{}, modschema.Doc{
		Summary: "test",
		Effects: modschema.Effects{Execution: "does the thing"},
	})
	if err != nil {
		t.Fatalf("NewSpec %s: %v", module, err)
	}
	return s
}

func execFn(context.Context, *exec.ModuleContext, map[string]any) (string, error) {
	return "ok", nil
}

func TestExecRegisterSpec_RegistersBothPaths(t *testing.T) {
	r := execmod.NewRegistry()
	if err := r.RegisterSpec(newExecSpec(t, "demo.fn"), execFn); err != nil {
		t.Fatalf("RegisterSpec: %v", err)
	}

	if !r.Has("demo.fn") {
		t.Error("Has(demo.fn) should be true")
	}
	out, err := r.Call(context.Background(), "demo.fn", nil, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out != "ok" {
		t.Errorf("Call = %q", out)
	}

	mi, ok := r.Describe("demo.fn")
	if !ok || mi.Module != "demo.fn" || mi.Kind != modschema.KindExec {
		t.Errorf("Describe = %+v ok=%v", mi, ok)
	}
	if names := r.SpecNames(); len(names) != 1 || names[0] != "demo.fn" {
		t.Errorf("SpecNames = %v", names)
	}
}

func TestExecRegisterSpec_Errors(t *testing.T) {
	r := execmod.NewRegistry()

	if err := r.RegisterSpec(nil, execFn); err == nil {
		t.Error("nil spec should error")
	}
	spec := newExecSpec(t, "demo.fn")
	if err := r.RegisterSpec(spec, nil); err == nil {
		t.Error("nil fn should error")
	}
	empty := newExecSpec(t, "x.y")
	empty.Module = ""
	if err := r.RegisterSpec(empty, execFn); err == nil {
		t.Error("empty module should error")
	}
	if err := r.RegisterSpec(spec, execFn); err != nil {
		t.Fatalf("first RegisterSpec: %v", err)
	}
	if err := r.RegisterSpec(newExecSpec(t, "demo.fn"), execFn); err == nil {
		t.Error("duplicate should error")
	}
}

func TestExecDescribe_NonSpecAndUnknown(t *testing.T) {
	r := execmod.NewRegistry()
	r.Register("legacy.fn", execFn)
	if _, ok := r.Describe("legacy.fn"); ok {
		t.Error("Describe should be false for a spec-less function")
	}
	if _, ok := r.Describe("nope.fn"); ok {
		t.Error("Describe should be false for an unknown function")
	}
	if names := r.SpecNames(); len(names) != 0 {
		t.Errorf("SpecNames should be empty, got %v", names)
	}
}

func TestExecRegistrySpec_Concurrent(t *testing.T) {
	r := execmod.NewRegistry()
	names := []string{"a.one", "b.two", "c.three", "d.four"}
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Go(func() {
			_ = r.RegisterSpec(newExecSpec(t, n), execFn)
		})
	}
	for range 8 {
		wg.Go(func() {
			_ = r.SpecNames()
			_, _ = r.Describe("a.one")
			_ = r.Has("b.two")
		})
	}
	wg.Wait()
	if got := r.SpecNames(); len(got) != len(names) {
		t.Errorf("SpecNames len = %d, want %d", len(got), len(names))
	}
}
