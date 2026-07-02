package migrate

import (
	"fmt"
	"strings"
)

// FormatReport formats the migration result for a single file as a human-readable report.
func FormatReport(r *Result) string {
	if len(r.Changes) == 0 && len(r.Warnings) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "=== %s → %s ===\n\n", r.Path, r.NewPath)

	for _, c := range r.Changes {
		fmt.Fprintf(&b, "  Line %d:  %s\n", c.Line, strings.TrimSpace(c.Before))
		fmt.Fprintf(&b, "        →  %s\n\n", strings.TrimSpace(c.After))
	}

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "  ⚠ Line %d: %s\n", w.Line, w.Message)
		if w.Context != "" && !strings.Contains(w.Message, "\n") {
			fmt.Fprintf(&b, "             %s\n", strings.TrimSpace(w.Context))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// FormatSummary formats an aggregate summary across all results.
func FormatSummary(results []*Result) string {
	total := len(results)
	changed := 0
	totalChanges := 0
	totalWarnings := 0

	for _, r := range results {
		if len(r.Changes) > 0 || len(r.Warnings) > 0 {
			changed++
		}
		totalChanges += len(r.Changes)
		totalWarnings += len(r.Warnings)
	}

	var b strings.Builder
	b.WriteString("--- Summary ---\n")
	fmt.Fprintf(&b, "Files:    %d processed, %d changed, %d unchanged\n", total, changed, total-changed)
	fmt.Fprintf(&b, "Changes:  %d automated\n", totalChanges)
	fmt.Fprintf(&b, "Warnings: %d (require manual review)\n", totalWarnings)
	return b.String()
}

// FormatWriteAction returns a one-line status for a file write operation.
func FormatWriteAction(r *Result, written bool) string {
	if !written {
		if len(r.Changes) == 0 && len(r.Warnings) == 0 {
			return fmt.Sprintf("Skipped: %s (no changes)", r.Path)
		}
		return fmt.Sprintf("Skipped: %s (dry run)", r.Path)
	}

	if r.Path == r.NewPath {
		return fmt.Sprintf("Written: %s (updated in place)", r.Path)
	}
	return fmt.Sprintf("Written: %s", r.NewPath)
}
