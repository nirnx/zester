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

// Heading-level constants for the shared section renderers (M5: function
// banners H2, their sections H3 — no colliding H2s). A standalone single-module
// page (renderModulePage) and a page group's ONE shared Parameters/Parameter
// Types section (renderSharedParamPageGroup — not nested under any module
// banner) render their sections at pageSectionLevel ("## Parameters"). Anything
// rendered UNDER a "## `module`" banner — a page group's per-module behavior
// sections, and the combined execution-modules page's per-function body — must
// nest one level deeper (nestedSectionLevel, "### Parameters") so the section
// heading never collides with the module banner's own H2.
const (
	moduleBannerLevel  = 2
	pageSectionLevel   = 2
	nestedSectionLevel = moduleBannerLevel + 1
)

// heading returns a Markdown ATX heading prefix of level hashes: heading(2) is
// "##", heading(3) is "###". Every shared section renderer takes its own
// heading level as a parameter and renders its subsections one level deeper, so
// the same renderer produces a correctly nested anatomy whether it is called at
// the top of a standalone page or underneath a "## `module`" banner.
func heading(level int) string {
	return strings.Repeat("#", level)
}

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
	renderParamsSection(&b, mi, pageSectionLevel)
	renderParamTypesSection(&b, mi, pageSectionLevel)
	renderPageEffects(&b, mi.Doc.Effects, pageSectionLevel)
	renderExamplesSection(&b, mi.Doc.Examples, pageSectionLevel)
	renderNotesSection(&b, mi.Doc.Notes, pageSectionLevel)
	renderDivergencesSection(&b, mi.Doc.Divergences, pageSectionLevel)
	if err := renderSeeAlsoSection(&b, mi.Module, mi.Doc.SeeAlso, pageSectionLevel); err != nil {
		return "", err
	}

	rendered := b.String()
	if err := assertNoJSX(mi.Module, rendered); err != nil {
		return "", err
	}
	return rendered, nil
}

// renderModulePageGroup renders ONE MDX page shared by the N modules that map to
// a single page slug (the §8 N:1 PageGroups). For a single-member group it is
// byte-identical to renderModulePage. For a multi-member group the caller states,
// via distinctParams, whether the members share a parameter surface:
//
//   - distinctParams=false — SHARED params (file.comment/file.uncomment, one
//     FileComment proto): the page renders one Parameters/Parameter Types section
//     for all members, then a per-module behavior section (Description → Effects →
//     … → See Also). assertSharedParams verifies the surface genuinely matches;
//     a mismatch is a LOUD generation error, never a silent fallback — a
//     shared-proto group that diverges is a real bug the flag must not mask.
//   - distinctParams=true — DISTINCT params (host.present/host.absent — present
//     has `ip`; ssh_auth.present/ssh_auth.absent — present has `enc`/`comment`;
//     the test-helpers group — 0..3 params): the page renders each member's FULL
//     body (Source → Description → Parameters → Parameter Types → Effects → … →
//     See Also) under its own banner, because a single shared Parameters table
//     would be wrong for at least one member. This is an EXPLICIT opt-in
//     (isDistinctParamSlug), not an error-triggered fallback.
func renderModulePageGroup(mis []modschema.ModuleInfo, distinctParams bool) (string, error) {
	if len(mis) == 1 {
		return renderModulePage(mis[0])
	}
	if distinctParams {
		return renderDistinctParamPageGroup(mis)
	}
	if err := assertSharedParams(mis); err != nil {
		return "", err
	}
	return renderSharedParamPageGroup(mis)
}

// renderSharedParamPageGroup renders a multi-member page whose members share one
// parameter surface (they share a Go proto): a joined title, one managed marker
// per module, the shared Source line, ONE Parameters and Parameter Types section,
// then a per-module behavior section (Description → Effects → Examples → Notes →
// Divergences → See Also) under a "## `<module>`" banner.
func renderSharedParamPageGroup(mis []modschema.ModuleInfo) (string, error) {
	var b strings.Builder
	head := mis[0]
	names := make([]string, len(mis))
	quoted := make([]string, len(mis))
	for i, mi := range mis {
		names[i] = mi.Module
		quoted[i] = "`" + mi.Module + "`"
	}

	renderGroupFrontmatter(&b, mis)
	fmt.Fprintf(&b, "**Source**: `%s`\n", sourcePath(head.Kind, head.Module))

	// Shared parameters note + one Parameters/Parameter Types section (the
	// members share one proto, asserted by the caller).
	b.WriteString("\n---\n\n")
	fmt.Fprintf(&b, "%s share the same parameters and implementation.\n", joinWithAnd(quoted))
	renderParamsSection(&b, head, pageSectionLevel)
	renderParamTypesSection(&b, head, pageSectionLevel)

	// Per-module behavior sections under a module banner: nested one level
	// deeper than the shared Parameters section above, so "### Effects" never
	// collides with the "## `module`" banner it sits under (M5).
	for _, mi := range mis {
		fmt.Fprintf(&b, "\n---\n\n## `%s`\n\n", mi.Module)
		if mi.Doc.Description != "" {
			b.WriteString(mi.Doc.Description)
			b.WriteString("\n")
		}
		renderPageEffects(&b, mi.Doc.Effects, nestedSectionLevel)
		renderExamplesSection(&b, mi.Doc.Examples, nestedSectionLevel)
		renderNotesSection(&b, mi.Doc.Notes, nestedSectionLevel)
		renderDivergencesSection(&b, mi.Doc.Divergences, nestedSectionLevel)
		if err := renderSeeAlsoSection(&b, mi.Module, mi.Doc.SeeAlso, nestedSectionLevel); err != nil {
			return "", err
		}
	}

	rendered := b.String()
	if err := assertNoJSX(strings.Join(names, "/"), rendered); err != nil {
		return "", err
	}
	return rendered, nil
}

// renderDistinctParamPageGroup renders a multi-member page whose members do NOT
// share a parameter surface (distinct Go protos — host.present/host.absent,
// ssh_auth.present/ssh_auth.absent). The shared header carries the joined title
// and one managed marker per module; each member then contributes its FULL body
// (Source → Description → Parameters → Parameter Types → Effects → Examples →
// Notes → Divergences → See Also, identical to a standalone page's body) under a
// "## `<module>`" banner, so each member's own parameter table is documented.
func renderDistinctParamPageGroup(mis []modschema.ModuleInfo) (string, error) {
	var b strings.Builder
	names := make([]string, len(mis))
	quoted := make([]string, len(mis))
	for i, mi := range mis {
		names[i] = mi.Module
		quoted[i] = "`" + mi.Module + "`"
	}

	renderGroupFrontmatter(&b, mis)
	fmt.Fprintf(&b, "%s are documented together on this page; each has its own parameters.\n", joinWithAnd(quoted))

	for _, mi := range mis {
		fmt.Fprintf(&b, "\n---\n\n## `%s`\n\n", mi.Module)
		fmt.Fprintf(&b, "**Source**: `%s`\n", sourcePath(mi.Kind, mi.Module))
		renderDescriptionSection(&b, mi.Doc.Description)
		renderParamsSection(&b, mi, nestedSectionLevel)
		renderParamTypesSection(&b, mi, nestedSectionLevel)
		renderPageEffects(&b, mi.Doc.Effects, nestedSectionLevel)
		renderExamplesSection(&b, mi.Doc.Examples, nestedSectionLevel)
		renderNotesSection(&b, mi.Doc.Notes, nestedSectionLevel)
		renderDivergencesSection(&b, mi.Doc.Divergences, nestedSectionLevel)
		if err := renderSeeAlsoSection(&b, mi.Module, mi.Doc.SeeAlso, nestedSectionLevel); err != nil {
			return "", err
		}
	}

	rendered := b.String()
	if err := assertNoJSX(strings.Join(names, "/"), rendered); err != nil {
		return "", err
	}
	return rendered, nil
}

// renderGroupFrontmatter writes the shared frontmatter (a "a / b" title, the
// space-joined member summaries as the description) and one managed marker per
// member, followed by a blank line — the common header of both multi-member page
// shapes.
func renderGroupFrontmatter(b *strings.Builder, mis []modschema.ModuleInfo) {
	names := make([]string, len(mis))
	summaries := make([]string, len(mis))
	for i, mi := range mis {
		names[i] = mi.Module
		summaries[i] = mi.Doc.Summary
	}
	fmt.Fprintf(b, "---\ntitle: %q\ndescription: %q\n---\n\n",
		strings.Join(names, " / "), strings.Join(summaries, " "))
	for _, mi := range mis {
		b.WriteString(managedMarker(mi.Module))
		b.WriteString("\n")
	}
	b.WriteString("\n")
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

func renderParamsSection(b *strings.Builder, mi modschema.ModuleInfo, level int) {
	h := heading(level)
	if len(mi.Params) == 0 {
		// A parameterless STATE module (test.ping, test.nop, module.run) still
		// documents that it accepts the requisite / Salt-parity attribute set —
		// every state does — so the page must not silently drop the requisites
		// boilerplate. A non-state surface (dispatch/exec) has no requisites, so
		// it renders nothing here.
		if mi.Kind == modschema.KindState {
			fmt.Fprintf(b, "\n---\n\n%s Parameters\n\n", h)
			b.WriteString("This module takes no parameters of its own.\n")
			b.WriteString("\n")
			b.WriteString(requisitesBoilerplate)
			b.WriteString("\n")
		}
		return
	}
	fmt.Fprintf(b, "\n---\n\n%s Parameters\n\n", h)
	b.WriteString("| Parameter | Type | Required | Default | Description |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, f := range mi.Params {
		typ := displayParamType(f)
		required := "No"
		if f.Required {
			required = "Yes"
		}
		def := paramDefaultCellKind(f, mi.Kind)
		fmt.Fprintf(b, "| `%s` | `%s` | %s | %s | %s |\n", f.Name, typ, required, def, escapeUsageCell(f.Usage))
	}
	if mi.Kind == modschema.KindState {
		b.WriteString("\n")
		b.WriteString(requisitesBoilerplate)
		b.WriteString("\n")
	}
}

func renderParamTypesSection(b *strings.Builder, mi modschema.ModuleInfo, level int) {
	if len(mi.SemTypes) == 0 {
		return
	}
	h, sub := heading(level), heading(level+1)
	fmt.Fprintf(b, "\n---\n\n%s Parameter Types\n\n", h)
	for _, st := range mi.SemTypes {
		fmt.Fprintf(b, "%s %s\n\n%s\n\n", sub, st.Name, st.Doc)
	}
}

func renderExamplesSection(b *strings.Builder, examples []modschema.Example, level int) {
	if len(examples) == 0 {
		return
	}
	h, sub := heading(level), heading(level+1)
	fmt.Fprintf(b, "\n---\n\n%s Examples\n\n", h)
	for _, ex := range examples {
		fmt.Fprintf(b, "%s %s\n\n", sub, ex.Title)
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

func renderNotesSection(b *strings.Builder, notes []modschema.Note, level int) {
	if len(notes) == 0 {
		return
	}
	fmt.Fprintf(b, "\n---\n\n%s Notes\n\n", heading(level))
	for _, n := range notes {
		fmt.Fprintf(b, "> **%s**\n>\n%s\n\n", n.Title, blockquoteBody(n.Body))
	}
}

func renderDivergencesSection(b *strings.Builder, divergences []string, level int) {
	if len(divergences) == 0 {
		return
	}
	fmt.Fprintf(b, "\n---\n\n%s Divergences\n\n", heading(level))
	for _, d := range divergences {
		fmt.Fprintf(b, "- %s\n", d)
	}
}

func renderSeeAlsoSection(b *strings.Builder, module string, seeAlso []string, level int) error {
	if len(seeAlso) == 0 {
		return nil
	}
	fmt.Fprintf(b, "\n---\n\n%s See Also\n\n", heading(level))
	for _, s := range seeAlso {
		// A state module resolves to its own page; an execution-only module
		// resolves to the shared execution-modules page. cmd.run is in
		// moduleToSlug (the state page is its canonical surface), so it resolves
		// as a state target even when named from an execution spec.
		if slug, ok := moduleToSlug[s]; ok {
			fmt.Fprintf(b, "- [%s](/docs/guides/modules/%s)\n", s, slug)
			continue
		}
		if execModuleSet[s] {
			fmt.Fprintf(b, "- [%s](%s)\n", s, execPageURL)
			continue
		}
		return fmt.Errorf("docgen: module %s: see-also target %q does not resolve to any page", module, s)
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
// usageCellEscaper escapes a Field.Usage string for embedding, verbatim, into
// a generated Markdown table cell. Usage is plain help prose from a module's
// `usage:"..."` tag, not authored CommonMark (unlike Description/Effects/
// Notes, whose authors deliberately backtick-wrap any placeholder like
// `<hex>` or `<branch>`) — a usage string is free to contain a BARE
// "<placeholder>" token (see archive.extracted's "sha256=<hex>", cmd.run's
// "name: <command>", git.latest's "origin/<branch>") or a bare "${name}"-style
// backreference token (see file.replace's "$1/${name} backreferences"). MDX
// (unlike plain Markdown, see the managedMarkerPrefix comment above for the
// same class of gotcha) parses a bare "<word>" ANYWHERE in the document as an
// unclosed JSX tag, and a bare "{word}" as a JavaScript expression container
// (evaluated as a reference to an undefined identifier at prerender time) —
// either fails the site build outright rather than just rendering oddly, so
// every angle bracket and curly brace in this untrusted-w.r.t.-markup text
// must be escaped. Curly braces use numeric character references (no named
// HTML entity exists for them). "&" is escaped too (so an already-escaped
// sequence is never double-unescaped) and "|" is escaped (a literal pipe
// would otherwise terminate the table cell early). Entities decode back to
// the literal characters in the rendered page, so this is visually a no-op
// for any usage string that happens not to need it.
var usageCellEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	"{", "&#123;",
	"}", "&#125;",
	"|", "\\|",
)

// escapeUsageCell applies usageCellEscaper to a Field.Usage value before it is
// embedded in a generated Markdown table cell.
func escapeUsageCell(usage string) string {
	return usageCellEscaper.Replace(usage)
}

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

// paramDefaultCellKind is the Kind-aware variant: an execution function's
// primary falls back to the request ID / bare positional, not a state ID.
func paramDefaultCellKind(f modschema.Field, kind modschema.Kind) string {
	if f.Primary && !f.Sensitive && (!f.HasDefault || f.Default == "") && kind == modschema.KindExec {
		return "Request ID (bare positional)"
	}
	return paramDefaultCell(f)
}

func renderPageEffects(b *strings.Builder, e modschema.Effects, level int) {
	if e.Check == "" && e.Apply == "" && e.Revert == "" && e.Execution == "" {
		return
	}
	h, sub := heading(level), heading(level+1)
	fmt.Fprintf(b, "\n---\n\n%s Effects\n\n", h)
	if e.Check != "" {
		fmt.Fprintf(b, "%s Check\n\n%s\n\n", sub, e.Check)
	}
	if e.Apply != "" {
		fmt.Fprintf(b, "%s Apply\n\n%s\n\n", sub, e.Apply)
	}
	if e.Revert != "" {
		fmt.Fprintf(b, "%s Revert\n\n%s\n\n", sub, e.Revert)
	}
	if e.Execution != "" {
		fmt.Fprintf(b, "%s Execution\n\n%s\n\n", sub, e.Execution)
	}
}

// writeModulePage writes the rendered page for a single mi to path. It is the
// single-module convenience wrapper over writeModulePageGroup (a single-member
// group never renders per-member, so distinctParams is irrelevant here).
func writeModulePage(path string, mi modschema.ModuleInfo, claim bool) error {
	return writeModulePageGroup(path, []modschema.ModuleInfo{mi}, claim, false)
}

// writeModulePageGroup writes the rendered page for the N modules sharing a page
// slug (see renderModulePageGroup). distinctParams is the caller's explicit
// declaration (isDistinctParamSlug) that the members do NOT share a parameter
// surface. If path already exists without the managed marker, it refuses to
// overwrite it UNLESS claim is true (the one-time adoption a module's migration
// PR performs explicitly) — the markerless-overwrite guard (§8) that protects
// hand-written pages for modules that have not been migrated yet.
func writeModulePageGroup(path string, mis []modschema.ModuleInfo, claim, distinctParams bool) error {
	rendered, err := renderModulePageGroup(mis, distinctParams)
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
