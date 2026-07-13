package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/moduledoc"
)

// docCmd renders the embedded self-documenting-module docs (pkg/moduledoc)
// through the SAME modschema.RenderText a connected daemon uses for a live
// sys.doc, so `zester doc file.managed` is byte-identical to the peel-side
// answer (TestDocdataMatchesLive pins embedded == live). It is fully OFFLINE:
// it reads only the embedded docdata and never contacts a master, so it works
// on a peel-only box with no config and no reachable NATS.
var docCmd = &cobra.Command{
	Use:   "doc [module]",
	Short: "Show documentation for self-documenting modules (offline)",
	Long: `Show module documentation from the embedded, offline docs.

With no argument, lists every documented module grouped by family. With a
module name, renders that module's full documentation — parameters, effects,
examples, and notes — identically to the peel-side 'sys.doc'. Add --json to
emit the structured ModuleInfo instead of rendered text.

  zester doc                # list documented modules
  zester doc file.managed   # full docs for one module
  zester doc pkg.installed --json`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeModuleNames,
	RunE:              runDoc,
}

func init() {
	docCmd.Flags().Bool("json", false, "emit structured ModuleInfo JSON instead of rendered text")
	rootCmd.AddCommand(docCmd)

	// Shell completion for the <module.function> positional of the exec form
	// (`zester '<target>' <TAB>`): suggest documented module names.
	rootCmd.ValidArgsFunction = completeExecModule
}

// runDoc is `zester doc`'s handler. Offline: it reads only pkg/moduledoc.
func runDoc(cmd *cobra.Command, args []string) error {
	// A corrupt embed is a build-time packaging defect; surface it loudly
	// rather than silently reporting every module as undocumented.
	if err := moduledoc.LoadError(); err != nil {
		return err
	}

	jsonOut, _ := cmd.Flags().GetBool("json")
	out := cmd.OutOrStdout()

	if len(args) == 0 {
		if jsonOut {
			return writeDocJSON(out, moduledoc.All())
		}
		writeModuleIndex(out, moduledoc.All())
		return nil
	}

	module := args[0]
	mi, ok := moduledoc.Lookup(module)
	if !ok {
		return unknownModuleError(module)
	}
	if jsonOut {
		return writeDocJSON(out, mi)
	}
	fmt.Fprintln(out, modschema.RenderText(mi))
	return nil
}

// writeModuleIndex prints the documented modules grouped by family (the segment
// before the first '.'), each with its one-line summary.
func writeModuleIndex(w io.Writer, mods []modschema.ModuleInfo) {
	if len(mods) == 0 {
		fmt.Fprintln(w, "No modules are documented in this build.")
		return
	}

	byFamily := map[string][]modschema.ModuleInfo{}
	for _, mi := range mods {
		fam := moduleFamily(mi.Module)
		byFamily[fam] = append(byFamily[fam], mi)
	}
	families := make([]string, 0, len(byFamily))
	for f := range byFamily {
		families = append(families, f)
	}
	sort.Strings(families)

	fmt.Fprintln(w, "Documented modules:")
	for _, fam := range families {
		mis := byFamily[fam]
		sort.Slice(mis, func(i, j int) bool { return mis[i].Module < mis[j].Module })
		width := 0
		for _, mi := range mis {
			if len(mi.Module) > width {
				width = len(mi.Module)
			}
		}
		fmt.Fprintf(w, "\n%s\n", fam)
		for _, mi := range mis {
			if summary := firstLine(mi.Doc.Summary); summary != "" {
				fmt.Fprintf(w, "  %-*s  %s\n", width, mi.Module, summary)
			} else {
				fmt.Fprintf(w, "  %s\n", mi.Module)
			}
		}
	}
	fmt.Fprintln(w, "\nRun 'zester doc <module>' for full documentation.")
}

// writeDocJSON writes v as indented JSON followed by a newline.
func writeDocJSON(w io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("doc: marshal json: %w", err)
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

// unknownModuleError builds a clear error for an unknown module, inlining
// nearest-match suggestions (edit distance <= 2) when any exist.
func unknownModuleError(module string) error {
	suggestions := suggestModules(module)
	if len(suggestions) > 0 {
		quoted := make([]string, len(suggestions))
		for i, s := range suggestions {
			quoted[i] = fmt.Sprintf("%q", s)
		}
		return fmt.Errorf("unknown module %q (did you mean %s?); run 'zester doc' to list documented modules",
			module, strings.Join(quoted, " or "))
	}
	return fmt.Errorf("unknown module %q; run 'zester doc' to list documented modules", module)
}

// suggestModules returns documented module names within edit distance 2 of the
// query, nearest first (name tiebreak), capped at 3. modschema exposes no
// public suggestion helper (its levenshtein is unexported and bound to a
// CompiledSchema's parameter names), so this is a small local implementation.
func suggestModules(module string) []string {
	type cand struct {
		name string
		dist int
	}
	var cands []cand
	for _, mi := range moduledoc.All() {
		if d := editDistance(module, mi.Module); d <= 2 {
			cands = append(cands, cand{mi.Module, d})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].dist != cands[j].dist {
			return cands[i].dist < cands[j].dist
		}
		return cands[i].name < cands[j].name
	})
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.name)
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// moduleFamily is the segment before the first '.' (the module family), or the
// whole name if it has no dot.
func moduleFamily(module string) string {
	if i := strings.IndexByte(module, '.'); i >= 0 {
		return module[:i]
	}
	return module
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// editDistance is the Levenshtein distance between a and b (local copy of the
// same algorithm modschema uses internally for parameter-name suggestions).
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

// completeModuleNames completes docCmd's single module argument from the
// embedded docdata.
func completeModuleNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return moduleNameCandidates(toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeExecModule completes the <module.function> positional of the root
// exec form. args[0] is the target expression, so module completion applies
// only to the second positional.
func completeExecModule(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 1 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return moduleNameCandidates(toComplete), cobra.ShellCompDirectiveNoFileComp
}

// moduleNameCandidates returns documented module names with the given prefix.
func moduleNameCandidates(prefix string) []string {
	var out []string
	for _, mi := range moduledoc.All() {
		if strings.HasPrefix(mi.Module, prefix) {
			out = append(out, mi.Module)
		}
	}
	return out
}
