package moduledoc_test

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/moduledoc"
)

func TestLoadError_EmbeddedDocdataIsValid(t *testing.T) {
	if err := moduledoc.LoadError(); err != nil {
		t.Fatalf("embedded docdata.json is invalid: %v", err)
	}
}

func TestLookup_KnownModule(t *testing.T) {
	mi, ok := moduledoc.Lookup("pkg.removed")
	if !ok {
		t.Fatal("Lookup(pkg.removed) = false, want true")
	}
	if mi.Module != "pkg.removed" {
		t.Errorf("Module = %q", mi.Module)
	}
	if mi.Kind != modschema.KindState {
		t.Errorf("Kind = %q, want state", mi.Kind)
	}
	if !mi.HasSpec {
		t.Error("HasSpec = false, want true")
	}
	if mi.Doc.Summary == "" {
		t.Error("Doc.Summary is empty")
	}
}

func TestLookup_UnknownModule(t *testing.T) {
	if _, ok := moduledoc.Lookup("no.such.module"); ok {
		t.Error("Lookup(no.such.module) = true, want false")
	}
}

func TestAll_ContainsKnownModuleSortedByName(t *testing.T) {
	all := moduledoc.All()
	if len(all) == 0 {
		t.Fatal("All() returned no modules")
	}
	found := false
	for i, mi := range all {
		if mi.Module == "pkg.removed" {
			found = true
		}
		if i > 0 && all[i-1].Module >= mi.Module {
			t.Errorf("All() not sorted: %q before %q", all[i-1].Module, mi.Module)
		}
	}
	if !found {
		t.Error("All() missing pkg.removed")
	}
}

func TestLookup_RendersThroughRenderText(t *testing.T) {
	mi, ok := moduledoc.Lookup("pkg.removed")
	if !ok {
		t.Fatal("Lookup(pkg.removed) failed")
	}
	text := modschema.RenderText(mi)
	if text == "" {
		t.Error("RenderText(embedded pkg.removed) is empty")
	}
}
