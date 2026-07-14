package modschema

import (
	"fmt"
	"strings"
)

// RenderTextAll renders several modules' documentation as ONE document — each
// through RenderText, joined by a blank line and a rule — in the given order.
// It backs the FAMILY form of the doc surfaces (`sys.doc ssh_auth` /
// `zester doc ssh_auth` render every ssh_auth.* module), so the live and
// offline family views share one shape.
func RenderTextAll(infos []ModuleInfo) string {
	parts := make([]string, 0, len(infos))
	for _, mi := range infos {
		parts = append(parts, strings.TrimRight(RenderText(mi), "\n"))
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// RenderText renders a ModuleInfo as deterministic plain text: the same
// currency sys.doc, `zester doc`, and docgen's page anatomy are derived from,
// rendered without any markup. Output depends only on mi's fields, never on
// ambient state, so a live Spec.Info() and its embedded pkg/moduledoc
// projection render identically (TestDocdataMatchesLive, §7).
//
// Sensitive fields never expose a value: Field.Default is already redacted to
// empty at the schema layer (§2.5), and RenderText additionally never renders
// anything else about a sensitive field beyond its name, type, and flags.
func RenderText(mi ModuleInfo) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s (%s)", mi.Module, mi.Kind)
	if mi.AlsoExecmod {
		fmt.Fprintf(&b, " — also reachable as an execution module: salt['%s']", mi.Module)
	}
	b.WriteString("\n")

	if mi.Doc.Summary != "" {
		b.WriteString("\n")
		b.WriteString(mi.Doc.Summary)
		b.WriteString("\n")
	}
	if mi.Doc.Description != "" {
		b.WriteString("\n")
		b.WriteString(mi.Doc.Description)
		b.WriteString("\n")
	}

	renderParams(&b, mi.Params)
	renderSemTypes(&b, mi.SemTypes)
	renderEffects(&b, mi.Doc.Effects)
	renderExamples(&b, mi.Doc.Examples)
	renderNotes(&b, mi.Doc.Notes)

	if len(mi.Doc.Divergences) > 0 {
		fmt.Fprintf(&b, "\nDivergences: %s\n", strings.Join(mi.Doc.Divergences, ", "))
	}
	if len(mi.Doc.SeeAlso) > 0 {
		fmt.Fprintf(&b, "\nSee Also: %s\n", strings.Join(mi.Doc.SeeAlso, ", "))
	}

	return b.String()
}

func renderParams(b *strings.Builder, params []Field) {
	if len(params) == 0 {
		return
	}
	b.WriteString("\nParameters:\n")
	for _, f := range params {
		typ := f.GoType
		if f.SemanticType != "" {
			typ = f.SemanticType
		}
		var flags []string
		if f.Required {
			flags = append(flags, "required")
		}
		if f.Primary {
			flags = append(flags, "primary")
		}
		if f.Lazy {
			flags = append(flags, "lazy")
		}
		if f.Sensitive {
			flags = append(flags, "sensitive")
		}
		flagStr := ""
		if len(flags) > 0 {
			flagStr = ", " + strings.Join(flags, ", ")
		}
		fmt.Fprintf(b, "  %s (%s%s)\n", f.Name, typ, flagStr)
		if len(f.Aliases) > 0 {
			fmt.Fprintf(b, "      aliases: %s\n", strings.Join(f.Aliases, ", "))
		}
		if f.Usage != "" {
			fmt.Fprintf(b, "      %s\n", f.Usage)
		}
		// Field.Default is already redacted to empty for a sensitive field at
		// the schema layer (§2.5); the explicit !f.Sensitive check here is a
		// second, independent line of defense so RenderText itself never
		// renders a sensitive value even if a caller-constructed ModuleInfo
		// (e.g. a hand-built fixture, or a future renderer bug upstream)
		// populates Default on one.
		if f.HasDefault && f.Default != "" && !f.Sensitive {
			suffix := ""
			if f.Lazy {
				suffix = " (lazy — applied by the module, not materialized here)"
			}
			fmt.Fprintf(b, "      default: %s%s\n", f.Default, suffix)
		}
	}
}

func renderSemTypes(b *strings.Builder, semTypes []SemanticTypeInfo) {
	if len(semTypes) == 0 {
		return
	}
	b.WriteString("\nParameter Types:\n")
	for _, st := range semTypes {
		fmt.Fprintf(b, "  %s\n", st.Name)
		if st.Doc != "" {
			fmt.Fprintf(b, "      %s\n", st.Doc)
		}
	}
}

func renderEffects(b *strings.Builder, e Effects) {
	if e.Check == "" && e.Apply == "" && e.Revert == "" && e.Execution == "" {
		return
	}
	b.WriteString("\nEffects:\n")
	if e.Check != "" {
		fmt.Fprintf(b, "  Check\n    %s\n", e.Check)
	}
	if e.Apply != "" {
		fmt.Fprintf(b, "  Apply\n    %s\n", e.Apply)
	}
	if e.Revert != "" {
		fmt.Fprintf(b, "  Revert\n    %s\n", e.Revert)
	}
	if e.Execution != "" {
		fmt.Fprintf(b, "  Execution\n    %s\n", e.Execution)
	}
}

func renderExamples(b *strings.Builder, examples []Example) {
	if len(examples) == 0 {
		return
	}
	b.WriteString("\nExamples:\n")
	for i, ex := range examples {
		fmt.Fprintf(b, "  %d. %s [%s]\n", i+1, ex.Title, ex.Kind)
		if ex.Explanation != "" {
			fmt.Fprintf(b, "     %s\n", ex.Explanation)
		}
		for line := range strings.SplitSeq(strings.TrimRight(ex.Code, "\n"), "\n") {
			fmt.Fprintf(b, "     %s\n", line)
		}
	}
}

func renderNotes(b *strings.Builder, notes []Note) {
	if len(notes) == 0 {
		return
	}
	b.WriteString("\nNotes:\n")
	for _, n := range notes {
		fmt.Fprintf(b, "  [%s] %s\n", n.Level, n.Title)
		if n.Body != "" {
			fmt.Fprintf(b, "      %s\n", n.Body)
		}
	}
}
