package modules

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
)

// policyMsg is appended to every conformance failure so the fix is obvious.
//
// The migration ratchet reached ZERO (keystone spec §9 gate 3): every built-in
// state module now carries a modschema.Spec. There is no longer an
// unmigratedAllowlist — the exemption list was deleted in the final gate-close
// wave (cmd.run + service.enabled), and this test is now in its FINAL form:
// EVERY registered module MUST have a Spec, with no exemptions. A newly added
// state module gains its Spec in the very change that registers it; a
// Spec-less registration fails this test.
const policyMsg = "the doc-coverage gate (keystone spec §9 gate 3) is CLOSED: " +
	"every registered built-in state module MUST carry a modschema.Spec — there " +
	"are no exemptions. A new module gains its Spec in the same change that " +
	"registers it. Never register a Spec-less state module."

func TestDocCoverage_EveryModuleHasSpec(t *testing.T) {
	var missing []string
	for _, r := range registrations {
		if r.Spec == nil {
			missing = append(missing, r.Name)
			continue
		}
		if r.Spec.Module != r.Name {
			t.Errorf("registration %q carries a spec for module %q — they must match.\n%s",
				r.Name, r.Spec.Module, policyMsg)
		}
	}
	if len(missing) != 0 {
		t.Errorf("%d registered module(s) have no modschema.Spec: %v.\n%s",
			len(missing), missing, policyMsg)
	}

	// The tranche-0D baseline is 47 built-in state modules; every one is now
	// migrated. This pin catches an accidental registration-table shrink as
	// well as a regression that would drop a module's Spec.
	if len(registrations) != 47 {
		t.Fatalf("registrations count = %d, want 47 (the tranche-0D baseline)", len(registrations))
	}
}

func TestDocCoverage_EffectsByKind(t *testing.T) {
	for _, r := range registrations {
		if r.Spec == nil {
			continue
		}
		if err := modschema.ValidateEffects(r.Spec.Kind, r.Spec.Doc.Effects); err != nil {
			t.Errorf("module %q: %v\n%s", r.Name, err, policyMsg)
		}
	}
}
