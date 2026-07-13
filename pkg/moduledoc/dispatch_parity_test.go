package moduledoc_test

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/moduledoc"
)

// TestDocdataCarriesDispatchSpecials pins the review fix: offline `zester doc`
// must answer the same surfaces live sys.doc answers — the dispatch specials
// (state.apply, facts.*, settings.*, event.send) are embedded in docdata.
func TestDocdataCarriesDispatchSpecials(t *testing.T) {
	for _, name := range []string{"state.apply", "state.highstate", "facts.get", "settings.items", "event.send"} {
		mi, ok := moduledoc.Lookup(name)
		if !ok {
			t.Errorf("docdata missing dispatch special %q (offline zester doc would say unknown module while sys.doc answers)", name)
			continue
		}
		if mi.Kind != modschema.KindDispatch {
			t.Errorf("%s: Kind = %q, want dispatch", name, mi.Kind)
		}
		if mi.Doc.Summary == "" || mi.Doc.Effects.Execution == "" {
			t.Errorf("%s: embedded doc incomplete (summary/execution)", name)
		}
	}
}
