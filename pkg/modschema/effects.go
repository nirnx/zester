package modschema

import (
	"fmt"
	"strings"
)

// ValidateEffects checks that effects satisfies the documentation-coverage
// requirement for kind (§4): a state module must document Check and Apply
// (Revert is optional — an explicit "cannot revert" prose is encouraged but
// not enforced); an exec or dispatch module must document Execution. Exec and
// dispatch are phase-less surfaces — they must NOT carry fake Check/Apply/
// Revert prose for phases they don't have.
//
// This is the coverage-test-enforced helper referenced by the doc-coverage
// ratchet: every module with a registered Spec must pass it.
func ValidateEffects(kind Kind, effects Effects) error {
	var missing []string
	var fake []string

	switch kind {
	case KindState:
		if effects.Check == "" {
			missing = append(missing, "Check")
		}
		if effects.Apply == "" {
			missing = append(missing, "Apply")
		}
	case KindExec, KindDispatch:
		if effects.Execution == "" {
			missing = append(missing, "Execution")
		}
		if effects.Check != "" {
			fake = append(fake, "Check")
		}
		if effects.Apply != "" {
			fake = append(fake, "Apply")
		}
		if effects.Revert != "" {
			fake = append(fake, "Revert")
		}
	default:
		return fmt.Errorf("modschema: validateeffects: unknown kind %q", kind)
	}

	if len(missing) == 0 && len(fake) == 0 {
		return nil
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("missing required effect(s) %s for kind %q",
			strings.Join(missing, ", "), kind))
	}
	if len(fake) > 0 {
		parts = append(parts, fmt.Sprintf("phase-less kind %q must not document %s (no fake Check/Apply/Revert prose)",
			kind, strings.Join(fake, ", ")))
	}
	return fmt.Errorf("modschema: validateeffects: %s", strings.Join(parts, "; "))
}
