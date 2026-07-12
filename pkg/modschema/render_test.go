package modschema_test

import (
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
)

// goldenModuleInfo is a self-contained ModuleInfo fixture exercising every
// RenderText section: summary, description, a required+primary param, a
// semantic-typed param, a sensitive param (redaction), parameter types,
// all four effect kinds, both example kinds, a note, divergences, and
// see-also. RenderText must depend only on its input, never ambient state
// (§7 — a live Spec.Info() and its embedded docdata projection render
// identically), so this fixture is built without any modschema.Compile call.
var goldenModuleInfo = modschema.ModuleInfo{
	Module: "demo.thing",
	Kind:   modschema.KindState,
	Doc: modschema.Doc{
		Summary:     "Ensure a demo thing exists.",
		Description: "`demo.thing` is a fixture module used only by RenderText's golden test.",
		Effects: modschema.Effects{
			Check:  "Checks whether the thing already exists.",
			Apply:  "Creates the thing.",
			Revert: "Removes the thing that Apply created.",
		},
		Examples: []modschema.Example{
			{
				Title:       "Create a thing",
				Kind:        "state",
				Explanation: "The name parameter selects the thing.",
				Code:        "my-thing:\n  demo.thing:\n    - name: widget\n",
			},
			{
				Title:       "Create a thing ad hoc",
				Kind:        "cli",
				Explanation: "The bare positional is the thing name.",
				Code:        "zester '*' demo.thing widget",
			},
		},
		Notes: []modschema.Note{
			{Level: "info", Title: "Fixture only", Body: "This module does not exist for real."},
		},
		Divergences: []string{"BD-1", "BD-6"},
		SeeAlso:     []string{"demo.other"},
	},
	Params: []modschema.Field{
		{
			Name:     "name",
			GoType:   "string",
			Usage:    "the thing's name",
			Required: true,
			Primary:  true,
		},
		{
			Name:         "mode",
			GoType:       "paramtypes.FileMode",
			SemanticType: "FileMode",
			Usage:        "the thing's mode",
			Lazy:         true,
			HasDefault:   true,
			Default:      "0644",
		},
		{
			Name:       "password",
			GoType:     "string",
			Usage:      "the thing's secret",
			Sensitive:  true,
			HasDefault: true,
			// Default intentionally left empty: the schema layer never
			// populates Default for a sensitive field (§2.5), and this
			// fixture pins that RenderText renders nothing else either.
		},
	},
	SemTypes: []modschema.SemanticTypeInfo{
		{Name: "FileMode", Doc: "Accepts an octal string or int; setuid/sticky honored."},
	},
}

const goldenModuleInfoText = `demo.thing (state)

Ensure a demo thing exists.

` + "`demo.thing`" + ` is a fixture module used only by RenderText's golden test.

Parameters:
  name (string, required, primary)
      the thing's name
  mode (FileMode, lazy)
      the thing's mode
      default: 0644 (lazy — applied by the module, not materialized here)
  password (string, sensitive)
      the thing's secret

Parameter Types:
  FileMode
      Accepts an octal string or int; setuid/sticky honored.

Effects:
  Check
    Checks whether the thing already exists.
  Apply
    Creates the thing.
  Revert
    Removes the thing that Apply created.

Examples:
  1. Create a thing [state]
     The name parameter selects the thing.
     my-thing:
       demo.thing:
         - name: widget
  2. Create a thing ad hoc [cli]
     The bare positional is the thing name.
     zester '*' demo.thing widget

Notes:
  [info] Fixture only
      This module does not exist for real.

Divergences: BD-1, BD-6

See Also: demo.other
`

func TestRenderText_Golden(t *testing.T) {
	got := modschema.RenderText(goldenModuleInfo)
	if got != goldenModuleInfoText {
		t.Errorf("RenderText mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, goldenModuleInfoText)
	}
}

func TestRenderText_Deterministic(t *testing.T) {
	first := modschema.RenderText(goldenModuleInfo)
	for range 5 {
		if again := modschema.RenderText(goldenModuleInfo); again != first {
			t.Fatalf("RenderText is not deterministic across calls")
		}
	}
}

func TestRenderText_SensitiveNeverLeaksValue(t *testing.T) {
	// Even if a caller mistakenly populates Default on a sensitive field (the
	// schema layer never does — §2.5 — but RenderText must not trust that as
	// its only line of defense), RenderText must still never render it.
	mi := modschema.ModuleInfo{
		Module: "demo.leak",
		Kind:   modschema.KindState,
		Doc:    modschema.Doc{Summary: "leak check"},
		Params: []modschema.Field{
			{Name: "password", GoType: "string", Sensitive: true, HasDefault: true, Default: "hunter2"},
		},
	}
	got := modschema.RenderText(mi)
	if strings.Contains(got, "hunter2") {
		t.Errorf("RenderText leaked a sensitive default:\n%s", got)
	}
}

func TestRenderText_AlsoExecmodAnnotation(t *testing.T) {
	mi := modschema.ModuleInfo{
		Module:      "cmd.run",
		Kind:        modschema.KindState,
		Doc:         modschema.Doc{Summary: "Run a command."},
		AlsoExecmod: true,
	}
	got := modschema.RenderText(mi)
	want := "cmd.run (state) — also reachable as an execution module: salt['cmd.run']\n"
	if !strings.HasPrefix(got, want) {
		t.Errorf("RenderText header = %q, want prefix %q", got, want)
	}
}

func TestRenderText_EmptySectionsOmitted(t *testing.T) {
	mi := modschema.ModuleInfo{
		Module: "demo.bare",
		Kind:   modschema.KindDispatch,
		Doc:    modschema.Doc{Summary: "Bare module with nothing else."},
	}
	got := modschema.RenderText(mi)
	for _, section := range []string{"Parameters:", "Parameter Types:", "Effects:", "Examples:", "Notes:", "Divergences:", "See Also:"} {
		if strings.Contains(got, section) {
			t.Errorf("RenderText rendered empty section %q:\n%s", section, got)
		}
	}
}
