package modules

import (
	"sort"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
)

// policyMsg is appended to every conformance failure so the fix is obvious.
const policyMsg = "the doc-coverage ratchet (keystone spec §9 gate 3) only SHRINKS: " +
	"a module gains a modschema.Spec in the very same change that removes its name " +
	"from unmigratedAllowlist below. Never add a name to grow this list back, and " +
	"never leave a migrated module's name in it."

// unmigratedAllowlist is every built-in state module that has NOT yet been
// migrated to a self-documenting schema (modschema.Spec). It started as the
// full 47-name registration table (tranche 0D, §12); pilot #1 (pkg.removed,
// tranche 0C) shrank it to 46, and the semantic-type pilot (tranche 0E,
// service.running + service.dead onto TriState) shrank it to 44. Every
// subsequent migration PR removes exactly the name(s) it migrates — never adds one.
var unmigratedAllowlist = []string{
	"archive.extracted",
	"cmd.run",
	"cron.absent",
	"cron.present",
	"file.absent",
	"file.append",
	"file.blockreplace",
	"file.comment",
	"file.copy",
	"file.directory",
	"file.keyvalue",
	"file.line",
	"file.managed",
	"file.recurse",
	"file.replace",
	"file.symlink",
	"file.touch",
	"file.uncomment",
	"git.cloned",
	"git.latest",
	"group.absent",
	"group.present",
	"host.absent",
	"host.present",
	"locale.present",
	"module.run",
	"mount.mounted",
	"pip.installed",
	"pkg.installed",
	"pkg.latest",
	"pkg.purged",
	"pkgrepo.managed",
	"service.enabled",
	"ssh_auth.absent",
	"ssh_auth.present",
	"sysctl.present",
	"test.configurable_test_state",
	"test.fail_without_changes",
	"test.nop",
	"test.ping",
	"test.succeed_with_changes",
	"timezone.system",
	"user.absent",
	"user.present",
}

func TestDocCoverage_Ratchet(t *testing.T) {
	sorted := append([]string(nil), unmigratedAllowlist...)
	sort.Strings(sorted)
	if !equalStrings(sorted, unmigratedAllowlist) {
		t.Fatalf("unmigratedAllowlist must be sorted.\n%s", policyMsg)
	}

	allowset := map[string]bool{}
	for _, n := range unmigratedAllowlist {
		allowset[n] = true
	}

	var migrated, unmigrated int
	for _, r := range registrations {
		hasSpec := r.Spec != nil
		inAllowlist := allowset[r.Name]
		switch {
		case hasSpec && inAllowlist:
			t.Errorf("module %q has a Spec but is still listed in unmigratedAllowlist — remove it.\n%s", r.Name, policyMsg)
		case !hasSpec && !inAllowlist:
			t.Errorf("module %q has no Spec but is missing from unmigratedAllowlist.\n%s", r.Name, policyMsg)
		}
		if hasSpec {
			migrated++
		} else {
			unmigrated++
		}
	}

	if len(registrations) != 47 {
		t.Fatalf("registrations count = %d, want 47 (the tranche-0D baseline)", len(registrations))
	}
	if unmigrated != len(unmigratedAllowlist) {
		t.Errorf("unmigrated registration count = %d, unmigratedAllowlist has %d entries — they must match.\n%s",
			unmigrated, len(unmigratedAllowlist), policyMsg)
	}
	if migrated+unmigrated != len(registrations) {
		t.Errorf("migrated(%d) + unmigrated(%d) != registrations(%d)", migrated, unmigrated, len(registrations))
	}
	// Pinned present state (tranche 0E): pkg.removed (0C) plus service.running
	// and service.dead (0E, the TriState semantic-type pilot) have shrunk the
	// list so far. This assertion is expected to need updating — by removing
	// names from unmigratedAllowlist, never by loosening this count — as each
	// subsequent migration PR lands.
	if migrated != 3 {
		t.Errorf("migrated module count = %d, want 3 (pkg.removed, service.running, service.dead as of tranche 0E).\n%s", migrated, policyMsg)
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

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
