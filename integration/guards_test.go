//go:build integration

package integration

import (
	"testing"
)

// TestGuardsOnlyifUnless pins the onlyif/unless guard contract end-to-end
// through ad-hoc execution (the peel wraps ad-hoc runs with WrapAttributes,
// wiring runGuard as the GuardRunner):
//
//   - onlyif exiting non-zero → the state is a CLEAN no-op (success, no
//     change) — NOT an error. Pre-fix this reported
//     `error: onlyif ...: exit status 1` (found in 0.4.0 fleet ops).
//   - unless exiting non-zero → the state RUNS. Pre-fix this ERRORED,
//     making unless unusable for its primary purpose.
//   - unless exiting zero → clean skip.
func TestGuardsOnlyifUnless(t *testing.T) {
	// onlyif not met: clean no-op, no error, nothing changed.
	results := execCLI(t, "web-01", "file.touch", "/tmp/zester-guard-onlyif", "onlyif=test -f /nonexistent-zester-guard")
	r := requireSuccess(t, results, "web-01")
	for _, sr := range r.Results {
		if sr.Error != "" {
			t.Fatalf("onlyif-not-met must be a clean skip, got state error: %s", sr.Error)
		}
		if sr.Changed {
			t.Fatalf("onlyif-not-met must not change anything, state %s changed", sr.Name)
		}
	}
	if out := execInContainer(t, "web-01", []string{"sh", "-c", "test -f /tmp/zester-guard-onlyif && echo EXISTS || echo ABSENT"}); out != "ABSENT\n" && out != "ABSENT" {
		t.Fatalf("guarded state ran anyway: %q", out)
	}

	// unless not met (guard exits non-zero): the state RUNS — the exact
	// path that errored pre-fix.
	results = execCLI(t, "web-01", "file.touch", "/tmp/zester-guard-unless", "unless=test -f /nonexistent-zester-guard")
	r = requireSuccess(t, results, "web-01")
	changed := false
	for _, sr := range r.Results {
		if sr.Error != "" {
			t.Fatalf("unless with non-zero guard exit must RUN the state, got error: %s", sr.Error)
		}
		changed = changed || sr.Changed
	}
	if !changed {
		t.Fatal("unless with non-zero guard exit must run the state (expected a change)")
	}

	// unless met (guard exits zero): clean skip; the just-created file makes
	// the same guard now exit 0.
	results = execCLI(t, "web-01", "file.touch", "/tmp/zester-guard-unless-2", "unless=test -f /tmp/zester-guard-unless")
	r = requireSuccess(t, results, "web-01")
	for _, sr := range r.Results {
		if sr.Error != "" || sr.Changed {
			t.Fatalf("unless-met must be a clean no-op, got error=%q changed=%v", sr.Error, sr.Changed)
		}
	}
}
