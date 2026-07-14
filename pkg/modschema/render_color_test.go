package modschema

import (
	"regexp"
	"strings"
	"testing"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// colorTestInfo builds a ModuleInfo exercising every RenderText section, so the
// colorizer tests cover the full grammar.
func colorTestInfo() ModuleInfo {
	return ModuleInfo{
		Module:      "demo.managed",
		Kind:        "state",
		AlsoExecmod: true,
		Doc: Doc{
			Summary:     "Manage a demo resource.",
			Description: "Longer prose.\nWith a second line.",
			Effects: Effects{
				Check: "Reports whether the demo would change.",
				Apply: "Converges the demo.",
			},
			Examples: []Example{
				{Title: "Basic usage", Kind: "yaml", Code: "demo:\n  demo.managed:\n    - name: x", Explanation: "The simplest form."},
			},
			Notes: []Note{
				{Level: "info", Title: "An informational note", Body: "Body text."},
				{Level: "warning", Title: "A warning note"},
				{Level: "danger", Title: "A dangerous note"},
				{Title: "A level-less note"},
			},
			Divergences: []string{"differs from Salt in X"},
			SeeAlso:     []string{"demo.absent"},
		},
		Params: []Field{
			{Name: "name", GoType: "string", Required: true, Primary: true, Usage: "the resource name"},
			{Name: "mode", GoType: "string", SemanticType: "file_mode", Aliases: []string{"file_mode"}, HasDefault: true, Default: "0644"},
			{Name: "password", GoType: "string", Sensitive: true},
			{Name: "timeout", GoType: "time.Duration", Lazy: true, HasDefault: true, Default: "30s"},
		},
		SemTypes: []SemanticTypeInfo{
			{Name: "file_mode", Doc: "An octal file mode."},
		},
	}
}

// The load-bearing invariant: stripping the injected ANSI codes reproduces the
// plain render byte-for-byte, for a ModuleInfo exercising every section and for
// a RenderTextAll family document.
func TestColorizeDoc_StripIsLossless(t *testing.T) {
	mi := colorTestInfo()

	plain := RenderText(mi)
	if got := stripANSI(ColorizeDoc(plain)); got != plain {
		t.Errorf("stripANSI(ColorizeDoc(RenderText)) != RenderText\n got: %q\nwant: %q", got, plain)
	}

	family := RenderTextAll([]ModuleInfo{mi, mi})
	if got := stripANSI(ColorizeDoc(family)); got != family {
		t.Errorf("stripANSI(ColorizeDoc(RenderTextAll)) != RenderTextAll\n got: %q\nwant: %q", got, family)
	}
}

// Each grammar element gets its intended color.
func TestColorizeDoc_ColorsGrammar(t *testing.T) {
	out := ColorizeDoc(RenderText(colorTestInfo()))

	for _, want := range []string{
		// Header: module name bold cyan, kind + AlsoExecmod overlay dim.
		ansiModuleName + "demo.managed" + ansiReset,
		ansiDim + "(state) — also reachable as an execution module: salt['demo.managed']" + ansiReset,
		// Section headings bold yellow.
		ansiHeading + "Parameters:" + ansiReset,
		ansiHeading + "Effects:" + ansiReset,
		ansiHeading + "Examples:" + ansiReset,
		ansiHeading + "Notes:" + ansiReset,
		ansiHeading + "Divergences:" + ansiReset,
		ansiHeading + "See Also:" + ansiReset,
		// Parameter names bold green; the type dim; flags weighted.
		"  " + ansiParamName + "name" + ansiReset + " (" + ansiDim + "string" + ansiReset + ", " + ansiFlagWarn + "required" + ansiReset + ", " + ansiFlagOther + "primary" + ansiReset + ")",
		"  " + ansiParamName + "password" + ansiReset + " (" + ansiDim + "string" + ansiReset + ", " + ansiFlagDanger + "sensitive" + ansiReset + ")",
		// The semantic type replaces GoType on the param line and is listed
		// under Parameter Types.
		"  " + ansiParamName + "mode" + ansiReset + " (" + ansiDim + "file_mode" + ansiReset + ")",
		"  " + ansiParamName + "file_mode" + ansiReset + "\n",
		// aliases/default labels dim.
		"      " + ansiDim + "aliases:" + ansiReset + " file_mode",
		"      " + ansiDim + "default:" + ansiReset + " 0644",
		// Effects modes bold.
		"  " + ansiBold + "Check" + ansiReset,
		"  " + ansiBold + "Apply" + ansiReset,
		// Example title: number and kind dim, title bold.
		"  " + ansiDim + "1." + ansiReset + " " + ansiBold + "Basic usage" + ansiReset + " " + ansiDim + "[yaml]" + ansiReset,
		// Note levels by severity, titles bold. A level-less note (several
		// shipped docs carry them, rendered "[] Title") is still colorized.
		"  " + ansiNoteInfo + "[info]" + ansiReset + " " + ansiBold + "An informational note" + ansiReset,
		"  " + ansiFlagWarn + "[warning]" + ansiReset + " " + ansiBold + "A warning note" + ansiReset,
		"  " + ansiFlagDanger + "[danger]" + ansiReset + " " + ansiBold + "A dangerous note" + ansiReset,
		"  " + ansiDim + "[]" + ansiReset + " " + ansiBold + "A level-less note" + ansiReset,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("colorized doc missing %q\nin:\n%s", want, out)
		}
	}
}

// The family separator is dimmed and the header AFTER it is recognized — the
// header state machine must re-arm on every "---" rule.
func TestColorizeDoc_FamilySeparatorRearmsHeader(t *testing.T) {
	mi := colorTestInfo()
	out := ColorizeDoc(RenderTextAll([]ModuleInfo{mi, mi}))

	if !strings.Contains(out, ansiDim+"---"+ansiReset) {
		t.Errorf("family separator not dimmed:\n%s", out)
	}
	if got := strings.Count(out, ansiModuleName+"demo.managed"+ansiReset); got != 2 {
		t.Errorf("colored headers after separator = %d, want 2:\n%s", got, out)
	}
}

// Non-doc input — the bare sys.doc name index, arbitrary prose — passes
// through byte-identical: no line matches the grammar, nothing is injected.
func TestColorizeDoc_NonDocInputUntouched(t *testing.T) {
	for _, text := range []string{
		"cmd.run\nfile.managed\npkg.installed\nsys.doc",
		"",
		"some free-form text\n  with indented lines\n      and deeper ones",
	} {
		if got := ColorizeDoc(text); got != text {
			t.Errorf("non-doc input modified:\n got: %q\nwant: %q", got, text)
		}
	}
}

// Sensitive params never gain a default line even colorized (defense in depth —
// the colorizer must not invent content the renderer redacted).
func TestColorizeDoc_SensitiveStaysRedacted(t *testing.T) {
	mi := colorTestInfo()
	mi.Params[2].HasDefault = true
	mi.Params[2].Default = "hunter2"

	out := ColorizeDoc(RenderText(mi))
	if strings.Contains(out, "hunter2") {
		t.Errorf("sensitive default leaked into colorized output:\n%s", out)
	}
}

// Prose (summary/description) lines are never colorized, even when indented or
// when they end with a colon that isn't a renderer-owned heading.
func TestColorizeDoc_ProseUntouched(t *testing.T) {
	mi := ModuleInfo{
		Module: "demo.managed",
		Kind:   "state",
		Doc: Doc{
			Summary:     "Summary line:",
			Description: "  an indented description line\nAnother:",
		},
	}
	plain := RenderText(mi)
	out := ColorizeDoc(plain)
	for _, line := range []string{"Summary line:", "  an indented description line", "Another:"} {
		if !strings.Contains(out, "\n"+line+"\n") {
			t.Errorf("prose line %q was modified:\n%s", line, out)
		}
	}
}
