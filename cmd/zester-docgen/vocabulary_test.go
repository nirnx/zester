package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
)

// vocabularyExceptions are the ONLY parameter keys allowed to carry different
// schemas in different modules. Every entry is a deliberate, documented
// divergence — almost always a Salt-parity name reused for a different
// concept — and each must name its reason. Adding a NEW module parameter that
// collides with an existing key under a different schema fails the gate below:
// reuse the existing vocabulary (same semantic type / same fragment), pick a
// non-colliding name, or add an entry here with a defensible reason.
var vocabularyExceptions = map[string]string{
	"mode": "file.line's `mode` is Salt's action selector (ensure/replace/insert/delete) — a different concept " +
		"from the FileMode permission `mode` of file.managed/file.directory; both names are Salt parity.",
	"text": "file.append's `text` is a scalar-tolerant LIST of lines (StringList), while test.echo's `text` " +
		"is the single string to echo; different surfaces, different concepts.",
	"gid": "group.present's `gid` is the numeric id to CREATE the group with (a name is meaningless there), " +
		"while user.present's `gid` is a GroupRef (existing group by id OR name); both are Salt parity.",
}

// TestParameterVocabularyConsistency is the cross-module vocabulary gate
// (review request 2026-07-13: "mode should always behave the same"): every
// parameter KEY — canonical name or alias — that appears in more than one
// module must resolve to the SAME schema fragment everywhere, unless the key
// has a documented exception above. It runs over the LIVE registries (state +
// exec + dispatch specials), so a new module cannot silently reuse a
// vocabulary word for a different shape.
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
		for _, u := range uses {
			frags[u.frag] = append(frags[u.frag], fmt.Sprintf("%s(%s)", u.module, u.canon))
		}
		if len(frags) <= 1 {
			continue
		}
		conflicting[k] = true
		if _, excepted := vocabularyExceptions[k]; excepted {
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

	for k, reason := range vocabularyExceptions {
		if reason == "" {
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
