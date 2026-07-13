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

// vocabularyExceptions are the ONLY parameter keys allowed to carry different
// schemas in different modules. Every entry is a deliberate, documented
// divergence — almost always a Salt-parity name reused for a different
// concept — and each PINS its exact participants and distinct-shape count, so
// the exception covers only the known divergence: a NEW module joining an
// excepted key (with any shape) or a participant changing shape count fails
// the gate and forces a deliberate pin update here. Adding a NEW parameter
// that collides with an existing key under a different schema fails the gate
// below: reuse the existing vocabulary (same semantic type), pick a
// non-colliding name, or add an entry here with a defensible reason.
type vocabException struct {
	reason string
	// participants is the exact sorted "module(canonicalName)" set allowed to
	// use this key, across ALL its shapes.
	participants []string
	// distinctShapes is the number of distinct schema fragments the
	// participants are allowed to spread across.
	distinctShapes int
}

var vocabularyExceptions = map[string]vocabException{
	"mode": {
		reason: "file.line's `mode` is Salt's action selector (ensure/replace/insert/delete) — a different " +
			"concept from the FileMode permission `mode` of file.managed/file.directory; both names are Salt parity.",
		participants:   []string{"file.directory(mode)", "file.line(mode)", "file.managed(mode)"},
		distinctShapes: 2,
	},
	"gid": {
		reason: "group.present's `gid` is the numeric id to CREATE the group with (a name is meaningless " +
			"there), while user.present's `gid` is a GroupRef (existing group by id OR name); both are Salt parity.",
		participants:   []string{"group.present(gid)", "user.present(gid)"},
		distinctShapes: 2,
	},
	"text": {
		reason: "file.append's `text` is a scalar-tolerant LIST of lines (StringList), while test.echo's " +
			"`text` is the single string to echo; different surfaces, different concepts.",
		participants:   []string{"file.append(text)", "test.echo(text)"},
		distinctShapes: 2,
	},
}

// TestParameterVocabularyConsistency is the cross-module vocabulary gate
// (review request 2026-07-13: "mode should always behave the same"): every
// parameter KEY — canonical name or alias — that appears in more than one
// module must resolve to the SAME schema fragment everywhere, unless the key
// has a documented exception above. It runs over the LIVE state and exec
// registries (dispatch specials carry no params; Starlark modules load
// operator content at runtime and are inherently outside a CI gate), so a new
// BUILT-IN module cannot silently reuse a vocabulary word for a different
// shape.
//
// The gate also refuses STALE exceptions: an entry whose key no longer
// conflicts must be deleted, so the exception table can only shrink toward
// zero, never rot.
func TestParameterVocabularyConsistency(t *testing.T) {
	infos := liveModuleInfos(t)

	type use struct {
		module string
		canon  string
		frag   string
	}
	byKey := map[string][]use{}
	for _, mi := range infos {
		for _, f := range mi.Params {
			frag, err := json.Marshal(f.JSONSchema)
			if err != nil {
				t.Fatalf("%s.%s: marshal fragment: %v", mi.Module, f.Name, err)
			}
			keys := append([]string{f.Name}, f.Aliases...)
			for _, k := range keys {
				byKey[k] = append(byKey[k], use{mi.Module, f.Name, string(frag)})
			}
		}
	}

	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	conflicting := map[string]bool{}
	for _, k := range keys {
		uses := byKey[k]
		frags := map[string][]string{}
		participants := map[string]bool{}
		for _, u := range uses {
			who := fmt.Sprintf("%s(%s)", u.module, u.canon)
			frags[u.frag] = append(frags[u.frag], who)
			participants[who] = true
		}
		if len(frags) <= 1 {
			continue
		}
		conflicting[k] = true
		if exc, excepted := vocabularyExceptions[k]; excepted {
			// The exception covers ONLY the pinned divergence: participants and
			// shape count must match exactly, so a new module joining the key —
			// or a participant growing a third shape — still fails (the "mode"
			// masking hole from the PR-19 adversarial review).
			got := make([]string, 0, len(participants))
			for who := range participants {
				got = append(got, who)
			}
			sort.Strings(got)
			want := append([]string(nil), exc.participants...)
			sort.Strings(want)
			if !slices.Equal(got, want) {
				t.Errorf("vocabulary exception %q: participants changed — got %v, pinned %v. A new user of an "+
					"excepted key is NOT covered by the exception: reuse the pinned shape's semantic type under a "+
					"non-colliding name, or deliberately update the pin with a reason.", k, got, want)
			}
			if len(frags) != exc.distinctShapes {
				t.Errorf("vocabulary exception %q: %d distinct shapes, pinned %d — update the pin deliberately", k, len(frags), exc.distinctShapes)
			}
			continue
		}
		var detail []string
		for frag, mods := range frags {
			sort.Strings(mods)
			detail = append(detail, fmt.Sprintf("  %s <- %s", frag, strings.Join(mods, ", ")))
		}
		sort.Strings(detail)
		t.Errorf("parameter vocabulary conflict: key %q has %d distinct schemas across modules — "+
			"same name must mean the same shape everywhere. Reuse the existing vocabulary (same "+
			"semantic type), pick a non-colliding name, or add a documented exception in "+
			"vocabulary_test.go:\n%s", k, len(frags), strings.Join(detail, "\n"))
	}

	for k, exc := range vocabularyExceptions {
		if exc.reason == "" {
			t.Errorf("vocabulary exception %q has no reason — every exception must be justified", k)
		}
		if !conflicting[k] {
			t.Errorf("vocabulary exception %q no longer conflicts — delete the stale entry", k)
		}
	}
}

// liveModuleInfos collects ModuleInfo from the live state and execmod
// registries (the same sources docgen renders from). Dispatch specials carry
// no params, so they are irrelevant to the vocabulary.
func liveModuleInfos(t *testing.T) []modschema.ModuleInfo {
	t.Helper()
	var infos []modschema.ModuleInfo
	stateReg := buildStateRegistry()
	for _, name := range stateModuleNames(stateReg) {
		if mi, ok := stateReg.Describe(name); ok {
			infos = append(infos, mi)
		}
	}
	execReg := buildExecmodRegistry()
	for _, name := range execmodSpecNames(execReg) {
		if mi, ok := execReg.Describe(name); ok {
			infos = append(infos, mi)
		}
	}
	return infos
}
