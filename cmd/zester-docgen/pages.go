package main

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
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

	if mi.Doc.Description != "" {
		b.WriteString("\n---\n\n")
		b.WriteString(mi.Doc.Description)
		b.WriteString("\n")
	}

	if len(mi.Params) > 0 {
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
			fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s | %s |\n", f.Name, typ, required, def, f.Usage)
		}
		if mi.Kind == modschema.KindState {
			b.WriteString("\n")
			b.WriteString(requisitesBoilerplate)
			b.WriteString("\n")
		}
	}

	if len(mi.SemTypes) > 0 {
		b.WriteString("\n---\n\n## Parameter Types\n\n")
		for _, st := range mi.SemTypes {
			fmt.Fprintf(&b, "### %s\n\n%s\n\n", st.Name, st.Doc)
		}
	}

	renderPageEffects(&b, mi.Doc.Effects)

	if len(mi.Doc.Examples) > 0 {
		b.WriteString("\n---\n\n## Examples\n\n")
		for _, ex := range mi.Doc.Examples {
			fmt.Fprintf(&b, "### %s\n\n", ex.Title)
			if ex.Explanation != "" {
				fmt.Fprintf(&b, "%s\n\n", ex.Explanation)
			}
			fence := "yaml"
			if ex.Kind == "cli" {
				fence = "bash"
			}
			fmt.Fprintf(&b, "```%s\n%s\n```\n\n", fence, strings.TrimRight(ex.Code, "\n"))
		}
	}

	if len(mi.Doc.Notes) > 0 {
		b.WriteString("\n---\n\n## Notes\n\n")
		for _, n := range mi.Doc.Notes {
			fmt.Fprintf(&b, "> **%s**\n>\n%s\n\n", n.Title, blockquoteBody(n.Body))
		}
	}

	if len(mi.Doc.Divergences) > 0 {
		b.WriteString("\n---\n\n## Divergences\n\n")
		for _, d := range mi.Doc.Divergences {
			fmt.Fprintf(&b, "- %s\n", d)
		}
	}

	if len(mi.Doc.SeeAlso) > 0 {
		b.WriteString("\n---\n\n## See Also\n\n")
		for _, s := range mi.Doc.SeeAlso {
			slug, ok := moduleToSlug[s]
			if !ok {
				return "", fmt.Errorf("docgen: module %s: see-also target %q does not resolve to any page", mi.Module, s)
			}
			fmt.Fprintf(&b, "- [%s](/docs/guides/modules/%s)\n", s, slug)
		}
	}

	rendered := b.String()
	if err := assertNoJSX(mi.Module, rendered); err != nil {
		return "", err
	}
	return rendered, nil
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

// writeModulePage writes the rendered page for mi to path. If path already
// exists without the managed marker, it refuses to overwrite it UNLESS claim
// is true (the one-time adoption a module's migration PR performs
// explicitly) — the markerless-overwrite guard (§8) that protects
// hand-written pages for modules that have not been migrated yet.
func writeModulePage(path string, mi modschema.ModuleInfo, claim bool) error {
	rendered, err := renderModulePage(mi)
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil {
		if !hasManagedMarker(existing) && !claim {
			return fmt.Errorf("docgen: refusing to overwrite markerless page %s for module %s "+
				"(pass --claim=%s to adopt it)", path, mi.Module, mi.Module)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("docgen: read %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(rendered), 0644); err != nil {
		return fmt.Errorf("docgen: write %s: %w", path, err)
	}
	return nil
}
