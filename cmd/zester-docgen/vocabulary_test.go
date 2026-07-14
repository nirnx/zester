package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
)

// Parameter-vocabulary gate — TWO TIERS per keystone spec §13 (Amendment A1,
// maintainer-approved 2026-07-13). The contract boundary is the FAMILY:
// (module kind, family, parameter name), where family = the first dotted
// namespace segment.
//
//   - TIER 1 (within one (kind, family) scope): the same parameter name must
//     carry ONE contract across every member that exposes it — value shape,
//     alias set, primary-ness, requiredness, and default are all compared.
//     Usage TEXT is deliberately NOT compared at this tier: member-declared
//     keys (above all the primary, whose usage describes each member's own
//     target) carry contextual prose by design; canonical usage arrives
//     STRUCTURALLY with the family parameter components (§13 — one embedded
//     declaration = one usage string, identical by construction), not
//     textually before them. Divergence fails unless pinned in
//     inFamilyExceptions — each entry is either a permanent compatibility
//     exception (file.line's mode) or a migration-pending entry that the
//     family-component tranche deletes (the ratchet-to-zero pattern).
//
//   - TIER 2 (across scopes): the same SPELLING may legitimately mean
//     different things in unrelated families (§13: cross-family reuse is
//     forbidden for components, and cross-family meaning divergence is
//     allowed), so only the value SHAPE is compared — an incompatible grammar
//     under one spelling is user-hostile even when semantically legitimate,
//     and costs a pinned entry in crossFamilyExceptions.
//
// Both exception tables pin their exact participants and distinct-signature
// counts, so an exception covers ONLY the known divergence: a new module
// joining an excepted key — with any shape — fails until the pin is
// deliberately updated. Stale entries (no remaining conflict) also fail, so
// the tables can only shrink. Starlark modules load operator content at
// runtime and are inherently outside a CI gate; dispatch specials carry no
// params.
type vocabException struct {
	reason string
	// participants is the exact sorted "module(canonicalName)" set allowed to
	// diverge on this key.
	participants []string
	// distinct is the pinned count of distinct contract signatures (tier 1)
	// or value shapes (tier 2) the participants may spread across.
	distinct int
}

// inFamilyExceptions is TIER 1's table, keyed "kind/family/param".
var inFamilyExceptions = map[string]vocabException{
	"state/file/mode": {
		reason: "PERMANENT compatibility exception (spec §13): file.line's `mode` is Salt's action selector " +
			"(ensure/replace/insert/delete) — explicitly OUTSIDE the canonical file.* mode contract. Plus " +
			"MIGRATION-PENDING variance among the FileMode members (dir_mode alias only on file.directory; " +
			"member defaults 0644/0755) that the file.* mode component resolves via the member-supplied " +
			"default rule; this entry shrinks to the file.line divergence alone after that tranche.",
		participants: []string{"file.directory(mode)", "file.line(mode)", "file.managed(mode)"},
		distinct:     3,
	},
	"state/file/dir_mode": {
		reason: "MIGRATION-PENDING (§13): `dir_mode` is an ALIAS of `mode` on file.directory (Salt-compat " +
			"fallback source) but a STANDALONE parameter on file.recurse (creation mode for new directories) " +
			"— same FileMode shape and 0755 default, different declaring field. The file.* mode component " +
			"tranche resolves whether dir_mode stays a directory-scoped alias, a family param, or both; " +
			"delete this entry when that tranche lands.",
		participants: []string{"file.directory(mode)", "file.recurse(dir_mode)"},
		distinct:     2,
	},
	"state/file/source": {
		reason: "MIGRATION-PENDING (§13): `source` is required by file.copy and optional on " +
			"file.managed/file.recurse — requiredness becomes a member-supplied dimension of the file.* " +
			"source component; delete this entry when that tranche lands.",
		participants: []string{"file.copy(source)", "file.managed(source)", "file.recurse(source)"},
		distinct:     2,
	},
	"state/pkg/refresh": {
		reason: "MIGRATION-PENDING (§13): pkg.latest defaults `refresh` to true while pkg.installed has no " +
			"default — the pkg.* family component decides whether the default unifies or is member-supplied; " +
			"delete this entry when that tranche lands.",
		participants: []string{"pkg.installed(refresh)", "pkg.latest(refresh)"},
		distinct:     2,
	},
}

// crossFamilyExceptions is TIER 2's table, keyed on the bare parameter key.
var crossFamilyExceptions = map[string]vocabException{
	"gid": {
		reason: "Cross-family, legitimately different (§13): group.present's `gid` is the numeric id to " +
			"CREATE the group with (a name is meaningless there), while user.present's `gid` is a GroupRef " +
			"(existing group by id OR name); both are Salt parity.",
		participants: []string{"group.present(gid)", "user.present(gid)"},
		distinct:     2,
	},
	"text": {
		reason: "Cross-family, cross-kind, legitimately different (§13): file.append's `text` is a " +
			"scalar-tolerant LIST of lines (StringList), while test.echo's `text` is the single string to " +
			"echo.",
		participants: []string{"file.append(text)", "test.echo(text)"},
		distinct:     2,
	},
	"mode": {
		reason: "Name-global shadow of the state/file/mode tier-1 entry: `mode` is used only within " +
			"state/file, so this tier-2 entry exists solely because tier 2 compares the bare spelling " +
			"across everything; it shrinks away together with the tier-1 file.line divergence.",
		participants: []string{"file.directory(mode)", "file.line(mode)", "file.managed(mode)"},
		distinct:     2,
	},
}

// paramUse is one module's exposure of a parameter KEY (canonical name or
// alias), carrying the declaring field's canonical name and full signature.
type paramUse struct {
	key    string // the exposed key (canonical name or alias)
	kind   string
	family string
	module string
	canon  string
	shape  string // canonical JSON of the schema fragment
	sig    string // full tier-1 contract signature
}

// TestParameterVocabularyConsistency runs both tiers over the LIVE state and
// exec registries.
func TestParameterVocabularyConsistency(t *testing.T) {
	uses := collectParamUses(t)

	byKey := map[string][]paramUse{}
	for _, u := range uses {
		byKey[u.key] = append(byKey[u.key], u)
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	tier1Conflicting := map[string]bool{}
	tier2Conflicting := map[string]bool{}

	for _, k := range keys {
		all := byKey[k]

		// ---- TIER 1: within each (kind, family) scope, full signature. ----
		byScope := map[string][]paramUse{}
		for _, u := range all {
			scope := u.kind + "/" + u.family
			byScope[scope] = append(byScope[scope], u)
		}
		for scope, scoped := range byScope {
			sigs := map[string][]string{}
			participants := map[string]bool{}
			for _, u := range scoped {
				who := fmt.Sprintf("%s(%s)", u.module, u.canon)
				sigs[u.sig] = append(sigs[u.sig], who)
				participants[who] = true
			}
			if len(sigs) <= 1 {
				continue
			}
			exKey := scope + "/" + k
			tier1Conflicting[exKey] = true
			if exc, ok := inFamilyExceptions[exKey]; ok {
				assertPin(t, "in-family", exKey, exc, participants, len(sigs))
				continue
			}
			var detail []string
			for sig, mods := range sigs {
				sort.Strings(mods)
				detail = append(detail, fmt.Sprintf("  %s <- %s", sig, strings.Join(mods, ", ")))
			}
			sort.Strings(detail)
			t.Errorf("IN-FAMILY contract conflict (%s): key %q has %d distinct contracts within one family — "+
				"one family, one contract (spec §13). Embed the family component / align the declarations, or "+
				"add a pinned exception in inFamilyExceptions:\n%s", scope, k, len(sigs), strings.Join(detail, "\n"))
		}

		// ---- TIER 2: across scopes, shape only. ----
		shapes := map[string][]string{}
		participants := map[string]bool{}
		for _, u := range all {
			who := fmt.Sprintf("%s(%s)", u.module, u.canon)
			shapes[u.shape] = append(shapes[u.shape], who)
			participants[who] = true
		}
		if len(shapes) <= 1 {
			continue
		}
		tier2Conflicting[k] = true
		if exc, ok := crossFamilyExceptions[k]; ok {
			assertPin(t, "cross-family", k, exc, participants, len(shapes))
			continue
		}
		var detail []string
		for shape, mods := range shapes {
			sort.Strings(mods)
			detail = append(detail, fmt.Sprintf("  %s <- %s", shape, strings.Join(mods, ", ")))
		}
		sort.Strings(detail)
		t.Errorf("CROSS-FAMILY shape conflict: key %q has %d distinct value shapes across families — the "+
			"same spelling may mean different things (§13), but an incompatible GRAMMAR under one spelling "+
			"costs a pinned entry in crossFamilyExceptions:\n%s", k, len(shapes), strings.Join(detail, "\n"))
	}

	for k, exc := range inFamilyExceptions {
		if exc.reason == "" {
			t.Errorf("in-family exception %q has no reason — every exception must be justified", k)
		}
		if !tier1Conflicting[k] {
			t.Errorf("in-family exception %q no longer conflicts — delete the stale entry", k)
		}
	}
	for k, exc := range crossFamilyExceptions {
		if exc.reason == "" {
			t.Errorf("cross-family exception %q has no reason — every exception must be justified", k)
		}
		if !tier2Conflicting[k] {
			t.Errorf("cross-family exception %q no longer conflicts — delete the stale entry", k)
		}
	}
}

// assertPin verifies an exception covers ONLY its pinned divergence.
func assertPin(t *testing.T, tier, key string, exc vocabException, participants map[string]bool, distinct int) {
	t.Helper()
	got := make([]string, 0, len(participants))
	for who := range participants {
		got = append(got, who)
	}
	sort.Strings(got)
	want := append([]string(nil), exc.participants...)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s exception %q: participants changed — got %v, pinned %v. A new user of an excepted key "+
			"is NOT covered by the exception: follow the family contract, pick a non-colliding name, or "+
			"deliberately update the pin with a reason.", tier, key, got, want)
	}
	if distinct != exc.distinct {
		t.Errorf("%s exception %q: %d distinct signatures, pinned %d — update the pin deliberately", tier, key, distinct, exc.distinct)
	}
}

// collectParamUses walks the live registries and emits one paramUse per
// (module, exposed key) — canonical names AND aliases each count as a key.
func collectParamUses(t *testing.T) []paramUse {
	t.Helper()
	var out []paramUse
	add := func(mi modschema.ModuleInfo) {
		family, _, _ := strings.Cut(mi.Module, ".")
		for _, f := range mi.Params {
			frag, err := json.Marshal(f.JSONSchema)
			if err != nil {
				t.Fatalf("%s.%s: marshal fragment: %v", mi.Module, f.Name, err)
			}
			aliases := append([]string(nil), f.Aliases...)
			sort.Strings(aliases)
			sig := fmt.Sprintf("shape=%s aliases=%v primary=%v required=%v default=%v/%q",
				frag, aliases, f.Primary, f.Required, f.HasDefault, f.Default)
			for _, k := range append([]string{f.Name}, f.Aliases...) {
				out = append(out, paramUse{
					key:    k,
					kind:   string(mi.Kind),
					family: family,
					module: mi.Module,
					canon:  f.Name,
					shape:  string(frag),
					sig:    sig,
				})
			}
		}
	}
	stateReg := buildStateRegistry()
	for _, name := range stateModuleNames(stateReg) {
		if mi, ok := stateReg.Describe(name); ok {
			add(mi)
		}
	}
	execReg := buildExecmodRegistry()
	for _, name := range execmodSpecNames(execReg) {
		if mi, ok := execReg.Describe(name); ok {
			add(mi)
		}
	}
	return out
}
