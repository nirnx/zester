//go:build integration

package integration

import (
	"testing"
)

// TestStateApplyBareAliasesHighstate pins the Salt-parity alias end to end
// against the stack's baked states tree (its top.zy targets web-01, so the
// alias exercises the real fixture highstate — no shared-volume mutation):
// `zester '<peel>' state.apply` with NO state name runs the full highstate,
// identical to an explicit `state.highstate`, and the classic
// `state.apply test=true` dry-run form works (the key=value token is not
// mistaken for a state name — pre-alias it errored trying to compile a state
// literally named "test=true").
func TestStateApplyBareAliasesHighstate(t *testing.T) {
	bare := requireSuccess(t, execCLI(t, "web-01", "state.apply"), "web-01")
	if len(bare.Results) == 0 {
		t.Fatal("bare state.apply succeeded but ran no states — did not take the highstate branch")
	}

	// Identical to the explicit form: the same compiled state SET (result
	// order is not pinned across runs — sibling states without requisite
	// edges may land in different DAG-level positions).
	hs := requireSuccess(t, execCLI(t, "web-01", "state.highstate"), "web-01")
	if len(hs.Results) != len(bare.Results) {
		t.Fatalf("state.highstate ran %d states, bare state.apply ran %d — alias diverged", len(hs.Results), len(bare.Results))
	}
	bareNames := map[string]bool{}
	for _, sr := range bare.Results {
		bareNames[sr.Name] = true
	}
	for _, sr := range hs.Results {
		if !bareNames[sr.Name] {
			t.Errorf("state.highstate ran %q, absent from bare state.apply's set %v", sr.Name, bare.Results)
		}
	}

	// The classic Salt dry run: `state.apply test=true` is a highstate in test
	// mode, not an attempt to apply a state named "test=true".
	dry := requireSuccess(t, execCLI(t, "web-01", "state.apply", "test=true"), "web-01")
	if len(dry.Results) != len(bare.Results) {
		t.Errorf("state.apply test=true ran %d states, want the %d highstate states", len(dry.Results), len(bare.Results))
	}
}
