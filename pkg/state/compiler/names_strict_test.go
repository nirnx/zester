package compiler_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/compiler"
	"github.com/nirnx/zester/pkg/state/modules"
	cmdmod "github.com/nirnx/zester/pkg/state/modules/cmd"
	filemod "github.com/nirnx/zester/pkg/state/modules/file"
	"github.com/nirnx/zester/pkg/template"
)

// TestNamesExpansionSchemaAware_StrictPolicy is the L1 regression guard: under
// the strict unknown-parameter policy (strict_params → PolicyError) the compiler
// must expand a `names:` list into one state per name without hard-failing any
// module. A module WITH a primary parameter gets the expanded name injected into
// that canonical key (cmd.run's primary is `command`, not a bare `name`, so the
// injection must land there — landing in a literal "name" key would decode into
// cmd.run's alias, which is not the point being pinned). A module with NO
// primary (the test.* family) gets the historical literal "name" injection
// (namesInjectKey in compiler.go) — restored in full since M1 made "name"
// reserved-tolerated wherever a module declares no such field of its own, so it
// no longer hard-fails strict the way it did before that fix.
//
// This exercises the REAL built-in registry (modules.RegisterAll) under
// PolicyError — the exact configuration the peel wires when strict_params is on —
// so a strict false-positive on the compiler's own name injection is caught here.
func TestNamesExpansionSchemaAware_StrictPolicy(t *testing.T) {
	states := compileStrict(t, "svc", `
run-cmds:
  cmd.run:
    - names:
      - echo one
      - echo two
pings:
  test.ping:
    - names:
      - alpha
      - beta
confs:
  file.managed:
    - names:
      - /etc/a.conf
      - /etc/b.conf
`)

	// cmd.run: no strict failure, and the expanded name lands in the PRIMARY
	// canonical key `command` (BD-8's `command,primary,aliases=name`), NOT a
	// literal "name" — the module has no bare `name` field, so a "name" injection
	// would decode into the alias but the point is the primary carries the value.
	for _, cmd := range []string{"echo one", "echo two"} {
		s := states["cmd.run:"+cmd]
		if s == nil {
			t.Fatalf("cmd.run:%q not built under strict names expansion", cmd)
		}
		cr, ok := s.(*cmdmod.CmdRun)
		if !ok {
			t.Fatalf("cmd.run:%q is %T, want *cmdmod.CmdRun", cmd, s)
		}
		if cr.Command != cmd {
			t.Errorf("cmd.run:%q Command = %q, want the expanded name injected into the command primary", cmd, cr.Command)
		}
	}

	// test.* declares NO parameters: the schema-aware expansion injects the
	// historical literal "name" key (M2 full parity), which strict now excuses
	// via ExtraReserved (M1) rather than rejecting as unknown. The state still
	// builds — the state ID carries the name regardless of the injected key.
	for _, name := range []string{"alpha", "beta"} {
		if states["test.ping:"+name] == nil {
			t.Errorf("test.ping:%s not built under strict names expansion (no-primary module)", name)
		}
	}

	// file.managed: primary IS `name`, so the injection target is byte-identical
	// to the historical literal "name" — the expanded path lands in Path.
	for _, path := range []string{"/etc/a.conf", "/etc/b.conf"} {
		s := states["file.managed:"+path]
		if s == nil {
			t.Fatalf("file.managed:%q not built under strict names expansion", path)
		}
		fm, ok := s.(*filemod.FileManaged)
		if !ok {
			t.Fatalf("file.managed:%q is %T, want *filemod.FileManaged", path, s)
		}
		if fm.Path != path {
			t.Errorf("file.managed:%q Path = %q, want the expanded name injected into name/Path", path, fm.Path)
		}
	}

	if len(states) != 6 {
		t.Errorf("expected 6 expanded states, got %d", len(states))
	}
}

// TestNamesExpansion_NamesWins pins the Salt semantics ruling: a `names:`
// expansion sets each expanded instance's primary from the names entry
// UNCONDITIONALLY — an explicit value at the inject key (or its aliases) is
// dropped, exactly as Salt's compiler does and as legacy zester's
// unconditional `inst["name"] = name` injection did. For cmd.run (primary
// `command`, alias `name`, BD-8) each expanded instance therefore runs its
// OWN name — names + explicit command no longer runs the shared command N
// times.
func TestNamesExpansion_NamesWins(t *testing.T) {
	states := compileStrict(t, "svc", `
shared-cmd:
  cmd.run:
    - names:
      - one
      - two
    - command: echo shared
shared-cmd-via-alias:
  cmd.run:
    - names:
      - three
      - four
    - name: echo shared-alias
`)

	for _, id := range []string{"one", "two"} {
		s := states["cmd.run:"+id]
		if s == nil {
			t.Fatalf("cmd.run:%s not built", id)
		}
		cr, ok := s.(*cmdmod.CmdRun)
		if !ok {
			t.Fatalf("cmd.run:%s is %T, want *cmdmod.CmdRun", id, s)
		}
		if cr.Command != id {
			t.Errorf("cmd.run:%s Command = %q, want the expanded name (Salt names-wins semantics)", id, cr.Command)
		}
	}
	for _, id := range []string{"three", "four"} {
		s := states["cmd.run:"+id]
		if s == nil {
			t.Fatalf("cmd.run:%s not built", id)
		}
		cr, ok := s.(*cmdmod.CmdRun)
		if !ok {
			t.Fatalf("cmd.run:%s is %T, want *cmdmod.CmdRun", id, s)
		}
		if cr.Command != id {
			t.Errorf("cmd.run:%s Command = %q, want the expanded name (names wins over the explicit name: alias too)", id, cr.Command)
		}
	}
	if len(states) != 4 {
		t.Errorf("expected 4 expanded states, got %d", len(states))
	}
}

// TestNamesExpansion_ModuleRunOpenParamsInjectsName pins the OpenParams branch of
// namesInjectKey: module.run has a Spec but NO primary parameter, yet it is a
// passthrough (OpenParams) that resolves its target from config["name"]. The
// schema-aware expansion must therefore keep injecting "name" (not "nothing"),
// so the classic `names:`-over-module.run form still selects the target — under
// strict, and without a false positive (module.run skips unknown-key validation).
func TestNamesExpansion_ModuleRunOpenParamsInjectsName(t *testing.T) {
	states := compileStrict(t, "mr", `
wrap:
  module.run:
    - names:
      - test.ping
      - test.nop
`)
	// A successful build IS the proof: had the "name" injection been dropped
	// (treated as a no-primary module), resolveModuleRunTarget would return "no
	// target module" and compileStrict would have failed the compile. Each
	// expanded state also wraps a real target (its Check delegates), so the
	// target resolved.
	for _, target := range []string{"test.ping", "test.nop"} {
		if states["module.run:"+target] == nil {
			t.Errorf("module.run:%q not built — the OpenParams name injection was dropped", target)
		}
	}
	if len(states) != 2 {
		t.Errorf("expected 2 expanded module.run states, got %d", len(states))
	}
}

// TestNamesExpansion_LegacyPlainBuilderInjectsName pins the no-spec branch of
// namesInjectKey: a module registered with a PLAIN builder (no schema — the
// legacy / Starlark-without-PARAMS path) keeps the historical literal-"name"
// injection so a builder that reads config["name"] directly still sees it.
func TestNamesExpansion_LegacyPlainBuilderInjectsName(t *testing.T) {
	tmpDir := t.TempDir()
	reg := state.NewRegistry()
	var seen []string
	reg.Register("legacy.mod", func(id string, config map[string]any) (state.State, error) {
		// A legacy builder reads config["name"] directly.
		if n, _ := config["name"].(string); n != "" {
			seen = append(seen, n)
		}
		return &testState{name: "legacy.mod:" + id}, nil
	})

	engine, err := template.NewEngine(template.EngineConfig{BasePath: tmpDir})
	if err != nil {
		t.Fatal(err)
	}
	c := compiler.NewCompiler(compiler.CompilerConfig{StatesDir: tmpDir, Engine: engine, Registry: reg})
	if err := os.MkdirAll(filepath.Join(tmpDir, "leg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "leg", "init.zy"), []byte(`
do:
  legacy.mod:
    - names:
      - x
      - y
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Compile(compiler.StateRef("leg")); err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if len(seen) != 2 || seen[0] == "" {
		t.Errorf("legacy plain builder should still receive config[\"name\"], got %v", seen)
	}
}

// compileStrict builds a compiler backed by the REAL built-in module registry
// under PolicyError (the strict_params-on configuration) and compiles a single
// init.zy under a formula dir, returning the built states keyed by Name().
func compileStrict(t *testing.T, formula, content string) map[string]state.State {
	t.Helper()
	tmpDir := t.TempDir()

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
	reg := state.NewRegistry()
	// PolicyError + the fleet reserved-key union + the exec-layer "test" flag and
	// the Salt universal "name" anchor (M1) — exactly what
	// internal/peeld.decodeOptions(true, …) wires (§5: "same options flow to the
	// compiler path").
	modules.RegisterAll(reg, mctx, modschema.DecodeOptions{
		Unknown:       modschema.PolicyError,
		Reserved:      state.ReservedKeySet(),
		ExtraReserved: []string{"test", "name"},
	})

	engine, err := template.NewEngine(template.EngineConfig{BasePath: tmpDir})
	if err != nil {
		t.Fatal(err)
	}
	c := compiler.NewCompiler(compiler.CompilerConfig{
		StatesDir: tmpDir,
		Engine:    engine,
		Registry:  reg,
		Facts:     map[string]any{"os": "linux"},
	})
	if err := os.MkdirAll(filepath.Join(tmpDir, formula), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, formula, "init.zy"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := c.Compile(compiler.StateRef(formula))
	if err != nil {
		t.Fatalf("strict compile failed (schema-aware names expansion false-positive?): %v", err)
	}
	out := make(map[string]state.State, len(result.States))
	for _, s := range result.States {
		out[s.Name()] = s
	}
	return out
}
