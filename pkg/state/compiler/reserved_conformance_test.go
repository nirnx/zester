package compiler

import (
	"reflect"
	"testing"

	"github.com/nirnx/zester/pkg/state"
)

// TestCompilerConsumedKeysAreReserved is the §6 conformance belt: every
// reserved key the compiler actually consumes — the inverse ("_in") requisite
// keys and their forward targets, the same-state alias key/value, the
// names-expansion key, and the extend-merge requisite classification — must be
// a member of state.ReservedKeys(). This guarantees the compiler never
// silently consumes a key that pkg/state does not treat as reserved (which
// would slip past unknown-key validation and module.run disambiguation).
func TestCompilerConsumedKeysAreReserved(t *testing.T) {
	reserved := state.ReservedKeySet()
	assert := func(kind, key string) {
		t.Helper()
		if _, ok := reserved[key]; !ok {
			t.Errorf("compiler consumes %s %q, but it is not in state.ReservedKeys()", kind, key)
		}
	}

	for k, v := range inverseForward {
		assert("inverse-requisite key", k)
		assert("inverse-requisite forward target", v)
	}
	for k, v := range sameStateAlias {
		assert("same-state alias key", k)
		assert("same-state alias target", v)
	}
	assert("names-expansion key", namesKey)
	for k := range requisiteKeys { // merge.go extend-append classification
		assert("extend-merge requisite key", k)
	}
}

// TestInverseForwardDerivation pins that deriving inverseForward from
// state.CompilerKeys() reproduces the exact former literal map (behavior
// identical), and that it covers precisely the "_in" keys in CompilerKeys.
func TestInverseForwardDerivation(t *testing.T) {
	want := map[string]string{
		"require_in":   "require",
		"watch_in":     "watch",
		"onchanges_in": "onchanges",
		"onfail_in":    "onfail",
		"listen_in":    "watch",
		"prereq_in":    "prereq",
	}
	if !reflect.DeepEqual(inverseForward, want) {
		t.Fatalf("inverseForward = %v, want %v", inverseForward, want)
	}

	// Every "_in" key in CompilerKeys is covered, and nothing else is.
	nInKeys := 0
	for _, k := range state.CompilerKeys() {
		if len(k) > len(inSuffix) && k[len(k)-len(inSuffix):] == inSuffix {
			nInKeys++
			if _, ok := inverseForward[k]; !ok {
				t.Errorf("CompilerKeys has %q but inverseForward does not cover it", k)
			}
		}
	}
	if nInKeys != len(inverseForward) {
		t.Errorf("inverseForward has %d entries, CompilerKeys has %d _in keys", len(inverseForward), nInKeys)
	}
}

// TestNamesKeyDerivation pins the derived names-expansion key.
func TestNamesKeyDerivation(t *testing.T) {
	if namesKey != "names" {
		t.Errorf("namesKey = %q, want %q", namesKey, "names")
	}
}

// TestSameStateAlias pins the single same-state alias (listen → watch).
func TestSameStateAlias(t *testing.T) {
	want := map[string]string{"listen": "watch"}
	if !reflect.DeepEqual(sameStateAlias, want) {
		t.Errorf("sameStateAlias = %v, want %v", sameStateAlias, want)
	}
}
