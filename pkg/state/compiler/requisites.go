package compiler

import (
	"fmt"

	"github.com/nirnx/zester/pkg/state"
)

// mergedStateMap is stateID -> module -> args list (single-key maps).
type mergedStateMap = map[string]map[string][]map[string]any

// inverseForward maps a Salt "_in" requisite to the forward requisite it
// injects onto its target. listen/listen_in are aliased to watch (Zester runs
// watch-triggered applies during the run rather than deferring to the end;
// the effect — apply-on-change — matches, only the timing differs).
var inverseForward = map[string]string{
	"require_in":   "require",
	"watch_in":     "watch",
	"onchanges_in": "onchanges",
	"onfail_in":    "onfail",
	"listen_in":    "watch",
	"prereq_in":    "prereq",
}

// sameStateAlias renames same-state requisite keys to their Zester equivalent.
var sameStateAlias = map[string]string{
	"listen": "watch",
}

// transformRequisites rewrites inverse ("_in") requisites and prereq ordering
// across the whole merged state set, in place. It must run after merge/extend
// and before building states.
//
// Steps:
//  1. Alias same-state keys (listen -> watch).
//  2. For every A that declares an "_in" requisite targeting B, inject the
//     corresponding forward requisite (A) onto B, then drop the "_in" key.
//  3. For every A that declares prereq: [B...], inject require: [A] onto B so
//     A is ordered before B. A keeps its prereq for the runtime gate.
func transformRequisites(states mergedStateMap) error {
	// Step 1: same-state aliases (listen -> watch), on the source state.
	for id, modules := range states {
		for mod, argsList := range modules {
			for _, arg := range argsList {
				for oldKey, newKey := range sameStateAlias {
					if v, ok := arg[oldKey]; ok {
						delete(arg, oldKey)
						injectRequisite(states, id, mod, newKey, state.ParseReqEntries(v))
					}
				}
			}
		}
	}

	// Step 2: inverse ("_in") requisites — inject forward requisite onto target.
	// Snapshot source refs first because injection mutates the map.
	type inv struct {
		fwdKey  string
		srcRef  string
		targets []string
	}
	var pending []inv
	for srcID, modules := range states {
		for srcMod, argsList := range modules {
			srcRef := srcMod + ":" + srcID
			for _, arg := range argsList {
				for inKey, fwdKey := range inverseForward {
					if v, ok := arg[inKey]; ok {
						delete(arg, inKey)
						pending = append(pending, inv{fwdKey, srcRef, state.ParseReqEntries(v)})
					}
				}
			}
		}
	}
	for _, p := range pending {
		for _, target := range p.targets {
			tID, tMod, err := splitRef(target)
			if err != nil {
				return fmt.Errorf("compiler: inverse requisite %q: %w", target, err)
			}
			injectRequisite(states, tID, tMod, p.fwdKey, []string{p.srcRef})
		}
	}

	// Step 3: prereq ordering — A runs before each prereq target B (B requires A).
	type ord struct {
		srcRef  string
		targets []string
	}
	var orders []ord
	for srcID, modules := range states {
		for srcMod, argsList := range modules {
			srcRef := srcMod + ":" + srcID
			for _, arg := range argsList {
				if v, ok := arg["prereq"]; ok {
					orders = append(orders, ord{srcRef, state.ParseReqEntries(v)})
				}
			}
		}
	}
	for _, o := range orders {
		for _, target := range o.targets {
			tID, tMod, err := splitRef(target)
			if err != nil {
				return fmt.Errorf("compiler: prereq %q: %w", target, err)
			}
			injectRequisite(states, tID, tMod, "require", []string{o.srcRef})
		}
	}

	return nil
}

// splitRef parses a "module:id" requisite reference into its id and module.
func splitRef(ref string) (id, module string, err error) {
	for i := len(ref) - 1; i >= 0; i-- {
		if ref[i] == ':' {
			return ref[i+1:], ref[:i], nil
		}
	}
	return "", "", fmt.Errorf("requisite target %q is not in module:id form", ref)
}

// injectRequisite adds refs to the requisite list under key on states[id][module],
// creating the state/module entry if absent, merging into an existing key arg
// map, and writing the (possibly reallocated) slice back into the map.
func injectRequisite(states mergedStateMap, id, module, key string, refs []string) {
	mods, ok := states[id]
	if !ok {
		mods = make(map[string][]map[string]any)
		states[id] = mods
	}
	argsList := mods[module]

	for _, arg := range argsList {
		if existing, ok := arg[key]; ok {
			arg[key] = append(toAnyList(existing), toAnySlice(refs)...)
			mods[module] = argsList
			return
		}
	}
	mods[module] = append(argsList, map[string]any{key: toAnySlice(refs)})
}

func toAnyList(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case nil:
		return nil
	default:
		return []any{t}
	}
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
