package modschema

import (
	"regexp"
	"strings"
)

// ANSI escape sequences used by ColorizeDoc. 16-color codes only — the doc
// surfaces render in arbitrary terminals (SSH sessions, consoles), so nothing
// here assumes 256-color or truecolor support.
const (
	ansiReset = "\033[0m"
	ansiBold  = "\033[1m"
	ansiDim   = "\033[2m"

	ansiModuleName = "\033[1;36m" // bold cyan — the module.function header
	ansiHeading    = "\033[1;33m" // bold yellow — section headings
	ansiParamName  = "\033[1;32m" // bold green — parameter / semantic-type names
	ansiFlagWarn   = "\033[33m"   // yellow — the "required" flag, warning notes
	ansiFlagDanger = "\033[91m"   // bright red — the "sensitive" flag, danger notes
	ansiFlagOther  = "\033[35m"   // magenta — remaining flags, unknown note levels
	ansiNoteInfo   = "\033[36m"   // cyan — info notes
)

// docSection tracks which RenderText section the colorizer is inside, so
// indented lines are interpreted by the section that owns them (a 2-space line
// is a parameter under "Parameters:" but an effect mode under "Effects:").
type docSection int

const (
	docProse docSection = iota // header, summary, description — free text
	docParams
	docSemTypes
	docEffects
	docExamples
	docNotes
)

// Line shapes from RenderText's pinned grammar (render.go). Anchored to the
// exact indents the renderer emits, so free-form doc prose never matches.
var (
	// "pkg.installed (state)" [+ optional AlsoExecmod overlay].
	docHeaderRe = regexp.MustCompile(`^(\S+) (\([^)]+\))(.*)$`)
	// "  name (semantic_type, required, primary)".
	docParamRe = regexp.MustCompile(`^  (\S+) \((.+)\)$`)
	// "  Check" / "  Apply" / "  Revert" / "  Execution".
	docEffectRe = regexp.MustCompile(`^  (Check|Apply|Revert|Execution)$`)
	// "  1. Install nginx [yaml]".
	docExampleRe = regexp.MustCompile(`^  (\d+\.) (.*) (\[\S+\])$`)
	// "  [warning] Title text". The level may be empty — several shipped docs
	// carry level-less notes, rendered as "  [] Title" — so \w* not \w+.
	docNoteRe = regexp.MustCompile(`^  \[(\w*)\] (.*)$`)
	// "  duration" — a semantic-type name under "Parameter Types:".
	docSemTypeRe = regexp.MustCompile(`^  (\S+)$`)
)

// ColorizeDoc injects ANSI color into text rendered by RenderText /
// RenderTextAll, for terminal display. It is a pure presentation layer over
// the pinned plain-text grammar: RenderText stays the canonical, byte-stable
// currency (the wire format sys.doc returns, the docgen/embedded-doc parity
// anchor), and callers colorize at print time only when the output is going to
// a color-capable terminal.
//
// Invariant (pinned by tests): stripping the injected escape sequences
// reproduces the input byte-for-byte. Lines that don't match the doc grammar —
// including entirely non-doc input such as the bare sys.doc name index — pass
// through untouched, so colorizing is always safe to apply to a sys.doc reply.
//
// Known limits: classification is purely line-shape based, so free-form doc
// prose that byte-coincides with a renderer-owned shape is mis-colored — a
// column-0 "Parameters:" or "---" line inside a Description (the latter also
// re-arms header matching), or a 6-space usage line that itself starts with
// "aliases: " or "default: ". All such cases are cosmetic only (the strip
// invariant still holds — content is never altered), and no shipped module doc
// triggers any of them.
func ColorizeDoc(text string) string {
	lines := strings.Split(text, "\n")
	section := docProse
	// The first non-blank line of the document — and of each RenderTextAll
	// member after a "---" rule — is a module header.
	expectHeader := true

	for i, line := range lines {
		if line == "" {
			continue
		}
		if line == "---" {
			lines[i] = ansiDim + line + ansiReset
			section = docProse
			expectHeader = true
			continue
		}
		if expectHeader {
			expectHeader = false
			if m := docHeaderRe.FindStringSubmatch(line); m != nil {
				lines[i] = ansiModuleName + m[1] + ansiReset + " " + ansiDim + m[2] + m[3] + ansiReset
			}
			continue
		}

		// Section headings (column 0, exact match — renderer-owned strings).
		switch line {
		case "Parameters:", "Parameter Types:", "Effects:", "Examples:", "Notes:":
			switch line {
			case "Parameters:":
				section = docParams
			case "Parameter Types:":
				section = docSemTypes
			case "Effects:":
				section = docEffects
			case "Examples:":
				section = docExamples
			case "Notes:":
				section = docNotes
			}
			lines[i] = ansiHeading + line + ansiReset
			continue
		}
		if rest, ok := strings.CutPrefix(line, "Divergences: "); ok {
			lines[i] = ansiHeading + "Divergences:" + ansiReset + " " + rest
			section = docProse
			continue
		}
		if rest, ok := strings.CutPrefix(line, "See Also: "); ok {
			lines[i] = ansiHeading + "See Also:" + ansiReset + " " + rest
			section = docProse
			continue
		}

		switch section {
		case docParams:
			if m := docParamRe.FindStringSubmatch(line); m != nil {
				lines[i] = "  " + ansiParamName + m[1] + ansiReset + " (" + colorizeParamParen(m[2]) + ")"
				continue
			}
			if rest, ok := strings.CutPrefix(line, "      aliases: "); ok {
				lines[i] = "      " + ansiDim + "aliases:" + ansiReset + " " + rest
			} else if rest, ok := strings.CutPrefix(line, "      default: "); ok {
				lines[i] = "      " + ansiDim + "default:" + ansiReset + " " + rest
			}
		case docSemTypes:
			if m := docSemTypeRe.FindStringSubmatch(line); m != nil {
				lines[i] = "  " + ansiParamName + m[1] + ansiReset
			}
		case docEffects:
			if m := docEffectRe.FindStringSubmatch(line); m != nil {
				lines[i] = "  " + ansiBold + m[1] + ansiReset
			}
		case docExamples:
			if m := docExampleRe.FindStringSubmatch(line); m != nil {
				lines[i] = "  " + ansiDim + m[1] + ansiReset + " " + ansiBold + m[2] + ansiReset + " " + ansiDim + m[3] + ansiReset
			}
		case docNotes:
			if m := docNoteRe.FindStringSubmatch(line); m != nil {
				lines[i] = "  " + noteLevelColor(m[1]) + "[" + m[1] + "]" + ansiReset + " " + ansiBold + m[2] + ansiReset
			}
		}
	}
	return strings.Join(lines, "\n")
}

// colorizeParamParen colors the parenthesized portion of a parameter line: the
// leading type dim, then each flag by weight (required draws the eye, sensitive
// is loud, the rest are quiet metadata).
func colorizeParamParen(inner string) string {
	parts := strings.Split(inner, ", ")
	for i, p := range parts {
		switch {
		case i == 0:
			parts[i] = ansiDim + p + ansiReset
		case p == "required":
			parts[i] = ansiFlagWarn + p + ansiReset
		case p == "sensitive":
			parts[i] = ansiFlagDanger + p + ansiReset
		default:
			parts[i] = ansiFlagOther + p + ansiReset
		}
	}
	return strings.Join(parts, ", ")
}

// noteLevelColor maps a note level to its color: warnings yellow, dangerous
// levels red, info cyan, an empty level dim (its "[]" badge carries no
// signal), anything unrecognized magenta (visible, not alarming).
func noteLevelColor(level string) string {
	switch level {
	case "warning", "warn":
		return ansiFlagWarn
	case "danger", "caution", "error", "security":
		return ansiFlagDanger
	case "info":
		return ansiNoteInfo
	case "":
		return ansiDim
	default:
		return ansiFlagOther
	}
}
