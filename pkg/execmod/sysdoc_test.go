package execmod_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/execmod"
	"github.com/nirnx/zester/pkg/modschema"
)

// fakeDocSource is a minimal execmod.DocSource for exercising SysDoc /
// SysListFunctions without pulling in the peel wiring.
type fakeDocSource struct {
	infos map[string]modschema.ModuleInfo
	names []string
}

func (f fakeDocSource) Describe(name string) (modschema.ModuleInfo, bool) {
	mi, ok := f.infos[name]
	return mi, ok
}
func (f fakeDocSource) Names() []string { return f.names }

func callFn(t *testing.T, fn execmod.Func, args map[string]any) (string, error) {
	t.Helper()
	return fn(context.Background(), nil, args)
}

func TestSysDoc_Index(t *testing.T) {
	src := fakeDocSource{names: []string{"cmd.run", "facts.get", "pkg.installed"}}
	out, err := callFn(t, execmod.SysDoc(src), nil)
	if err != nil {
		t.Fatalf("SysDoc index: %v", err)
	}
	if out != "cmd.run\nfacts.get\npkg.installed" {
		t.Errorf("index = %q", out)
	}
}

func TestSysDoc_ByName(t *testing.T) {
	src := fakeDocSource{infos: map[string]modschema.ModuleInfo{
		"facts.get": {
			Module: "facts.get",
			Kind:   modschema.KindDispatch,
			Doc:    modschema.Doc{Summary: "Read a single fact by dotted key."},
		},
	}}
	out, err := callFn(t, execmod.SysDoc(src), map[string]any{"name": "facts.get"})
	if err != nil {
		t.Fatalf("SysDoc facts.get: %v", err)
	}
	// Rendered via the SAME modschema.RenderText the docs layer uses.
	if !strings.Contains(out, "facts.get (dispatch)") {
		t.Errorf("rendered doc missing header:\n%s", out)
	}
	if !strings.Contains(out, "Read a single fact by dotted key.") {
		t.Errorf("rendered doc missing summary:\n%s", out)
	}
}

// TestSysDoc_DualSurfaceAppendix pins the cmd.run dual-surface rendering: a
// ModuleInfo with AlsoExecmod set surfaces the salt['<mod>'] template-reach note.
func TestSysDoc_DualSurfaceAppendix(t *testing.T) {
	src := fakeDocSource{infos: map[string]modschema.ModuleInfo{
		"cmd.run": {
			Module:      "cmd.run",
			Kind:        modschema.KindState,
			AlsoExecmod: true,
			Doc:         modschema.Doc{Summary: "Run a command."},
		},
	}}
	out, err := callFn(t, execmod.SysDoc(src), map[string]any{"name": "cmd.run"})
	if err != nil {
		t.Fatalf("SysDoc cmd.run: %v", err)
	}
	if !strings.Contains(out, "salt['cmd.run']") {
		t.Errorf("dual-surface appendix missing salt['cmd.run']:\n%s", out)
	}
}

func TestSysDoc_UnknownAndNil(t *testing.T) {
	src := fakeDocSource{infos: map[string]modschema.ModuleInfo{}}
	if _, err := callFn(t, execmod.SysDoc(src), map[string]any{"name": "nope.fn"}); err == nil {
		t.Error("SysDoc(unknown) should error")
	}
	if _, err := callFn(t, execmod.SysDoc(nil), nil); err == nil {
		t.Error("SysDoc(nil source) should error")
	}
}

func TestSysListFunctions_Merged(t *testing.T) {
	src := fakeDocSource{names: []string{"cmd.run", "facts.get", "state.apply"}}
	out, err := callFn(t, execmod.SysListFunctions(src), nil)
	if err != nil {
		t.Fatalf("SysListFunctions: %v", err)
	}
	if out != "cmd.run\nfacts.get\nstate.apply" {
		t.Errorf("merged list = %q", out)
	}
	if _, err := callFn(t, execmod.SysListFunctions(nil), nil); err == nil {
		t.Error("SysListFunctions(nil source) should error")
	}
}
