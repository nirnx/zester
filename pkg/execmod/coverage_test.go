package execmod_test

import (
	"testing"

	"github.com/nirnx/zester/pkg/execmod"
	"github.com/nirnx/zester/pkg/modschema"
)

// execPolicyMsg is appended to every coverage failure so the fix is obvious.
//
// This is the execution-module analogue of the state registry's doc-coverage
// gate (keystone spec §9 gate 3), and it is in its FINAL form: EVERY function
// registered in DefaultRegistry MUST carry a modschema.Spec — there are no
// exemptions and no allowlist, because every built-in execution function has
// been migrated. A new execution function gains its Spec in the same change
// that registers it (RegisterSpec, never the plain Register).
const execPolicyMsg = "the execmod doc-coverage gate is CLOSED: every function in " +
	"DefaultRegistry MUST carry a modschema.Spec (register it via RegisterSpec, not " +
	"Register). A new execution function gains its Spec in the same change that adds it."

// TestExecDocCoverage_EveryFunctionHasSpec pins that every built-in execution
// function is self-documenting: DefaultRegistry registers all of them via
// RegisterSpec, so Describe must succeed for every name it exposes.
func TestExecDocCoverage_EveryFunctionHasSpec(t *testing.T) {
	r := execmod.DefaultRegistry()

	var missing []string
	for _, name := range r.Names() {
		mi, ok := r.Describe(name)
		if !ok {
			missing = append(missing, name)
			continue
		}
		if mi.Module != name {
			t.Errorf("Describe(%q).Module = %q — they must match.\n%s", name, mi.Module, execPolicyMsg)
		}
		if mi.Kind != modschema.KindExec {
			t.Errorf("execution function %q has Kind %q, want exec.\n%s", name, mi.Kind, execPolicyMsg)
		}
	}
	if len(missing) != 0 {
		t.Errorf("%d execution function(s) have no modschema.Spec: %v.\n%s", len(missing), missing, execPolicyMsg)
	}

	// SpecNames must be exactly the callable-function set — no function without a
	// spec, no spec without a function.
	if got, want := len(r.SpecNames()), len(r.Names()); got != want {
		t.Errorf("SpecNames count = %d, Names count = %d — every function must have a spec and vice versa.\n%s",
			got, want, execPolicyMsg)
	}

	// The baseline is the 16 built-in execution functions (test.*, pkg.version,
	// pkg.list_pkgs, service.*, disk.usage, cmd.run, grains.*, sys.list_functions,
	// sys.doc). This pin catches an accidental registration-table shrink as well
	// as a regression that drops a function's Spec.
	if got := len(r.SpecNames()); got != 16 {
		t.Fatalf("DefaultRegistry has %d spec-registered functions, want 16", got)
	}
}

// TestExecDocCoverage_EffectsByKind pins that every built-in execution spec
// satisfies the Effects-by-kind requirement (exec surfaces document Execution
// and must NOT carry fake Check/Apply/Revert prose).
func TestExecDocCoverage_EffectsByKind(t *testing.T) {
	r := execmod.DefaultRegistry()
	for _, name := range r.SpecNames() {
		mi, ok := r.Describe(name)
		if !ok {
			t.Fatalf("SpecNames listed %q but Describe failed", name)
		}
		if err := modschema.ValidateEffects(mi.Kind, mi.Doc.Effects); err != nil {
			t.Errorf("execution function %q: %v\n%s", name, err, execPolicyMsg)
		}
	}
}
