package peeld

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nirnx/zester/internal/config"
	"github.com/nirnx/zester/internal/metrics"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/execmod"
	"github.com/nirnx/zester/pkg/facts"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/proto"
	"github.com/nirnx/zester/pkg/state"
	filemod "github.com/nirnx/zester/pkg/state/modules/file"
)

// The strict flip (keystone spec §5 endgame): strict_params defaults ON, so an
// unknown state-module parameter FAILS the build with a typed UnknownKeyError.
// These tests audit every decode path the peel funnels a state build through —
// compiled highstate, ad-hoc exec, module.run forwarding, reactor dispatch, and
// the Starlark validating builder (the last in pkg/starmod) — proving that under
// PolicyError:
//   - reserved directives (requisites/attributes/compiler keys) and injected
//     control keys (the exec-layer "test", module.run's "name") never
//     false-positive as unknown parameters, and
//   - a genuine typo does fail, with the module, key, and did-you-mean
//     suggestion named.
//
// L3 MACHINERY-INJECTED-KEY AUDIT (orchestrator ruling 2026-07-13; pinned here
// as the audit's conclusion). A strict false positive can only arise if some
// piece of MACHINERY writes a non-user, non-reserved key into a per-state config
// map that then reaches a Spec's strict Decode. Grepping every config-map write
// in the compiler (pkg/state/compiler) and the peel (internal/peeld) that can
// flow into registry.Build → spec.Decode leaves exactly ONE such site:
//   - compiler `names:` expansion (compiler.go) — historically wrote a LITERAL
//     "name" into every expanded instance, which hard-failed cmd.run (primary
//     `command`, no bare `name` field) under strict. FIXED (and then
//     RE-SIMPLIFIED by M1/M2): the expansion is schema-aware (namesInjectKey),
//     injecting the module's PRIMARY canonical key when it has one (cmd.run →
//     `command`, never overwriting an explicit `command:`/`name:` already
//     present — M2), or the historical literal "name" otherwise (OpenParams/
//     legacy/no-primary modules like test.* — safe now that M1 added "name" to
//     this file's ExtraReserved, so a no-primary module no longer hard-fails on
//     the injected key). Pinned by pkg/state/compiler's
//     TestNamesExpansionSchemaAware_StrictPolicy and
//     TestNamesExpansion_DoesNotOverwriteExplicitPrimary.
// Every OTHER config-map write is NOT a false-positive source:
//   - transformRequisites `_in`/listen/prereq rewrites (requisites.go) inject
//     ONLY requisite keys (require/watch/onchanges/onfail), which are in
//     state.ReservedKeySet() and so excused by DecodeOptions.Reserved.
//   - the peel's `args["name"] = id` positional default (exec.go, two sites) is
//     confined to the EXECMOD imperative path (a.execReg.Call) — grains.*/
//     pkg.version/… — which runs no strict Spec.Decode; the state build
//     (registry.Build at exec.go, ad-hoc AND reactor dispatch) passes args
//     UNMODIFIED, primary-defaults-to-id living inside the compiled plan.
//   - module.run forwarding copies the user's config (its own "name" selector
//     filtered out); deep-merge and reactor reaction kwargs carry USER keys
//     through unchanged — a typo there is a real typo, correctly failed.
// So `names` was the only machinery-injected key, and it is fixed; the tests
// below exercise the remaining paths to keep that conclusion honest.

// TestStrictParams_CompiledHighstateReservedKeys covers the compiled-highstate
// path: the compiler hands Registry.Build a config map that STILL carries the
// requisites, generic attributes, and compiler directives (they are consumed
// later by ParseRequisites / WrapAttributes / the compiler, not by the module).
// Under strict, none of them may be mistaken for an unknown parameter.
func TestStrictParams_CompiledHighstateReservedKeys(t *testing.T) {
	registry, h := registerForTest(t, true)

	// The full reserved union present alongside the real primary — every key a
	// compiled state can legitimately carry into Build.
	s, err := registry.Build("pkg.removed", "telnet", map[string]any{
		"name":       "telnet",
		"require":    []any{"cmd.run:a"},
		"watch":      []any{"file.managed:/x"},
		"onchanges":  []any{"cmd.run:b"},
		"onfail":     []any{"cmd.run:c"},
		"onlyif":     "true",
		"unless":     "false",
		"order":      5,
		"retry":      3,
		"failhard":   true,
		"prereq":     []any{"cmd.run:d"},
		"names":      []any{"telnet"},
		"listen":     []any{"cmd.run:e"},
		"listen_in":  []any{"cmd.run:f"},
		"require_in": []any{"cmd.run:g"},
		"test":       true, // exec-layer dry-run flag (ExtraReserved)
	})
	if err != nil {
		t.Fatalf("reserved/attribute/compiler keys must not fail a strict build: %v", err)
	}
	if s == nil {
		t.Fatal("state must build")
	}
	if warns := h.messages(); len(warns) != 0 {
		t.Fatalf("strict policy must not warn (it errors instead), got %d: %v", len(warns), warns)
	}
}

// TestStrictParams_TypoFailsWithTypedError pins the headline behavior: under
// strict a typo'd parameter fails the build with a modschema.UnknownKeyError
// that names the module, the offending key, and the did-you-mean suggestion.
func TestStrictParams_TypoFailsWithTypedError(t *testing.T) {
	registry, _ := registerForTest(t, true)

	_, err := registry.Build("pkg.removed", "telnet", map[string]any{
		"nmae": "telnet", // typo of "name"
	})
	if err == nil {
		t.Fatal("strict build must fail on an unknown parameter")
	}
	var uke *modschema.UnknownKeyError
	if !errors.As(err, &uke) {
		t.Fatalf("want a modschema.UnknownKeyError in the chain, got %T: %v", err, err)
	}
	if uke.Module != "pkg.removed" {
		t.Errorf("UnknownKeyError.Module = %q, want pkg.removed", uke.Module)
	}
	if uke.Key != "nmae" {
		t.Errorf("UnknownKeyError.Key = %q, want nmae", uke.Key)
	}
	if uke.Suggestion != "name" {
		t.Errorf("UnknownKeyError.Suggestion = %q, want name (did-you-mean)", uke.Suggestion)
	}
}

// TestStrictParams_SaltUniversalNameAnchor pins M1 (keystone spec final fix
// pass): the Salt universal state-identifier idiom — an explicit `name:` on a
// module that declares no `name` parameter of its own, e.g. `test.nop: - name:
// anchor` — must build under default strict, not hard-fail. test.nop declares
// ZERO parameters, so before the fix "name" had no home: it was neither a
// declared field nor excused, and strict rejected it as unknown. The fix adds
// "name" to internal/peeld's ExtraReserved alongside the exec-layer "test".
func TestStrictParams_SaltUniversalNameAnchor(t *testing.T) {
	registry, h := registerForTest(t, true)

	s, err := registry.Build("test.nop", "anchor", map[string]any{
		"name": "anchor",
	})
	if err != nil {
		t.Fatalf("test.nop with an explicit name: anchor must build under strict (M1): %v", err)
	}
	if s == nil {
		t.Fatal("state must build")
	}
	if warns := h.messages(); len(warns) != 0 {
		t.Fatalf("strict policy must not warn (it excuses \"name\", not warn-and-continue), got %d: %v", len(warns), warns)
	}
}

// TestStrictParams_TypoStillFailsOnNoPrimaryModule is M1's flip side: the
// ExtraReserved={"name"} excuse is for the LITERAL key "name" only — it must
// never widen into general tolerance for an unrecognized parameter on a module
// that declares none. A typo of the anchor idiom on test.nop (which has no
// fields at all, so there is no declared-name suggestion to offer) still fails
// strict with a typed UnknownKeyError.
func TestStrictParams_TypoStillFailsOnNoPrimaryModule(t *testing.T) {
	registry, _ := registerForTest(t, true)

	_, err := registry.Build("test.nop", "anchor", map[string]any{
		"nmae": "anchor", // typo of "name" — NOT in ExtraReserved, must not be excused
	})
	if err == nil {
		t.Fatal("strict build must fail on an unknown parameter, even on a module with no declared fields")
	}
	var uke *modschema.UnknownKeyError
	if !errors.As(err, &uke) {
		t.Fatalf("want a modschema.UnknownKeyError in the chain, got %T: %v", err, err)
	}
	if uke.Module != "test.nop" {
		t.Errorf("UnknownKeyError.Module = %q, want test.nop", uke.Module)
	}
	if uke.Key != "nmae" {
		t.Errorf("UnknownKeyError.Key = %q, want nmae", uke.Key)
	}
}

// TestStrictParams_DeclaredNameFieldWinsOverExtraReserved pins the ordering
// half of M1: a module that DOES declare its own `name` parameter (file.managed
// — primary `name`) still decodes an explicit `name:` value into that field —
// ExtraReserved's "name" excuse is a fallback for keys no field claimed, not a
// substitute for real parameter binding. Declared names win before the
// reserved-key check (see modschema.checkUnknownKeys: consumed/knownKeys are
// checked before Reserved/ExtraReserved).
func TestStrictParams_DeclaredNameFieldWinsOverExtraReserved(t *testing.T) {
	// file.managed needs a File provider, which registerForTest's Package-only
	// mctx does not wire; build a dedicated registry the way compileStrict does.
	logger := discardLogger()
	mctx := exec.NewModuleContext(
		&exec.ProviderSet{
			Package: exectest.NewFakePackageExec("apt"),
			File:    exectest.NewFakeFileExec(),
		},
		map[string]any{}, nil, logger,
	)
	registry := state.NewRegistry()
	registerStateModules(registry, mctx, decodeOptions(true, logger))

	s, err := registry.Build("file.managed", "cfg", map[string]any{
		"name": "/etc/explicit.conf",
	})
	if err != nil {
		t.Fatalf("file.managed name: must still decode as the param: %v", err)
	}
	fm, ok := s.(*filemod.FileManaged)
	if !ok {
		t.Fatalf("file.managed built %T, want *filemod.FileManaged", s)
	}
	if fm.Path != "/etc/explicit.conf" {
		t.Errorf("Path = %q, want the explicit name: value (declared field must win over ExtraReserved)", fm.Path)
	}
}

// TestStrictParams_ModuleRunForwarding covers the module.run path. module.run is
// OpenParams (its own unknown keys are skipped), but it FORWARDS its config —
// reserved directives carried through, its own "name" selector filtered out — to
// the target module, which decodes under the same strict policy. So reserved
// keys must not false-positive on the target, yet a typo forwarded to the target
// must still fail (module.run does not shield typos).
func TestStrictParams_ModuleRunForwarding(t *testing.T) {
	registry, _ := registerForTest(t, true)

	// Classic form: name selects the target; require/test are forwarded and must
	// not false-positive on pkg.removed under strict.
	if _, err := registry.Build("module.run", "telnet", map[string]any{
		"name":    "pkg.removed",
		"require": []any{"cmd.run:a"},
		"test":    true,
	}); err != nil {
		t.Fatalf("module.run forwarding of reserved keys must not fail under strict: %v", err)
	}

	// A typo forwarded to the target still fails — module.run cannot launder an
	// unknown parameter past the strict target decode.
	_, err := registry.Build("module.run", "telnet", map[string]any{
		"name": "pkg.removed",
		"nmae": "telnet",
	})
	if err == nil {
		t.Fatal("module.run must not hide an unknown target parameter under strict")
	}
	var uke *modschema.UnknownKeyError
	if !errors.As(err, &uke) || uke.Key != "nmae" {
		t.Fatalf("want an UnknownKeyError for nmae through module.run, got %v", err)
	}
}

// newStrictExecAgent builds an Agent wired for the real execModule path with the
// full built-in registry under the given strict policy: enough to drive an
// ad-hoc / reactor-dispatched single-module execution end to end without NATS.
func newStrictExecAgent(t *testing.T, strict bool) *Agent {
	t.Helper()
	logger := discardLogger()
	a := New(&config.PeelConfig{ID: "p1", StrictParams: strict}, logger)
	a.peelID = "p1"
	a.settingsSnapshotPath = ""
	a.dedup = newDedupTracker("", dedupCapacity, noSave, logger)
	a.runCtx = context.Background()
	a.metrics = metrics.NewPeelRegistry()
	a.ps = bustest.NewFakePubSub()
	a.execReg = execmod.DefaultRegistry()

	// A populated states cache pinned as the effective dir, so execModule's
	// states-dir switch is a no-op (no starLoader rebuild, no fallback Warn).
	statesDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(statesDir, "keep.zy"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.cfg.StatesCache = statesDir
	a.bakedStatesDir = statesDir
	a.effectiveStatesDir = statesDir

	mctx := exec.NewModuleContext(
		&exec.ProviderSet{Package: exectest.NewFakePackageExec("apt")},
		map[string]any{}, nil, logger,
	)
	a.mctx = mctx
	a.mctxTemplate = mctx.WithFactsSettings(nil, nil)
	a.decodeOpts = decodeOptions(strict, logger)
	a.registry = state.NewRegistry()
	registerStateModules(a.registry, a.mctx, a.decodeOpts)
	a.wireDocSource()
	a.runner = state.NewRunner(logger)
	a.guardRunner = state.GuardRunnerFunc(a.runGuard)

	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(context.Background(), js); err != nil {
		t.Fatal(err)
	}
	mgr, err := facts.NewManager(facts.ManagerConfig{PeelID: "p1", JS: js, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	a.mgr = mgr

	return a
}

// TestStrictParams_AdHocExecPath drives the real execModule ad-hoc path — the
// same path a reactor dispatch.module lands on (master → ExecRequest → peel
// handler → execModule → registry.Build). Under strict a typo fails the build
// (surfaced as a "build state:" error response), while the injected exec-layer
// "test" flag and a reserved attribute never false-positive.
func TestStrictParams_AdHocExecPath(t *testing.T) {
	a := newStrictExecAgent(t, true)

	// Typo → build failure surfaced in the ExecResponse, naming module/key.
	resp, err := a.execModule(context.Background(), proto.ExecRequest{
		ID:     "telnet",
		Module: "pkg.removed",
		Args:   map[string]any{"nmae": "telnet", "test": true},
	})
	if err != nil {
		t.Fatalf("execModule returned a transport error: %v", err)
	}
	if resp.Success {
		t.Fatal("ad-hoc run with a typo'd parameter must not succeed under strict")
	}
	for _, want := range []string{"pkg.removed", "nmae", "name"} {
		if !strings.Contains(resp.Error, want) {
			t.Errorf("error %q must mention %q", resp.Error, want)
		}
	}

	// Injected "test" + a reserved "order" attribute must not false-positive:
	// the build succeeds and the dry run reports (fake package not installed).
	resp, err = a.execModule(context.Background(), proto.ExecRequest{
		ID:     "telnet",
		Module: "pkg.removed",
		Args:   map[string]any{"name": "telnet", "order": 5, "test": true},
	})
	if err != nil {
		t.Fatalf("execModule returned a transport error: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("reserved/injected keys must not fail the strict ad-hoc build: %q", resp.Error)
	}
	if !resp.Success || !resp.Test {
		t.Fatalf("expected a successful dry run, got success=%v test=%v results=%+v",
			resp.Success, resp.Test, resp.Results)
	}
}

// TestStrictParams_RelaxedAdHocExecPath is the strict_params:false counterpart:
// the same typo warns instead of failing — the state still builds and the dry
// run succeeds.
func TestStrictParams_RelaxedAdHocExecPath(t *testing.T) {
	a := newStrictExecAgent(t, false)

	resp, err := a.execModule(context.Background(), proto.ExecRequest{
		ID:     "telnet",
		Module: "pkg.removed",
		Args:   map[string]any{"nmae": "telnet", "test": true},
	})
	if err != nil {
		t.Fatalf("execModule returned a transport error: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("relaxed policy must not fail the build, got error %q", resp.Error)
	}
	if !resp.Success {
		t.Fatalf("relaxed ad-hoc dry run should succeed, got results=%+v", resp.Results)
	}
}
