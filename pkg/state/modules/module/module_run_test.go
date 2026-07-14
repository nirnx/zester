package modulemod

import (
	"testing"

	"github.com/nirnx/zester/pkg/state"
)

// TestIsReservedStateKey_MatchesReservedSetPlusName pins module.run's
// directive-vs-target disambiguation to exactly state.ReservedKeySet() ∪
// {"name"}. "name" is deliberately included here (module.run's classic target
// selector) even though it is excluded from the fleet-wide reserved union.
func TestIsReservedStateKey_MatchesReservedSetPlusName(t *testing.T) {
	reserved := state.ReservedKeySet()

	// "name" is not part of the fleet-wide reserved union...
	if _, ok := reserved["name"]; ok {
		t.Fatal(`state.ReservedKeySet() must EXCLUDE "name"; module.run adds it locally`)
	}

	// Build the expected answer set: reserved ∪ {"name"}.
	want := map[string]struct{}{"name": {}}
	for k := range reserved {
		want[k] = struct{}{}
	}

	// Every reserved key is treated as a directive.
	for k := range reserved {
		if !isReservedStateKey(k) {
			t.Errorf("isReservedStateKey(%q) = false, want true (reserved)", k)
		}
	}

	// "name" is included (the whole point of the local addition).
	if !isReservedStateKey("name") {
		t.Error(`isReservedStateKey("name") = false, want true`)
	}

	// The answer set is EXACTLY reserved ∪ {"name"}: no extras, and a spread of
	// genuine module params / target references are NOT reserved.
	if len(reservedModuleRunKeys) != len(want) {
		t.Errorf("reservedModuleRunKeys has %d entries, want %d (reserved + name)", len(reservedModuleRunKeys), len(want))
	}
	for k := range reservedModuleRunKeys {
		if _, ok := want[k]; !ok {
			t.Errorf("reservedModuleRunKeys has unexpected key %q", k)
		}
	}
	for _, k := range []string{"path", "content", "mode", "source", "user", "group", "file.touch", "cmd.run", "text"} {
		if isReservedStateKey(k) {
			t.Errorf("isReservedStateKey(%q) = true, want false (module param / target)", k)
		}
	}
}

// TestResolveModuleRunTarget_DottedKeyNotShadowedByReserved is a behavioral
// guard: a dotted target key (file.touch) is selected as the target, while
// reserved directives alongside it (require, order, name absent) are carried
// through, never mistaken for the target.
func TestResolveModuleRunTarget_DottedKeyNotShadowedByReserved(t *testing.T) {
	target, args, err := resolveModuleRunTarget(map[string]any{
		"file.touch": map[string]any{"path": "/tmp/x"},
		"require":    []any{"pkg.installed:nginx"},
		"order":      "last",
	})
	if err != nil {
		t.Fatal(err)
	}
	if target != "file.touch" {
		t.Errorf("target = %q, want file.touch", target)
	}
	if _, ok := args["require"]; !ok {
		t.Error("require directive should be carried through to inner args")
	}
	if _, ok := args["path"]; !ok {
		t.Error("path arg should be carried through to inner args")
	}
}
