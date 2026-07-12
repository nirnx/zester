package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
)

func samplePkgRemovedInfo(t *testing.T) modschema.ModuleInfo {
	t.Helper()
	reg := buildStateRegistry()
	mi, ok := reg.Describe("pkg.removed")
	if !ok {
		t.Fatal("pkg.removed has no registered spec")
	}
	return mi
}

func TestWriteModulePage_RefusesMarkerlessOverwriteWithoutClaim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pkg-removed.mdx")
	original := "---\ntitle: hand-written\n---\n\nDo not touch me.\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	mi := samplePkgRemovedInfo(t)

	err := writeModulePage(path, mi, false)
	if err == nil {
		t.Fatal("writeModulePage: expected refusal for markerless existing page, got nil error")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Errorf("markerless page was modified despite refusal:\n%s", got)
	}
}

func TestWriteModulePage_ClaimAdoptsMarkerlessPage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pkg-removed.mdx")
	original := "---\ntitle: hand-written\n---\n\nDo not touch me.\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	mi := samplePkgRemovedInfo(t)

	if err := writeModulePage(path, mi, true); err != nil {
		t.Fatalf("writeModulePage with claim=true: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !hasManagedMarker(got) {
		t.Error("claimed page does not carry the managed marker")
	}
	if strings.Contains(string(got), "Do not touch me.") {
		t.Error("claimed page still contains the old hand-written body")
	}
}

func TestWriteModulePage_RegeneratesAlreadyManagedPageWithoutClaim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pkg-removed.mdx")
	mi := samplePkgRemovedInfo(t)

	// First write must claim (markerless).
	if err := writeModulePage(path, mi, true); err != nil {
		t.Fatal(err)
	}
	firstGen, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// A second run with claim=false must succeed (marker already present) and
	// be byte-identical (deterministic).
	if err := writeModulePage(path, mi, false); err != nil {
		t.Fatalf("writeModulePage on an already-managed page without claim: %v", err)
	}
	secondGen, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstGen) != string(secondGen) {
		t.Errorf("regeneration is not deterministic:\n--- first ---\n%s\n--- second ---\n%s", firstGen, secondGen)
	}
}

func TestWriteModulePage_NewFileNeedsNoClaimGuardBypass(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "brand-new.mdx")
	mi := samplePkgRemovedInfo(t)
	if err := writeModulePage(path, mi, false); err != nil {
		t.Fatalf("writeModulePage on a nonexistent file should not require --claim: %v", err)
	}
}

func TestRenderModulePage_PkgRemoved_AnatomySections(t *testing.T) {
	mi := samplePkgRemovedInfo(t)
	got, err := renderModulePage(mi)
	if err != nil {
		t.Fatalf("renderModulePage: %v", err)
	}

	mustContain := []string{
		`title: "pkg.removed"`,
		managedMarker("pkg.removed"),
		"**Source**: `pkg/state/modules/pkg_removed.go`",
		"## Parameters",
		"| `name` | `string` |",
		requisitesBoilerplate,
		"## Effects",
		"### Check",
		"### Apply",
		"### Revert",
		"## Examples",
		"### Remove a package by name",
		"```yaml",
		"### Remove a package ad hoc",
		"```bash",
		"## Notes",
		"## Divergences",
		"- BD-6",
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("rendered page missing %q\n--- full page ---\n%s", want, got)
		}
	}
	// pkg.removed has no semantic-typed params and no SeeAlso: those sections
	// must be omitted entirely.
	for _, section := range []string{"## Parameter Types", "## See Also"} {
		if strings.Contains(got, section) {
			t.Errorf("rendered page has empty section %q that should be omitted", section)
		}
	}
}

// TestRenderModulePage_RejectsJSXInPoisonedDoc is the §4/§8 assertNoJSX guard:
// a Doc whose prose carries raw MDX/JSX (an `import` line or an unescaped
// `<Component>` open tag — the shape a hand-page migration could leave behind)
// must FAIL generation rather than emit a page that breaks the website build.
func TestRenderModulePage_RejectsJSXInPoisonedDoc(t *testing.T) {
	cases := []struct {
		name        string
		description string
	}{
		{"esm-import", "import Callout from '@site/callout'\n\nSome prose."},
		{"jsx-component", "See the <Callout type=\"warn\">caveat</Callout> below."},
		{"self-closing-jsx", "A tab set: <Tabs />"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mi := modschema.ModuleInfo{
				Module: "demo.poison",
				Kind:   modschema.KindState,
				Doc:    modschema.Doc{Summary: "demo", Description: tc.description},
			}
			if _, err := renderModulePage(mi); err == nil {
				t.Fatalf("renderModulePage: expected assertNoJSX to fail generation for %q", tc.description)
			}
		})
	}
}

// TestAssertNoJSX_ExemptsLiteralAndSafeForms pins the guard's exemptions:
// fenced code, inline `code` spans, backslash-escaped `<`, and plain lowercase
// HTML tags are all legitimate CommonMark and must NOT trip it.
func TestAssertNoJSX_ExemptsLiteralAndSafeForms(t *testing.T) {
	ok := []string{
		"plain prose with no angle brackets",
		"an inline `<Component>` in a code span is literal",
		"```jsx\n<Component />\n```",
		"a backslash-escaped \\<Component stays prose",
		"a lowercase <div> tag is valid MDX",
		"you can import the key mid-sentence; only a leading import line is rejected",
	}
	for _, s := range ok {
		if err := assertNoJSX("demo.mod", s); err != nil {
			t.Errorf("assertNoJSX rejected safe content %q: %v", s, err)
		}
	}
	bad := []string{
		"import Thing from 'x'",
		"   import Thing from 'x'",
		"prose then a raw <Component> tag",
	}
	for _, s := range bad {
		if err := assertNoJSX("demo.mod", s); err == nil {
			t.Errorf("assertNoJSX accepted JSX-bearing content %q", s)
		}
	}
}

func TestRenderModulePage_UnresolvableSeeAlsoFailsGeneration(t *testing.T) {
	mi := modschema.ModuleInfo{
		Module: "demo.thing",
		Kind:   modschema.KindState,
		Doc: modschema.Doc{
			Summary: "demo",
			SeeAlso: []string{"nonexistent.module"},
		},
	}
	if _, err := renderModulePage(mi); err == nil {
		t.Fatal("renderModulePage: expected error for an unresolvable See Also target")
	}
}
