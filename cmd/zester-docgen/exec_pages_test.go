package main

import (
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
)

// execOnlyInfos reproduces main.go's execution-only filter: every
// spec-registered execution function whose name is not also a state module
// (cmd.run — the dual-surface module documented by its state page).
func execOnlyInfos(t *testing.T) []modschema.ModuleInfo {
	t.Helper()
	stateReg := buildStateRegistry()
	stateNames := map[string]bool{}
	for _, n := range stateReg.Modules() {
		stateNames[n] = true
	}
	execReg := buildExecmodRegistry()
	var out []modschema.ModuleInfo
	for _, name := range execmodSpecNames(execReg) {
		if stateNames[name] {
			continue
		}
		mi, ok := execReg.Describe(name)
		if !ok {
			t.Fatalf("execReg.Describe(%q) failed", name)
		}
		out = append(out, mi)
	}
	return out
}

// TestExecPageOrder_CoversExecOnly pins that execPageOrder places EXACTLY the
// execution-only specs — a new execution function without a page slot, or a
// stale slot for a removed function, fails generation (the exec analogue of
// moduleToSlug's coverage guards).
func TestExecPageOrder_CoversExecOnly(t *testing.T) {
	infos := execOnlyInfos(t)

	inOrder := map[string]bool{}
	for _, n := range execPageOrder {
		inOrder[n] = true
	}
	have := map[string]bool{}
	for _, mi := range infos {
		have[mi.Module] = true
		if !inOrder[mi.Module] {
			t.Errorf("execution module %q has no slot in execPageOrder", mi.Module)
		}
	}
	for _, n := range execPageOrder {
		if !have[n] {
			t.Errorf("execPageOrder lists %q but it has no execution-only spec", n)
		}
	}
	if len(execPageOrder) != len(infos) {
		t.Errorf("execPageOrder has %d entries, execution-only specs = %d", len(execPageOrder), len(infos))
	}

	// cmd.run is dual-surface and must NOT appear on the exec page.
	if inOrder["cmd.run"] {
		t.Error("execPageOrder must not include cmd.run (documented on its state page)")
	}
}

// TestExecModuleSet_MatchesOrder pins that the See-Also resolution set is
// derived from execPageOrder (no drift).
func TestExecModuleSet_MatchesOrder(t *testing.T) {
	if len(execModuleSet) != len(execPageOrder) {
		t.Fatalf("execModuleSet has %d entries, execPageOrder %d", len(execModuleSet), len(execPageOrder))
	}
	for _, n := range execPageOrder {
		if !execModuleSet[n] {
			t.Errorf("execModuleSet missing %q", n)
		}
	}
}

// TestRenderExecModulesPage_Anatomy renders the combined execution-modules page
// from the live specs and checks its structure: title, a per-function banner
// with the correct Source line, the Execution effect, a managed marker, and NO
// cmd.run banner (dual-surface). It also confirms the page carries no raw JSX.
func TestRenderExecModulesPage_Anatomy(t *testing.T) {
	infos := execOnlyInfos(t)
	page, err := renderExecModulesPage(infos)
	if err != nil {
		t.Fatalf("renderExecModulesPage: %v", err)
	}

	for _, want := range []string{
		"title: \"Execution Modules\"",
		"{/* zester-docgen:managed module=\"test.echo\" */}",
		// Banners carry explicit stable anchors (Fumadocs [#id] heading ids).
		"## `test.echo` [#test-echo]",
		"## `pkg.version` [#pkg-version]",
		"## `sys.doc` [#sys-doc]",
		"## `sys.list_functions` [#sys-list-functions]",
		"**Source**: `pkg/execmod/builtins.go`",
		"**Source**: `pkg/execmod/sysdoc.go`", // sys.doc / sys.list_functions
		// M5: each function's own sections nest one level BELOW its "##
		// `module`" banner, so Effects is "### Effects" and its Execution
		// sub-heading is "#### Execution" — never a sibling H2/H3 of the banner.
		"### Effects",
		"#### Execution",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("rendered exec page missing %q", want)
		}
	}

	// Notes and Divergences are dropped from ALL rendered pages (maintainer
	// decision) — they remain on the terminal sys.doc / `zester doc` surface.
	for _, banned := range []string{"# Notes", "# Divergences"} {
		if strings.Contains(page, banned) {
			t.Errorf("exec page must not render a Notes/Divergences section (found %q)", banned)
		}
	}

	if strings.Contains(page, "## `cmd.run`") {
		t.Error("exec page must not carry a cmd.run banner (dual-surface, on the state page)")
	}
	// No function section ever renders as a bare H2 — every one of them nests
	// under its own "## `module`" banner (M5: no colliding H2s).
	for line := range strings.SplitSeq(page, "\n") {
		switch line {
		case "## Parameters", "## Effects", "## Examples", "## Notes", "## Divergences", "## See Also", "## Parameter Types":
			t.Errorf("exec page section %q must not render as an H2 (collides with the module banner)", line)
		}
	}
	// Exec surfaces have no requisites boilerplate and no Check/Apply/Revert
	// effect headings (the exact heading form, not an example title that happens
	// to start with "Check").
	for _, unwanted := range []string{"#### Check\n", "#### Apply\n", "#### Revert\n", "Dependencies & Requisites"} {
		if strings.Contains(page, unwanted) {
			t.Errorf("exec page must not contain %q (phase-less surface)", unwanted)
		}
	}
}

// TestExecSourcePath pins the Source-line mapping for the two shared files.
func TestExecSourcePath(t *testing.T) {
	cases := map[string]string{
		"test.echo":          "pkg/execmod/builtins.go",
		"pkg.version":        "pkg/execmod/builtins.go",
		"grains.items":       "pkg/execmod/builtins.go",
		"sys.doc":            "pkg/execmod/sysdoc.go",
		"sys.list_functions": "pkg/execmod/sysdoc.go",
	}
	for module, want := range cases {
		if got := execSourcePath(module); got != want {
			t.Errorf("execSourcePath(%q) = %q, want %q", module, got, want)
		}
	}
}
