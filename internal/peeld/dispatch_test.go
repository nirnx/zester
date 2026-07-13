package peeld

import (
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/proto"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules"
)

// TestDispatchTableBound is the §7 pin: the two peel dispatch sites and the
// bound handlers stay 1:1 with the shared modules.DispatchSpecials table.
//
//  1. Every table entry has a bound specialHandler (execModule's site).
//  2. No handler is bound to a name absent from the table.
//  3. readOnlyModule's classification (the read-only site) agrees with each
//     concrete entry's ReadOnly bit.
func TestDispatchTableBound(t *testing.T) {
	a := newTestAgent(t)

	tableNames := make(map[string]bool, len(modules.DispatchSpecials))
	for _, sp := range modules.DispatchSpecials {
		tableNames[sp.Name] = true
		if a.specialHandlers[sp.Name] == nil {
			t.Errorf("no handler bound for dispatch special %q (execModule would fall through)", sp.Name)
		}
	}
	for name := range a.specialHandlers {
		if !tableNames[name] {
			t.Errorf("handler %q bound with no matching DispatchSpecials entry", name)
		}
	}

	// readOnlyModule agrees with the table for every concrete (addressable)
	// special. Family catch-alls are not directly addressable module names.
	for _, sp := range modules.DispatchSpecials {
		if sp.Match != modules.DispatchExact {
			continue
		}
		if got := a.readOnlyModule(sp.Name); got != sp.ReadOnly {
			t.Errorf("readOnlyModule(%q) = %v, table ReadOnly = %v — the two sites disagree",
				sp.Name, got, sp.ReadOnly)
		}
	}
}

// TestSysDocReadOnly pins that sys.doc joins the read-only set (so it answers
// during long state runs) once the peel's DocSource is wired.
func TestSysDocReadOnly(t *testing.T) {
	a := newTestAgent(t)
	if !a.readOnlyModule("sys.doc") {
		t.Error("readOnlyModule(sys.doc) = false, want true")
	}
	// A state module shadowing sys.doc drops it from the read-only fast path,
	// mirroring the sys.list_functions/grains precedence rule.
	a.registry.Register("sys.doc", modules.NewTestPingBuilder(modschema.DecodeOptions{}))
	if a.readOnlyModule("sys.doc") {
		t.Error("readOnlyModule(sys.doc) = true after a state module shadowed it")
	}
}

// TestSysDocExecutes runs sys.doc end-to-end through the read-only dispatch
// path (handleExecRequest → execReadOnly → execmod), pinning the result-string
// wire (ExecResponse.Results[0].Details["result"]) and the RenderText output.
func TestSysDocExecutes(t *testing.T) {
	a := newTestAgent(t)
	// execReadOnly's default (execmod) branch derives an immutable context from
	// mctxTemplate; give the test agent a minimal one.
	a.mctxTemplate = exec.NewModuleContext(&exec.ProviderSet{}, nil, nil, discardLogger())

	// Bare sys.doc → the unified index. It must include a dispatch special, a
	// state module, and an execmod function.
	idx := runReadOnly(t, a, proto.ExecRequest{Module: "sys.doc"})
	for _, want := range []string{"state.apply", "test.ping", "cmd.run"} {
		if !strings.Contains(idx, want) {
			t.Errorf("sys.doc index missing %q:\n%s", want, idx)
		}
	}

	// sys.doc for a dispatch special → its dispatch doc, via the SAME RenderText
	// the docs layer uses.
	doc := runReadOnly(t, a, proto.ExecRequest{ID: "facts.get", Module: "sys.doc"})
	if !strings.Contains(doc, "facts.get (dispatch)") {
		t.Errorf("sys.doc facts.get missing dispatch header:\n%s", doc)
	}
}

// TestUnknownFamilySubfunctionError pins that an unrecognized subfunction still
// routes through the family catch-all to the in-handler error message — the
// exact behavior the table-driven dispatch must preserve.
func TestUnknownFamilySubfunctionError(t *testing.T) {
	a := newTestAgent(t)
	a.basketScopeMu.Lock()
	a.cachedSettings = map[string]any{"x": "y"} // so settings.* has a snapshot to query
	a.basketScopeMu.Unlock()

	tests := []struct {
		module  string
		wantErr string
	}{
		{"facts.bogus", "unknown facts function: facts.bogus"},
		{"settings.bogus", "unknown settings function: settings.bogus"},
		{"pillar.bogus", "unknown settings function: pillar.bogus"},
	}
	for _, tt := range tests {
		if !a.readOnlyModule(tt.module) {
			t.Fatalf("%q should classify read-only (family catch-all)", tt.module)
		}
		req := proto.ExecRequest{Module: tt.module}
		resp, err := a.execReadOnly(a.runCtx, req)
		if err != nil {
			t.Fatalf("execReadOnly(%q): %v", tt.module, err)
		}
		if resp.Error != tt.wantErr {
			t.Errorf("execReadOnly(%q) error = %q, want %q", tt.module, resp.Error, tt.wantErr)
		}
	}
}

// runReadOnly drives one read-only request through handleExecRequest via
// request/reply and returns the first result's "result" detail string.
func runReadOnly(t *testing.T, a *Agent, req proto.ExecRequest) string {
	t.Helper()
	if !a.readOnlyModule(req.Module) {
		t.Fatalf("module %q is not read-only; test would not exercise the fast path", req.Module)
	}
	data, err := bus.Encode(req)
	if err != nil {
		t.Fatal(err)
	}
	var resp proto.ExecResponse
	msg := bus.NewMsg("zester.cmd.p1", data, "_INBOX.test", func(b []byte) error {
		return bus.Decode(b, &resp)
	})
	a.handleExecRequest(msg)
	if !resp.Success {
		t.Fatalf("read-only exec %q failed: %q", req.Module, resp.Error)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %+v, want exactly 1", resp.Results)
	}
	return resp.Results[0].Details["result"]
}

// TestPeelDocSourcePrecedence pins the DocSource lookup precedence (specials →
// state registry → execmod) and the cmd.run dual-surface flag.
func TestPeelDocSourcePrecedence(t *testing.T) {
	a := newTestAgent(t)
	src := peelDocSource{a: a}

	// 1. Dispatch special wins and is rendered as Kind dispatch.
	if mi, ok := src.Describe("facts.get"); !ok || mi.Kind != modschema.KindDispatch {
		t.Errorf("Describe(facts.get) = %+v ok=%v, want Kind dispatch", mi, ok)
	}

	// 2. A migrated state module that is ALSO an execmod function (cmd.run) sets
	//    AlsoExecmod so RenderText appends the salt['cmd.run'] dual-surface note.
	spec, err := modschema.NewSpec("cmd.run", modschema.KindState, dualProto{}, modschema.Doc{
		Summary: "Run a command.",
		Effects: modschema.Effects{Check: "checks", Apply: "applies"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.registry.RegisterSpec(spec, func(id string, _ map[string]any) (state.State, error) {
		return modules.NewTestPingBuilder(modschema.DecodeOptions{})(id, nil)
	}); err != nil {
		t.Fatal(err)
	}
	mi, ok := src.Describe("cmd.run")
	if !ok {
		t.Fatal("Describe(cmd.run) not found")
	}
	if mi.Kind != modschema.KindState {
		t.Errorf("Describe(cmd.run) Kind = %v, want state (registry precedence over execmod)", mi.Kind)
	}
	if !mi.AlsoExecmod {
		t.Error("Describe(cmd.run) AlsoExecmod = false, want true (dual-surface)")
	}
	if !strings.Contains(modschema.RenderText(mi), "salt['cmd.run']") {
		t.Error("RenderText(cmd.run) missing dual-surface appendix")
	}

	// 3. An execmod-only function (no state spec) is described from execmod.
	//    grains.item is a plain (spec-less) execmod fn, so it is NOT described;
	//    an unknown name is likewise not found.
	if _, ok := src.Describe("nope.fn"); ok {
		t.Error("Describe(nope.fn) should be false")
	}

	// Names merges state + exec + dispatch surfaces.
	names := src.Names()
	nameSet := map[string]bool{}
	for _, n := range names {
		nameSet[n] = true
	}
	for _, want := range []string{"cmd.run", "test.ping", "state.apply", "facts.get", "sys.doc"} {
		if !nameSet[want] {
			t.Errorf("Names() missing %q: %v", want, names)
		}
	}
	// The prefix-family catch-alls are NOT callable names.
	if nameSet["facts."] || nameSet["settings."] {
		t.Error("Names() must not include prefix-family catch-alls")
	}
}

// dualProto is a trivial schema proto for the cmd.run dual-surface test.
type dualProto struct {
	Command string `zester:"command,primary" usage:"the command"`
}

// TestPeelDocSourceCoversAllStateModules is the K1 permanent pin (keystone spec
// §7, gate clause 3): the peel's sys.doc unified index (peelDocSource.Names())
// covers EVERY registered state module, so that coverage is a TEST rather than an
// empirical observation. It registers the full built-in state-module set the way
// the runtime does (modules.RegisterAll) and asserts Names() is a superset of the
// state registry's own module set (Names merges state + execmod + dispatch
// surfaces, so it must contain every state module verbatim).
func TestPeelDocSourceCoversAllStateModules(t *testing.T) {
	a := newTestAgent(t)

	// Register the FULL built-in state-module set (the runtime does this via
	// registerStateModules → modules.RegisterAll); the bare test agent registers
	// only test.ping, which would make the coverage assertion vacuous.
	full := state.NewRegistry()
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{
		Package: exectest.NewFakePackageExec("apt"),
		File:    exectest.NewFakeFileExec(),
		Command: exectest.NewFakeCommandExec(),
		Service: exectest.NewFakeServiceExec("systemd"),
		User:    exectest.NewFakeUserExec(),
		Group:   exectest.NewFakeGroupExec(),
		Cron:    exectest.NewFakeCronExec(),
		Sysctl:  exectest.NewFakeSysctlExec(),
		Mount:   exectest.NewFakeMountExec(),
	}}
	modules.RegisterAll(full, mctx, modschema.DecodeOptions{})
	a.registry = full // peelDocSource reads a.registry live

	src := peelDocSource{a: a}
	have := map[string]bool{}
	for _, n := range src.Names() {
		have[n] = true
	}

	stateNames := full.Modules()
	if len(stateNames) < 47 {
		t.Fatalf("expected the full built-in state-module set (>=47), got %d", len(stateNames))
	}
	for _, n := range stateNames {
		if !have[n] {
			t.Errorf("sys.doc unified index (peelDocSource.Names) is missing registered state module %q", n)
		}
	}
	// The gate-close arrivals (and other N:1 / OpenParams surfaces) must be indexed.
	for _, n := range []string{"cmd.run", "service.enabled", "module.run", "test.configurable_test_state"} {
		if !have[n] {
			t.Errorf("sys.doc index missing %q", n)
		}
	}
}
