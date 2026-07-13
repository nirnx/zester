package modules

import (
	"reflect"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
)

// TestLookupDispatch_Precedence pins the structural precedence: an exact
// concrete entry beats its prefix family, an unrecognized subfunction falls
// through to the family catch-all, and a non-dispatch name misses entirely.
func TestLookupDispatch_Precedence(t *testing.T) {
	tests := []struct {
		module    string
		wantName  string
		wantMatch DispatchMatch
		wantFound bool
	}{
		// Concrete exact entries win.
		{"state.apply", "state.apply", DispatchExact, true},
		{"state.highstate", "state.highstate", DispatchExact, true},
		{"facts.get", "facts.get", DispatchExact, true},
		{"facts.items", "facts.items", DispatchExact, true},
		{"facts.keys", "facts.keys", DispatchExact, true},
		{"facts.set", "facts.set", DispatchExact, true}, // exact beats the facts. family
		{"settings.get", "settings.get", DispatchExact, true},
		{"settings.items", "settings.items", DispatchExact, true},
		{"settings.keys", "settings.keys", DispatchExact, true},
		{"pillar.get", "pillar.get", DispatchExact, true},
		{"pillar.items", "pillar.items", DispatchExact, true},
		{"pillar.keys", "pillar.keys", DispatchExact, true},
		{"event.send", "event.send", DispatchExact, true},
		// Unrecognized subfunctions fall through to the prefix-family catch-all.
		{"facts.bogus", "facts.", DispatchPrefix, true},
		{"settings.bogus", "settings.", DispatchPrefix, true},
		{"pillar.bogus", "pillar.", DispatchPrefix, true},
		// Non-dispatch names miss.
		{"pkg.installed", "", 0, false},
		{"cmd.run", "", 0, false},
		{"grains.item", "", 0, false},
		{"sys.list_functions", "", 0, false},
		{"factsX", "", 0, false}, // not "facts." — no dot
	}
	for _, tt := range tests {
		sp, ok := LookupDispatch(tt.module)
		if ok != tt.wantFound {
			t.Errorf("LookupDispatch(%q) found = %v, want %v", tt.module, ok, tt.wantFound)
			continue
		}
		if !ok {
			continue
		}
		if sp.Name != tt.wantName || sp.Match != tt.wantMatch {
			t.Errorf("LookupDispatch(%q) = {%q, %v}, want {%q, %v}",
				tt.module, sp.Name, sp.Match, tt.wantName, tt.wantMatch)
		}
	}
}

// TestIsReadOnlyDispatch pins the read-only classification that backs
// readOnlyModule: query surfaces read-only, mutating surfaces not, and the
// facts.set exact entry correctly overrides the read-only facts. family.
func TestIsReadOnlyDispatch(t *testing.T) {
	readOnly := []string{
		"facts.get", "facts.items", "facts.keys",
		"settings.get", "settings.items", "settings.keys",
		"pillar.get", "pillar.items", "pillar.keys",
		"facts.bogus", "settings.bogus", "pillar.bogus", // family catch-alls are read-only
	}
	for _, m := range readOnly {
		if !IsReadOnlyDispatch(m) {
			t.Errorf("IsReadOnlyDispatch(%q) = false, want true", m)
		}
	}
	mutating := []string{
		"state.apply", "state.highstate", "facts.set", "event.send",
	}
	for _, m := range mutating {
		if IsReadOnlyDispatch(m) {
			t.Errorf("IsReadOnlyDispatch(%q) = true, want false", m)
		}
	}
	// Non-dispatch names are never read-only via the table.
	for _, m := range []string{"pkg.installed", "cmd.run", "grains.item"} {
		if IsReadOnlyDispatch(m) {
			t.Errorf("IsReadOnlyDispatch(%q) = true, want false (not a dispatch surface)", m)
		}
	}
}

// TestDispatchTableStructure pins the invariants the dispatch sites rely on:
// prefix catch-alls come strictly after every concrete entry (so exact wins),
// names are unique, and the three families each have exactly one prefix entry.
func TestDispatchTableStructure(t *testing.T) {
	seen := map[string]bool{}
	sawPrefix := false
	for _, sp := range DispatchSpecials {
		if seen[sp.Name] {
			t.Errorf("duplicate dispatch name %q", sp.Name)
		}
		seen[sp.Name] = true
		switch sp.Match {
		case DispatchExact:
			if sawPrefix {
				t.Errorf("concrete entry %q appears after a prefix catch-all — exact must win", sp.Name)
			}
		case DispatchPrefix:
			sawPrefix = true
		}
	}
	for _, fam := range []string{"facts.", "settings.", "pillar."} {
		if !seen[fam] {
			t.Errorf("missing prefix-family catch-all %q", fam)
		}
	}
}

// TestDispatchNames returns only the concrete (callable) names, sorted, with no
// prefix catch-alls.
func TestDispatchNames(t *testing.T) {
	want := []string{
		"event.send",
		"facts.get", "facts.items", "facts.keys", "facts.set",
		"pillar.get", "pillar.items", "pillar.keys",
		"settings.get", "settings.items", "settings.keys",
		"state.apply", "state.highstate",
	}
	got := DispatchNames()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DispatchNames() = %v\nwant %v", got, want)
	}
}

// TestDispatchInfo returns docs for concrete surfaces only; a prefix-family
// match (unrecognized subfunction) is not an addressable documentation target.
func TestDispatchInfo(t *testing.T) {
	mi, ok := DispatchInfo("facts.get")
	if !ok {
		t.Fatal("DispatchInfo(facts.get) not found")
	}
	if mi.Module != "facts.get" || mi.Kind != modschema.KindDispatch || mi.Doc.Summary == "" {
		t.Errorf("DispatchInfo(facts.get) = %+v", mi)
	}
	if mi.HasSpec {
		t.Error("dispatch ModuleInfo should not claim HasSpec")
	}
	if _, ok := DispatchInfo("facts.bogus"); ok {
		t.Error("DispatchInfo(facts.bogus) should be false (prefix family is not addressable)")
	}
	if _, ok := DispatchInfo("pkg.installed"); ok {
		t.Error("DispatchInfo(pkg.installed) should be false (not a dispatch surface)")
	}
}

// TestDispatchDocsEffectsByKind enforces the §4 coverage rule for every row:
// dispatch surfaces document Execution and never carry Check/Apply/Revert prose.
func TestDispatchDocsEffectsByKind(t *testing.T) {
	for _, sp := range DispatchSpecials {
		if err := modschema.ValidateEffects(modschema.KindDispatch, sp.Doc.Effects); err != nil {
			t.Errorf("dispatch special %q: %v", sp.Name, err)
		}
		if sp.Doc.Summary == "" {
			t.Errorf("dispatch special %q: empty Doc.Summary", sp.Name)
		}
	}
}
