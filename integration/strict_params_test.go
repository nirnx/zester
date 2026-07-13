//go:build integration

package integration

import (
	"strings"
	"testing"
)

// TestSysDoc_ModuleRoundTrip runs `zester '<peel>' sys.doc pkg.installed` end to
// end: the peel resolves the module's registered ModuleInfo and renders it
// through modschema.RenderText, and the rendered doc rides
// ExecResponse.Results[0].Details["result"] back to the CLI. This pins the
// on-node self-documentation surface for a migrated (Spec-carrying) state module.
func TestSysDoc_ModuleRoundTrip(t *testing.T) {
	r := requireSuccess(t, execCLI(t, "web-01", "sys.doc", "pkg.installed"), "web-01")
	if len(r.Results) == 0 {
		t.Fatal("sys.doc pkg.installed returned no results")
	}
	doc := r.Results[0].Details["result"]
	// Header line: "pkg.installed (state)".
	if !strings.Contains(doc, "pkg.installed (state)") {
		t.Errorf("sys.doc pkg.installed missing the state header:\n%s", doc)
	}
	// The rendered anatomy carries a Parameters section (name primary) and the
	// Check/Apply Effects the module declared.
	for _, want := range []string{"Parameters:", "Effects:", "Check", "Apply"} {
		if !strings.Contains(doc, want) {
			t.Errorf("sys.doc pkg.installed missing %q in the render:\n%s", want, doc)
		}
	}
}

// TestSysDoc_UnifiedIndex runs bare `zester '<peel>' sys.doc`: the peel returns
// the unified index of every callable surface, which must span all three
// surface kinds — a state module, an execution function, and a dispatch special.
func TestSysDoc_UnifiedIndex(t *testing.T) {
	r := requireSuccess(t, execCLI(t, "web-01", "sys.doc"), "web-01")
	if len(r.Results) == 0 {
		t.Fatal("bare sys.doc returned no results")
	}
	index := r.Results[0].Details["result"]
	cases := map[string]string{
		"state module":     "pkg.installed",
		"exec function":    "pkg.version",
		"dispatch special": "state.apply",
	}
	for kind, name := range cases {
		if !strings.Contains(index, name) {
			t.Errorf("sys.doc index missing the %s %q:\n%s", kind, name, index)
		}
	}
}

// TestStrictParams_TypoFailsUnderDefault pins the strict flip end to end: with
// strict_params on by default, a typo'd parameter on a migrated module FAILS the
// build and the error names the module, the offending key, and the did-you-mean
// suggestion. The state never reaches Apply (test=True is belt-and-suspenders in
// case the guard ever regressed).
func TestStrictParams_TypoFailsUnderDefault(t *testing.T) {
	results := execCLI(t, "web-01", "pkg.installed", "name=zester-nonexistent-pkg", "nmae=x", "test=true")

	var got *cliResult
	for i := range results {
		if results[i].PeelID == "web-01" {
			got = &results[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("no result for web-01 in %d results: %+v", len(results), results)
	}
	if got.Success {
		t.Fatalf("a typo'd parameter must fail the build under default strict_params, got success: %+v", got)
	}
	for _, want := range []string{"pkg.installed", "nmae", "name"} {
		if !strings.Contains(got.Error, want) {
			t.Errorf("strict build error %q must mention %q", got.Error, want)
		}
	}
}

// TestZesterDoc_OfflineOnAdmin runs `zester doc file.managed` in the admin
// container. `zester doc` renders the embedded module docs (pkg/moduledoc) with
// no master and no NATS — a peel-only or operator box needs no config file — so
// this succeeds purely from the binary's embedded docdata.
func TestZesterDoc_OfflineOnAdmin(t *testing.T) {
	out := execInContainer(t, "admin", []string{"zester", "doc", "file.managed"})
	if !strings.Contains(out, "file.managed") {
		t.Errorf("zester doc file.managed did not render the module:\n%s", out)
	}
	// The offline render uses the SAME anatomy as sys.doc (a Parameters section).
	if !strings.Contains(out, "Parameters:") {
		t.Errorf("zester doc file.managed missing Parameters section:\n%s", out)
	}
}
