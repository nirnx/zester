package main

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/nirnx/zester/pkg/modschema"
)

// managedMarkerPrefix identifies a docgen-owned MDX page. Any file lacking it
// is presumed hand-written and is never blindly overwritten (§8 marker guard).
//
// MDX (unlike plain Markdown) parses `<...>` as JSX, so a raw HTML comment
// (`<!-- ... -->`) is a hard build error ("Unexpected character `!`") — the
// site failed to build on the very first generated page until this used
// MDX's own comment syntax, `{/* ... */}`, instead.
const managedMarkerPrefix = "{/* zester-docgen:managed"

// managedMarker renders the exact marker comment stamped into a generated
// page for module.
func managedMarker(module string) string {
	return fmt.Sprintf("{/* zester-docgen:managed module=%q */}", module)
}

// hasManagedMarker reports whether content already carries the docgen marker.
func hasManagedMarker(content []byte) bool {
	return bytes.Contains(content, []byte(managedMarkerPrefix))
}

// requisitesBoilerplate is the auto-inserted requisites paragraph (§8 page
// anatomy: "auto requisites boilerplate"), verbatim from the existing
// hand-written pages so the wording stays familiar across generated and
// not-yet-migrated pages. State modules only — exec/dispatch surfaces have no
// requisites.
const requisitesBoilerplate = "All states also accept the full set of requisite parameters and " +
	"Salt-parity state attributes — see [Dependencies & Requisites](/docs/guides/states/dependencies)."

// renderModulePage renders the full MDX page for mi per the §8 page anatomy:
// frontmatter → marker → **Source**: → Description → Parameters table →
// auto requisites boilerplate → Parameter Types → Effects (by kind) →
// Examples → Notes → Divergences → See Also. Empty sections are omitted.
func renderModulePage(mi modschema.ModuleInfo) (string, error) {
	var b strings.Builder

	fmt.Fprintf(&b, "---\ntitle: %q\ndescription: %q\n---\n\n", mi.Module, mi.Doc.Summary)
	b.WriteString(managedMarker(mi.Module))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "**Source**: `%s`\n", sourcePath(mi.Kind, mi.Module))

	renderDescriptionSection(&b, mi.Doc.Description)
	renderParamsSection(&b, mi)
	renderParamTypesSection(&b, mi)
	renderPageEffects(&b, mi.Doc.Effects)
	renderExamplesSection(&b, mi.Doc.Examples)
	renderNotesSection(&b, mi.Doc.Notes)
	renderDivergencesSection(&b, mi.Doc.Divergences)
	if err := renderSeeAlsoSection(&b, mi.Module, mi.Doc.SeeAlso); err != nil {
		return "", err
	}

	rendered := b.String()
	if err := assertNoJSX(mi.Module, rendered); err != nil {
		return "", err
	}
	return rendered, nil
}

// renderModulePageGroup renders ONE MDX page shared by the N modules that map to
// a single page slug (the §8 N:1 PageGroups — file.comment/file.uncomment is the
// first with every member migrated). For a single-member group it is
// byte-identical to renderModulePage. For a multi-member group it renders the
// SHARED header (a joined title, one managed marker per module, the shared Source
// line, and — because the members share one proto — a single Parameters and
// Parameter Types section) followed by a per-module section (Description →
// Effects → Examples → Notes → Divergences → See Also) under a "## `<module>`"
// banner. The members are expected to share an identical parameter surface (they
// share the proto); that is asserted before rendering.
func renderModulePageGroup(mis []modschema.ModuleInfo) (string, error) {
	if len(mis) == 1 {
		return renderModulePage(mis[0])
	}
	if err := assertSharedParams(mis); err != nil {
		return "", err
	}

	var b strings.Builder
	head := mis[0]
	names := make([]string, len(mis))
	summaries := make([]string, len(mis))
	quoted := make([]string, len(mis))
	for i, mi := range mis {
		names[i] = mi.Module
		summaries[i] = mi.Doc.Summary
		quoted[i] = "`" + mi.Module + "`"
	}

	fmt.Fprintf(&b, "---\ntitle: %q\ndescription: %q\n---\n\n",
		strings.Join(names, " / "), strings.Join(summaries, " "))
	for _, mi := range mis {
		b.WriteString(managedMarker(mi.Module))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "**Source**: `%s`\n", sourcePath(head.Kind, head.Module))

	// Shared parameters note + one Parameters/Parameter Types section (the
	// members share one proto, asserted above).
	b.WriteString("\n---\n\n")
	fmt.Fprintf(&b, "%s share the same parameters and implementation.\n", joinWithAnd(quoted))
	renderParamsSection(&b, head)
	renderParamTypesSection(&b, head)

	// Per-module behavior sections under a module banner.
	for _, mi := range mis {
		fmt.Fprintf(&b, "\n---\n\n## `%s`\n\n", mi.Module)
		if mi.Doc.Description != "" {
			b.WriteString(mi.Doc.Description)
			b.WriteString("\n")
		}
		renderPageEffects(&b, mi.Doc.Effects)
		renderExamplesSection(&b, mi.Doc.Examples)
		renderNotesSection(&b, mi.Doc.Notes)
		renderDivergencesSection(&b, mi.Doc.Divergences)
		if err := renderSeeAlsoSection(&b, mi.Module, mi.Doc.SeeAlso); err != nil {
			return "", err
		}
	}

	rendered := b.String()
	if err := assertNoJSX(strings.Join(names, "/"), rendered); err != nil {
		return "", err
	}
	return rendered, nil
}

// assertSharedParams verifies every module in an N:1 page group exposes the same
// parameter surface (name/type/required/default/aliases) — the invariant that
// lets the combined page render a SINGLE shared Parameters table. Members of a
// PageGroup share one Go proto, so this holds by construction; the check turns a
// future divergence into a loud generation failure rather than a silently wrong
// page.
func assertSharedParams(mis []modschema.ModuleInfo) error {
	head := mis[0]
	for _, mi := range mis[1:] {
		if len(mi.Params) != len(head.Params) {
			return fmt.Errorf("docgen: page group %v: members expose different parameter counts (%d vs %d)",
				groupNames(mis), len(mi.Params), len(head.Params))
		}
		for i := range mi.Params {
			a, c := head.Params[i], mi.Params[i]
			if a.Name != c.Name || a.GoType != c.GoType || a.Required != c.Required ||
				a.Primary != c.Primary || a.Default != c.Default || a.SemanticType != c.SemanticType ||
				!slices.Equal(a.Aliases, c.Aliases) {
				return fmt.Errorf("docgen: page group %v: parameter %q differs across members — a shared page needs one parameter surface",
					groupNames(mis), a.Name)
			}
		}
	}
	return nil
}

func groupNames(mis []modschema.ModuleInfo) []string {
	out := make([]string, len(mis))
	for i, mi := range mis {
		out[i] = mi.Module
	}
	return out
}

// joinWithAnd renders items as "a and b", "a, b, and c", or a single item.
func joinWithAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
	}
}

func renderDescriptionSection(b *strings.Builder, description string) {
	if description == "" {
		return
	}
	b.WriteString("\n---\n\n")
	b.WriteString(description)
	b.WriteString("\n")
}

func renderParamsSection(b *strings.Builder, mi modschema.ModuleInfo) {
	if len(mi.Params) == 0 {
		return
	}
	b.WriteString("\n---\n\n## Parameters\n\n")
	b.WriteString("| Parameter | Type | Required | Default | Description |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, f := range mi.Params {
		typ := displayParamType(f)
		required := "No"
		if f.Required {
			required = "Yes"
		}
		def := paramDefaultCell(f)
		fmt.Fprintf(b, "| `%s` | `%s` | %s | %s | %s |\n", f.Name, typ, required, def, f.Usage)
	}
	if mi.Kind == modschema.KindState {
		b.WriteString("\n")
		b.WriteString(requisitesBoilerplate)
		b.WriteString("\n")
	}
}

func renderParamTypesSection(b *strings.Builder, mi modschema.ModuleInfo) {
	if len(mi.SemTypes) == 0 {
		return
	}
	b.WriteString("\n---\n\n## Parameter Types\n\n")
	for _, st := range mi.SemTypes {
		fmt.Fprintf(b, "### %s\n\n%s\n\n", st.Name, st.Doc)
	}
}

func renderExamplesSection(b *strings.Builder, examples []modschema.Example) {
	if len(examples) == 0 {
		return
	}
	b.WriteString("\n---\n\n## Examples\n\n")
	for _, ex := range examples {
		fmt.Fprintf(b, "### %s\n\n", ex.Title)
		if ex.Explanation != "" {
			fmt.Fprintf(b, "%s\n\n", ex.Explanation)
		}
		fence := "yaml"
		if ex.Kind == "cli" {
			fence = "bash"
		}
		fmt.Fprintf(b, "```%s\n%s\n```\n\n", fence, strings.TrimRight(ex.Code, "\n"))
	}
}

func renderNotesSection(b *strings.Builder, notes []modschema.Note) {
	if len(notes) == 0 {
		return
	}
	b.WriteString("\n---\n\n## Notes\n\n")
	for _, n := range notes {
		fmt.Fprintf(b, "> **%s**\n>\n%s\n\n", n.Title, blockquoteBody(n.Body))
	}
}

func renderDivergencesSection(b *strings.Builder, divergences []string) {
	if len(divergences) == 0 {
		return
	}
	b.WriteString("\n---\n\n## Divergences\n\n")
	for _, d := range divergences {
		fmt.Fprintf(b, "- %s\n", d)
	}
}

func renderSeeAlsoSection(b *strings.Builder, module string, seeAlso []string) error {
	if len(seeAlso) == 0 {
		return nil
	}
	b.WriteString("\n---\n\n## See Also\n\n")
	for _, s := range seeAlso {
		slug, ok := moduleToSlug[s]
		if !ok {
			return fmt.Errorf("docgen: module %s: see-also target %q does not resolve to any page", module, s)
		}
		fmt.Fprintf(b, "- [%s](/docs/guides/modules/%s)\n", s, slug)
	}
	return nil
}

// importLineRE matches a raw ES-module / MDX `import` statement at the start of
// a line (leading indentation tolerated) — the shape a hand-page's MDX header
// would leave in a Doc field.
var importLineRE = regexp.MustCompile(`^\s*import\b`)

// assertNoJSX enforces the CommonMark-only prose contract (§4/§8): a docgen
// page must never carry raw MDX/JSX. Doc prose is rendered as plain CommonMark
// — `{/* */}` comments, Note blockquotes, and fenced code — so an `import`
// line or an unescaped `<Component` open tag can only have leaked in from an
// un-migrated hand page (§4: `<Tabs>` → sequential fenced blocks, `<Callout>` →
// Note blockquotes). Left in place it would silently break the website's MDX
// build; here it fails generation loudly instead. Fenced code blocks and inline
// `code` spans are literal in MDX, so their contents are exempt.
func assertNoJSX(module, rendered string) error {
	inFence := false
	for i, line := range strings.Split(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		// A Note body renders inside a blockquote, so its fenced-code delimiters
		// arrive prefixed with "> "; strip an optional leading blockquote marker
		// before the fence check so code inside a Note toggles the fence and is
		// still skipped.
		fenceProbe := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
		if strings.HasPrefix(fenceProbe, "```") || strings.HasPrefix(fenceProbe, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if importLineRE.MatchString(line) {
			return fmt.Errorf("docgen: module %s: generated line %d is a raw MDX/ESM import (%q) — "+
				"Doc prose must be CommonMark, never MDX/JSX", module, i+1, trimmed)
		}
		if col, ok := unescapedJSXComponent(line); ok {
			return fmt.Errorf("docgen: module %s: generated line %d has an unescaped JSX element at column %d (%q) — "+
				"migrate <Component> prose to CommonMark (fenced blocks / Note blockquotes)", module, i+1, col+1, trimmed)
		}
	}
	return nil
}

// unescapedJSXComponent reports the byte offset of a `<` immediately followed by
// an uppercase ASCII letter (a JSX component open tag) that is neither
// backslash-escaped nor inside an inline `code` span. A lowercase `<tag` (plain
// HTML, valid in MDX prose) is deliberately allowed.
func unescapedJSXComponent(line string) (int, bool) {
	inCode := false
	for i := range len(line) {
		switch line[i] {
		case '`':
			inCode = !inCode
		case '<':
			if inCode {
				continue
			}
			if i > 0 && line[i-1] == '\\' {
				continue // escaped
			}
			if i+1 < len(line) && line[i+1] >= 'A' && line[i+1] <= 'Z' {
				return i, true
			}
		}
	}
	return -1, false
}

// displayParamType renders the Parameters-table Type cell for a field. A
// semantic-typed field shows its registered semantic-type name; a primitive
// field shows its Go type, except that the composite primitive passthroughs get
// a friendly display name — a `map[string]interface {}` (file.managed's
// context/defaults) renders as `map` and a `[]interface {}` as `list`, instead
// of leaking Go's reflect spelling into the docs. Scalar primitives (string,
// bool, int, …) pass through unchanged.
func displayParamType(f modschema.Field) string {
	if f.SemanticType != "" {
		return f.SemanticType
	}
	switch f.GoType {
	case "map[string]interface {}":
		return "map"
	case "[]interface {}":
		return "list"
	default:
		return f.GoType
	}
}

// blockquoteBody prefixes EVERY line of a Note body with a blockquote marker so
// a multi-paragraph or fenced-code body stays inside ONE `>` callout. In
// CommonMark a bare blank line (no `>`) terminates a blockquote, so a body with
// a fenced code block or a trailing paragraph would otherwise escape the callout
// after its first line (the file.managed "Worked source-template render" bug):
// blank lines render as a lone `>` and every other line — fenced-code delimiters
// and their contents included — as `> <line>`.
func blockquoteBody(body string) string {
	lines := strings.Split(body, "\n")
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		if line == "" {
			b.WriteByte('>')
			continue
		}
		b.WriteString("> ")
		b.WriteString(line)
	}
	return b.String()
}

// paramDefaultCell renders the Default column. A sensitive parameter's
// Default is already redacted to empty by the schema layer (§2.5); this never
// prints a value for one regardless.
func paramDefaultCell(f modschema.Field) string {
	if f.Sensitive {
		return "*(sensitive — not shown)*"
	}
	if f.HasDefault && f.Default != "" {
		if f.Lazy {
			return fmt.Sprintf("`%s` (lazy)", f.Default)
		}
		return fmt.Sprintf("`%s`", f.Default)
	}
	if f.Primary {
		return "State ID"
	}
	return "*(none)*"
}

func renderPageEffects(b *strings.Builder, e modschema.Effects) {
	if e.Check == "" && e.Apply == "" && e.Revert == "" && e.Execution == "" {
		return
	}
	b.WriteString("\n---\n\n## Effects\n\n")
	if e.Check != "" {
		fmt.Fprintf(b, "### Check\n\n%s\n\n", e.Check)
	}
	if e.Apply != "" {
		fmt.Fprintf(b, "### Apply\n\n%s\n\n", e.Apply)
	}
	if e.Revert != "" {
		fmt.Fprintf(b, "### Revert\n\n%s\n\n", e.Revert)
	}
	if e.Execution != "" {
		fmt.Fprintf(b, "### Execution\n\n%s\n\n", e.Execution)
	}
}

// writeModulePage writes the rendered page for a single mi to path. It is the
// single-module convenience wrapper over writeModulePageGroup.
func writeModulePage(path string, mi modschema.ModuleInfo, claim bool) error {
	return writeModulePageGroup(path, []modschema.ModuleInfo{mi}, claim)
}

// writeModulePageGroup writes the rendered page for the N modules sharing a page
// slug (see renderModulePageGroup). If path already exists without the managed
// marker, it refuses to overwrite it UNLESS claim is true (the one-time adoption
// a module's migration PR performs explicitly) — the markerless-overwrite guard
// (§8) that protects hand-written pages for modules that have not been migrated
// yet.
func writeModulePageGroup(path string, mis []modschema.ModuleInfo, claim bool) error {
	rendered, err := renderModulePageGroup(mis)
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil {
		if !hasManagedMarker(existing) && !claim {
			return fmt.Errorf("docgen: refusing to overwrite markerless page %s for %v "+
				"(pass --claim to adopt it)", path, groupNames(mis))
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("docgen: read %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(rendered), 0644); err != nil {
		return fmt.Errorf("docgen: write %s: %w", path, err)
	}
	return nil
}
