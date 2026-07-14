package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
)

func samplePkgInfos(t *testing.T) []modschema.ModuleInfo {
	t.Helper()
	return familyInfos(t, "pkg")
}

func TestWriteFamilyPage_RefusesMarkerlessOverwriteWithoutClaim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pkg.mdx")
	original := "---\ntitle: hand-written\n---\n\nDo not touch me.\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	err := writeFamilyPage(path, "pkg", samplePkgInfos(t), false)
	if err == nil {
		t.Fatal("writeFamilyPage: expected refusal for markerless existing page, got nil error")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Errorf("markerless page was modified despite refusal:\n%s", got)
	}
}

func TestWriteFamilyPage_ClaimAdoptsMarkerlessPage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pkg.mdx")
	original := "---\ntitle: hand-written\n---\n\nDo not touch me.\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeFamilyPage(path, "pkg", samplePkgInfos(t), true); err != nil {
		t.Fatalf("writeFamilyPage with claim=true: %v", err)
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

func TestWriteFamilyPage_RegeneratesAlreadyManagedPageWithoutClaim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pkg.mdx")
	mis := samplePkgInfos(t)

	// First write to a fresh file needs no claim.
	if err := writeFamilyPage(path, "pkg", mis, false); err != nil {
		t.Fatal(err)
	}
	firstGen, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// A second run with claim=false must succeed (marker already present) and
	// be byte-identical (deterministic).
	if err := writeFamilyPage(path, "pkg", mis, false); err != nil {
		t.Fatalf("writeFamilyPage on an already-managed page without claim: %v", err)
	}
	secondGen, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstGen) != string(secondGen) {
		t.Errorf("regeneration is not deterministic:\n--- first ---\n%s\n--- second ---\n%s", firstGen, secondGen)
	}
	for _, mi := range mis {
		if !strings.Contains(string(firstGen), managedMarker(mi.Module)) {
			t.Errorf("family page missing managed marker for %s", mi.Module)
		}
	}
}

// TestCleanupStalePages pins the stale-page sweep: a marker-carrying page whose
// slug is no longer produced is deleted; hand-written (markerless) pages and
// still-produced pages survive; non-.mdx files are ignored.
func TestCleanupStalePages(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("file.mdx", managedMarker("file.managed")+"\nproduced page\n")
	write("file-managed.mdx", managedMarker("file.managed")+"\nstale per-module page\n")
	write("query.mdx", "---\ntitle: hand-written\n---\n")
	write("meta.json", `{"title":"Modules"}`)

	removed, err := cleanupStalePages(dir, map[string]bool{"file": true})
	if err != nil {
		t.Fatalf("cleanupStalePages: %v", err)
	}
	if strings.Join(removed, ",") != "file-managed" {
		t.Errorf("removed = %v, want [file-managed]", removed)
	}
	for _, survivor := range []string{"file.mdx", "query.mdx", "meta.json"} {
		if _, err := os.Stat(filepath.Join(dir, survivor)); err != nil {
			t.Errorf("%s should have survived cleanup: %v", survivor, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "file-managed.mdx")); !os.IsNotExist(err) {
		t.Error("stale marker-carrying file-managed.mdx should have been deleted")
	}
}

// TestRenderFamilyPage_PkgAnatomySections pins the per-member anatomy on the
// live pkg family page, including that pkg.removed's Notes and Divergences —
// both present in its Doc — do NOT reach the rendered page.
func TestRenderFamilyPage_PkgAnatomySections(t *testing.T) {
	mis := samplePkgInfos(t)
	var removed *modschema.ModuleInfo
	for i := range mis {
		if mis[i].Module == "pkg.removed" {
			removed = &mis[i]
		}
	}
	if removed == nil {
		t.Fatal("pkg.removed not in the pkg family")
	}
	if len(removed.Doc.Notes) == 0 || len(removed.Doc.Divergences) == 0 {
		t.Fatal("fixture drift: pkg.removed's Doc no longer carries Notes/Divergences — pick another module for this pin")
	}

	got, err := renderFamilyPage("pkg", mis)
	if err != nil {
		t.Fatalf("renderFamilyPage: %v", err)
	}

	mustContain := []string{
		`title: "pkg"`,
		managedMarker("pkg.removed"),
		"## `pkg.removed` [#pkg-removed]",
		"**Source**: `pkg/state/modules/pkg/pkg_removed.go`",
		"### Parameters",
		"| `name` | `string` |",
		"### Effects",
		"#### Check",
		"#### Apply",
		"#### Revert",
		"### Examples",
		"#### Remove a package by name",
		"```yaml",
		"#### Remove a package ad hoc",
		"```bash",
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
	// Notes and Divergences are dropped from rendered pages.
	for _, banned := range []string{"# Notes", "# Divergences", removed.Doc.Notes[0].Title, "- " + removed.Doc.Divergences[0]} {
		if strings.Contains(got, banned) {
			t.Errorf("rendered page must not carry Notes/Divergences content (found %q)", banned)
		}
	}
}

// TestRenderFamilyPage_RejectsJSXInPoisonedDoc is the §4/§8 assertNoJSX guard:
// a Doc whose prose carries raw MDX/JSX (an `import` line or an unescaped
// `<Component>` open tag — the shape a hand-page migration could leave behind)
// must FAIL generation rather than emit a page that breaks the website build.
func TestRenderFamilyPage_RejectsJSXInPoisonedDoc(t *testing.T) {
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
			if _, err := renderFamilyPage("demo", []modschema.ModuleInfo{mi}); err == nil {
				t.Fatalf("renderFamilyPage: expected assertNoJSX to fail generation for %q", tc.description)
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

// TestDisplayParamType_FriendlyCompositeNames pins the friendly display names for
// the composite primitive passthroughs (map[string]any / []any) in the
// Parameters table, and that a semantic type's registered name and scalar
// primitives are unaffected.
func TestDisplayParamType_FriendlyCompositeNames(t *testing.T) {
	cases := []struct {
		name string
		f    modschema.Field
		want string
	}{
		{"map-passthrough", modschema.Field{GoType: "map[string]interface {}"}, "map"},
		{"list-passthrough", modschema.Field{GoType: "[]interface {}"}, "list"},
		{"scalar-string", modschema.Field{GoType: "string"}, "string"},
		{"scalar-bool", modschema.Field{GoType: "bool"}, "bool"},
		{"semantic-type-wins", modschema.Field{GoType: "paramtypes.FileMode", SemanticType: "FileMode"}, "FileMode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayParamType(tc.f); got != tc.want {
				t.Errorf("displayParamType(%+v) = %q, want %q", tc.f, got, tc.want)
			}
		})
	}
}

// TestRenderFamilyPage_FileManaged_CompositeTypesRenderFriendly ties the
// friendly display names to the real file family page: file.managed's
// context/defaults parameters must render as `map`, never leaking Go's
// `map[string]interface {}` spelling.
func TestRenderFamilyPage_FileManaged_CompositeTypesRenderFriendly(t *testing.T) {
	got, err := renderFamilyPage("file", familyInfos(t, "file"))
	if err != nil {
		t.Fatalf("renderFamilyPage: %v", err)
	}
	if strings.Contains(got, "map[string]interface {}") {
		t.Errorf("file family page still leaks the raw Go map type")
	}
	for _, want := range []string{"| `context` | `map` |", "| `defaults` | `map` |"} {
		if !strings.Contains(got, want) {
			t.Errorf("file.managed composite param not rendered with friendly `map` type: missing %q", want)
		}
	}
}

func TestRenderFamilyPage_UnresolvableSeeAlsoFailsGeneration(t *testing.T) {
	mi := modschema.ModuleInfo{
		Module: "demo.thing",
		Kind:   modschema.KindState,
		Doc: modschema.Doc{
			Summary: "demo",
			SeeAlso: []string{"nonexistent.module"},
		},
	}
	if _, err := renderFamilyPage("demo", []modschema.ModuleInfo{mi}); err == nil {
		t.Fatal("renderFamilyPage: expected error for an unresolvable See Also target")
	}
}

// TestRenderFamilyPage_SeeAlsoTargets pins the see-also link forms: a state
// target links its family page + member anchor; an execution-only target links
// the combined execution-modules page + function anchor.
func TestRenderFamilyPage_SeeAlsoTargets(t *testing.T) {
	mi := modschema.ModuleInfo{
		Module: "demo.thing",
		Kind:   modschema.KindState,
		Doc: modschema.Doc{
			Summary: "demo",
			SeeAlso: []string{"ssh_auth.present", "pkg.version"},
		},
	}
	got, err := renderFamilyPage("demo", []modschema.ModuleInfo{mi})
	if err != nil {
		t.Fatalf("renderFamilyPage: %v", err)
	}
	for _, want := range []string{
		"- [ssh_auth.present](/docs/guides/modules/ssh-auth#ssh-auth-present)",
		"- [pkg.version](/docs/guides/execution-modules#pkg-version)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("see-also link missing %q", want)
		}
	}
}
