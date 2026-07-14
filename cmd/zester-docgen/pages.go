package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/nirnx/zester/pkg/modschema"
)

// managedMarkerPrefix identifies a docgen-owned MDX page. Any file lacking it
// is presumed hand-written and is never blindly overwritten (§8 marker guard);
// the stale-page cleanup (cleanupStalePages) likewise deletes ONLY
// marker-carrying files.
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
// anatomy: "auto requisites boilerplate"), verbatim from the pre-family-tree
// pages so the wording stays familiar. Every family-page member is a state
// module, so it renders ONCE per family page (in the lede, after the function
// index) rather than once per member.
const requisitesBoilerplate = "All states also accept the full set of requisite parameters and " +
	"Salt-parity state attributes — see [Dependencies & Requisites](/docs/guides/states/dependencies)."

// Heading-level constants for the shared section renderers: member banners H2,
// their sections H3 — no colliding H2s. A family page's shared sections
// (Family Parameters, Parameter Types) render at pageSectionLevel
// ("## Family Parameters"); anything rendered UNDER a "## `module`" banner —
// a member's body on a family page, and the combined execution-modules page's
// per-function body — nests one level deeper (nestedSectionLevel,
// "### Parameters") so a section heading never collides with the banner's own
// H2.
const (
	moduleBannerLevel  = 2
	pageSectionLevel   = 2
	nestedSectionLevel = moduleBannerLevel + 1
)

// heading returns a Markdown ATX heading prefix of level hashes: heading(2) is
// "##", heading(3) is "###". Every shared section renderer takes its own
// heading level as a parameter and renders its subsections one level deeper.
func heading(level int) string {
	return strings.Repeat("#", level)
}

// memberLink renders an intra-page link to a member's banner anchor.
func memberLink(module string) string {
	return fmt.Sprintf("[`%s`](#%s)", module, memberAnchor(module))
}

// familyDescription is the family page's frontmatter description — short and
// deterministic.
func familyDescription(family string) string {
	return fmt.Sprintf("The %s.* family of state modules.", family)
}

// renderFamilyPage renders ONE MDX page for a whole module family (Salt-style
// family tree): frontmatter (title = family name) → one managed marker per
// member → function index table (linking each member's stable anchor) → the
// requisites boilerplate (once — every member is a state) → Family Parameters
// (the component-declared params, rendered once) → Parameter Types (the
// members' semantic types, deduplicated) → per-member sections under
// "## `module.function` [#module-function]" banners (Fumadocs custom heading
// ids, so the anchors are stable regardless of slugger behavior). Rendered
// pages carry NO Notes and NO Divergences sections (maintainer decision) —
// those Doc fields remain on the terminal sys.doc / `zester doc` surface.
func renderFamilyPage(family string, mis []modschema.ModuleInfo) (string, error) {
	if len(mis) == 0 {
		return "", fmt.Errorf("docgen: family %s has no members to render", family)
	}
	var b strings.Builder

	fmt.Fprintf(&b, "---\ntitle: %q\ndescription: %q\n---\n\n", family, familyDescription(family))
	for _, mi := range mis {
		b.WriteString(managedMarker(mi.Module))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Function index: every member with its summary, linked to its anchor.
	b.WriteString("| Module | Summary |\n|---|---|\n")
	for _, mi := range mis {
		fmt.Fprintf(&b, "| %s | %s |\n", memberLink(mi.Module), escapeSummaryCell(mi.Doc.Summary))
	}
	b.WriteString("\n")
	b.WriteString(requisitesBoilerplate)
	b.WriteString("\n")

	fps, err := collectFamilyParams(mis)
	if err != nil {
		return "", err
	}
	renderFamilyParamsSection(&b, fps, len(mis), pageSectionLevel)
	renderFamilySemTypes(&b, mis, pageSectionLevel)

	for _, mi := range mis {
		if err := renderMemberSection(&b, mi); err != nil {
			return "", err
		}
	}

	rendered := b.String()
	if err := assertNoJSX(family, rendered); err != nil {
		return "", err
	}
	return rendered, nil
}

// renderMemberSection renders one member's body under its anchored banner:
// summary → Source → description → Parameters (module-declared params only,
// with a pointer to Family Parameters for the component-declared ones) →
// Effects → Examples → See Also. No Notes, no Divergences.
func renderMemberSection(b *strings.Builder, mi modschema.ModuleInfo) error {
	fmt.Fprintf(b, "\n---\n\n%s `%s` [#%s]\n\n", heading(moduleBannerLevel), mi.Module, memberAnchor(mi.Module))
	if mi.Doc.Summary != "" {
		b.WriteString(mi.Doc.Summary)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(b, "**Source**: `%s`\n", sourcePath(mi.Kind, mi.Module))
	renderDescriptionSection(b, mi.Doc.Description)
	renderMemberParams(b, mi, nestedSectionLevel)
	renderPageEffects(b, mi.Doc.Effects, nestedSectionLevel)
	renderExamplesSection(b, mi.Doc.Examples, nestedSectionLevel)
	return renderSeeAlsoSection(b, mi.Module, mi.Doc.SeeAlso, nestedSectionLevel)
}

// renderMemberParams renders a member's Parameters section: a table of the
// params the module declares ITSELF, excluding the component-declared ones,
// which are documented once in the page's Family Parameters section — members
// exposing any get a one-line pointer instead of duplicate rows.
func renderMemberParams(b *strings.Builder, mi modschema.ModuleInfo, level int) {
	var own []modschema.Field
	var shared []string
	for _, f := range mi.Params {
		if f.DeclaredBy != "" {
			shared = append(shared, "`"+f.Name+"`")
			continue
		}
		own = append(own, f)
	}

	fmt.Fprintf(b, "\n---\n\n%s Parameters\n\n", heading(level))
	if len(own) == 0 && len(shared) == 0 {
		b.WriteString("This module takes no parameters of its own.\n")
		return
	}
	if len(own) > 0 {
		b.WriteString("| Parameter | Type | Required | Default | Description |\n")
		b.WriteString("|---|---|---|---|---|\n")
		for _, f := range own {
			required := "No"
			if f.Required {
				required = "Yes"
			}
			fmt.Fprintf(b, "| `%s` | `%s` | %s | %s | %s |\n",
				f.Name, displayParamType(f), required, paramDefaultCellKind(f, mi.Kind), escapeUsageCell(f.Usage))
		}
	}
	if len(shared) > 0 {
		if len(own) > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(b, "`%s` also accepts the family parameter%s %s — see [Family Parameters](#family-parameters).\n",
			mi.Module, plural(len(shared)), strings.Join(shared, ", "))
	}
}

// plural returns "s" for n != 1.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// memberFieldRef pairs an exposing member with its concrete Field copy — the
// member-supplied default/requiredness dimensions (§13) live on the member's
// own copy of a component field.
type memberFieldRef struct {
	module string
	field  modschema.Field
}

// familyParam is one component-declared parameter (Field.DeclaredBy non-empty
// — only available on LIVE-registry ModuleInfos; DeclaredBy is json:"-" and
// absent from docdata) aggregated across the family members that expose it.
type familyParam struct {
	// field is the canonical contract — the first exposing member's copy.
	field modschema.Field
	// members are the exposing members in page order.
	members []memberFieldRef
}

// collectFamilyParams gathers the component-declared parameters across the
// family's members, in first-appearance order, verifying every exposing member
// carries the identical contract (assertComponentParam) — the invariant that
// lets the page render each ONCE.
func collectFamilyParams(mis []modschema.ModuleInfo) ([]familyParam, error) {
	var order []string
	byName := map[string]*familyParam{}
	for _, mi := range mis {
		for _, f := range mi.Params {
			if f.DeclaredBy == "" {
				continue
			}
			fp, ok := byName[f.Name]
			if !ok {
				order = append(order, f.Name)
				byName[f.Name] = &familyParam{field: f, members: []memberFieldRef{{mi.Module, f}}}
				continue
			}
			if err := assertComponentParam(fp.field, f, fp.members[0].module, mi.Module); err != nil {
				return nil, err
			}
			fp.members = append(fp.members, memberFieldRef{mi.Module, f})
		}
	}
	out := make([]familyParam, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	return out, nil
}

// assertComponentParam verifies two members' copies of a component-declared
// parameter agree on every dimension the component fixes: declarer, type,
// aliases, usage, primary/lazy/sensitive, and — unless the component declares
// the dimension member-supplied (§13 memberdefault/memberrequired) — the
// default and requiredness too. A divergence is a LOUD generation error: a
// component's contract is identical by construction, so disagreement means the
// page would document a lie.
func assertComponentParam(a, c modschema.Field, aMod, cMod string) error {
	mismatch := func(dim string) error {
		return fmt.Errorf("docgen: family parameter %q differs between %s and %s on %s — component-declared parameters must carry one contract",
			a.Name, aMod, cMod, dim)
	}
	switch {
	case a.DeclaredBy != c.DeclaredBy:
		return mismatch("declaring component")
	case a.GoType != c.GoType || a.SemanticType != c.SemanticType:
		return mismatch("type")
	case !slices.Equal(a.Aliases, c.Aliases):
		return mismatch("aliases")
	case a.Usage != c.Usage:
		return mismatch("usage")
	case a.Primary != c.Primary || a.Lazy != c.Lazy || a.Sensitive != c.Sensitive:
		return mismatch("primary/lazy/sensitive flags")
	case a.DefaultMemberSupplied != c.DefaultMemberSupplied || a.RequiredMemberSupplied != c.RequiredMemberSupplied:
		return mismatch("member-supplied dimension declarations")
	case !a.RequiredMemberSupplied && a.Required != c.Required:
		return mismatch("requiredness")
	case !a.DefaultMemberSupplied && (a.HasDefault != c.HasDefault || a.Default != c.Default):
		return mismatch("default")
	}
	return nil
}

// renderFamilyParamsSection renders the Family Parameters section: one table
// row per component-declared parameter (rendered once for the whole family),
// an exposure list for params not on every member, and per-member sub-tables
// for the member-supplied dimensions (defaults / requiredness).
func renderFamilyParamsSection(b *strings.Builder, fps []familyParam, memberCount, level int) {
	if len(fps) == 0 {
		return
	}
	h, sub := heading(level), heading(level+1)
	fmt.Fprintf(b, "\n---\n\n%s Family Parameters\n\n", h)
	b.WriteString("These parameters are declared once by the family's shared parameter components — " +
		"every member that exposes one accepts the identical contract.\n\n")

	b.WriteString("| Parameter | Type | Required | Default | Description |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, fp := range fps {
		f := fp.field
		name := "`" + f.Name + "`"
		if len(f.Aliases) > 0 {
			quoted := make([]string, len(f.Aliases))
			for i, a := range f.Aliases {
				quoted[i] = "`" + a + "`"
			}
			name += fmt.Sprintf(" (alias%s %s)", plural(len(f.Aliases)), strings.Join(quoted, ", "))
		}
		required := "No"
		switch {
		case f.RequiredMemberSupplied:
			required = "member-specific (see below)"
		case f.Required:
			required = "Yes"
		}
		def := paramDefaultCell(f)
		if f.DefaultMemberSupplied {
			def = "member-specific (see below)"
		}
		fmt.Fprintf(b, "| %s | `%s` | %s | %s | %s |\n",
			name, displayParamType(f), required, def, escapeUsageCell(f.Usage))
	}

	var partial []familyParam
	for _, fp := range fps {
		if len(fp.members) < memberCount {
			partial = append(partial, fp)
		}
	}
	if len(partial) > 0 {
		b.WriteString("\nNot every member exposes every family parameter:\n\n")
		for _, fp := range partial {
			links := make([]string, len(fp.members))
			for i, m := range fp.members {
				links[i] = memberLink(m.module)
			}
			fmt.Fprintf(b, "- `%s` — %s\n", fp.field.Name, strings.Join(links, ", "))
		}
	}

	for _, fp := range fps {
		if fp.field.DefaultMemberSupplied {
			fmt.Fprintf(b, "\n%s `%s` defaults\n\n", sub, fp.field.Name)
			b.WriteString("| Module | Default |\n|---|---|\n")
			for _, m := range fp.members {
				fmt.Fprintf(b, "| %s | %s |\n", memberLink(m.module), paramDefaultCell(m.field))
			}
		}
		if fp.field.RequiredMemberSupplied {
			fmt.Fprintf(b, "\n%s `%s` requiredness\n\n", sub, fp.field.Name)
			b.WriteString("| Module | Required |\n|---|---|\n")
			for _, m := range fp.members {
				required := "No"
				if m.field.Required {
					required = "Yes"
				}
				fmt.Fprintf(b, "| %s | %s |\n", memberLink(m.module), required)
			}
		}
	}
}

// renderFamilySemTypes renders ONE Parameter Types section for the whole
// family page: the union of the members' semantic types, deduplicated by name
// (the vocabulary is sealed, so one name is one registered type), in
// first-appearance order.
func renderFamilySemTypes(b *strings.Builder, mis []modschema.ModuleInfo, level int) {
	seen := map[string]bool{}
	var union []modschema.SemanticTypeInfo
	for _, mi := range mis {
		for _, st := range mi.SemTypes {
			if seen[st.Name] {
				continue
			}
			seen[st.Name] = true
			union = append(union, st)
		}
	}
	if len(union) == 0 {
		return
	}
	h, sub := heading(level), heading(level+1)
	fmt.Fprintf(b, "\n---\n\n%s Parameter Types\n\n", h)
	for _, st := range union {
		fmt.Fprintf(b, "%s %s\n\n%s\n\n", sub, st.Name, st.Doc)
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

// renderParamsSection renders a plain Parameters table for every param the
// module exposes. It backs the combined execution-modules page (exec functions
// have no family components and no requisites); family pages render members
// through renderMemberParams instead.
func renderParamsSection(b *strings.Builder, mi modschema.ModuleInfo, level int) {
	if len(mi.Params) == 0 {
		return
	}
	fmt.Fprintf(b, "\n---\n\n%s Parameters\n\n", heading(level))
	b.WriteString("| Parameter | Type | Required | Default | Description |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, f := range mi.Params {
		required := "No"
		if f.Required {
			required = "Yes"
		}
		fmt.Fprintf(b, "| `%s` | `%s` | %s | %s | %s |\n",
			f.Name, displayParamType(f), required, paramDefaultCellKind(f, mi.Kind), escapeUsageCell(f.Usage))
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

func renderSeeAlsoSection(b *strings.Builder, module string, seeAlso []string, level int) error {
	if len(seeAlso) == 0 {
		return nil
	}
	fmt.Fprintf(b, "\n---\n\n%s See Also\n\n", heading(level))
	for _, s := range seeAlso {
		// A state module resolves to its family page + member anchor; an
		// execution-only module resolves to the shared execution-modules page +
		// its function anchor. cmd.run is a state module (the family page is its
		// canonical surface), so it resolves as a state target even when named
		// from an execution spec.
		if stateModules[s] {
			fmt.Fprintf(b, "- [%s](%s)\n", s, modulePageURL(s))
			continue
		}
		if execModuleSet[s] {
			fmt.Fprintf(b, "- [%s](%s#%s)\n", s, execPageURL, memberAnchor(s))
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
// — `{/* */}` comments and fenced code — so an `import` line or an unescaped
// `<Component` open tag can only have leaked in from an un-migrated hand page
// (§4: `<Tabs>` → sequential fenced blocks). Left in place it would silently
// break the website's MDX build; here it fails generation loudly instead.
// Fenced code blocks and inline `code` spans are literal in MDX, so their
// contents are exempt.
func assertNoJSX(module, rendered string) error {
	inFence := false
	for i, line := range strings.Split(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		// Blockquoted prose arrives with fenced-code delimiters prefixed by
		// "> "; strip an optional leading blockquote marker before the fence
		// check so code inside a blockquote toggles the fence and is still
		// skipped.
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
				"migrate <Component> prose to CommonMark (fenced blocks)", module, i+1, col+1, trimmed)
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

// paramDefaultCell renders the Default column. A sensitive parameter's
// Default is already redacted to empty by the schema layer (§2.5); this never
// prints a value for one regardless.
// usageCellEscaper escapes a Field.Usage string for embedding, verbatim, into
// a generated Markdown table cell. Usage is plain help prose from a module's
// `usage:"..."` tag, not authored CommonMark (unlike Description/Effects,
// whose authors deliberately backtick-wrap any placeholder like `<hex>` or
// `<branch>`) — a usage string is free to contain a BARE "<placeholder>"
// token (see archive.extracted's "sha256=<hex>", cmd.run's "name: <command>",
// git.latest's "origin/<branch>") or a bare "${name}"-style backreference
// token (see file.replace's "$1/${name} backreferences"). MDX (unlike plain
// Markdown, see the managedMarkerPrefix comment above for the same class of
// gotcha) parses a bare "<word>" ANYWHERE in the document as an unclosed JSX
// tag, and a bare "{word}" as a JavaScript expression container (evaluated as
// a reference to an undefined identifier at prerender time) — either fails
// the site build outright rather than just rendering oddly, so every angle
// bracket and curly brace in this untrusted-w.r.t.-markup text must be
// escaped. Curly braces use numeric character references (no named HTML
// entity exists for them). "&" is escaped too (so an already-escaped sequence
// is never double-unescaped) and "|" is escaped (a literal pipe would
// otherwise terminate the table cell early). Entities decode back to the
// literal characters in the rendered page, so this is visually a no-op for
// any usage string that happens not to need it.
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

// escapeSummaryCell escapes a Doc.Summary for the function-index table cell.
// Unlike usage strings, summaries ARE authored CommonMark (inline code spans
// are legitimate and must render), so only a literal pipe — which would
// terminate the table cell early — is escaped.
func escapeSummaryCell(summary string) string {
	return strings.ReplaceAll(summary, "|", "\\|")
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

// writeFamilyPage writes the rendered family page for the members of family.
// If path already exists without the managed marker, it refuses to overwrite
// it UNLESS claim is true (the one-time adoption a migration PR performs
// explicitly) — the markerless-overwrite guard (§8) that protects hand-written
// pages.
func writeFamilyPage(path, family string, mis []modschema.ModuleInfo, claim bool) error {
	rendered, err := renderFamilyPage(family, mis)
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil {
		if !hasManagedMarker(existing) && !claim {
			return fmt.Errorf("docgen: refusing to overwrite markerless page %s for family %s "+
				"(pass --claim to adopt it)", path, family)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("docgen: read %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(rendered), 0644); err != nil {
		return fmt.Errorf("docgen: write %s: %w", path, err)
	}
	return nil
}

// cleanupStalePages deletes previously generated pages whose slug is no longer
// produced: any .mdx directly in dir that carries the managed marker and is
// not in produced. Hand-written pages (query.mdx, starlark.mdx,
// developing.mdx, index.mdx — no marker) are never touched. Returns the
// removed slugs, sorted by directory order (ReadDir sorts by name).
func cleanupStalePages(dir string, produced map[string]bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("docgen: read %s: %w", dir, err)
	}
	var removed []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".mdx") {
			continue
		}
		slug := strings.TrimSuffix(name, ".mdx")
		if produced[slug] {
			continue
		}
		path := filepath.Join(dir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			return removed, fmt.Errorf("docgen: read %s: %w", path, err)
		}
		if !hasManagedMarker(content) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return removed, fmt.Errorf("docgen: remove stale page %s: %w", path, err)
		}
		removed = append(removed, slug)
	}
	return removed, nil
}
