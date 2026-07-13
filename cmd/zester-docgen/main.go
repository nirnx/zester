// Command zester-docgen generates Zester's self-documenting-module artifacts
// (keystone spec §8) from the live module registries: the modules guide
// meta.json (wholesale nav, all 47 built-in state modules), a marker-guarded
// MDX page per module that has a registered modschema.Spec, the combined
// JSON Schema artifact, and pkg/moduledoc's embedded docdata.json.
//
// Run from the repo root: go run ./cmd/zester-docgen [--claim=module1,module2]
//
// CI gate 1 (§9) runs it bare and requires the working tree to already match:
// `go run ./cmd/zester-docgen && git add -A && git diff --cached --exit-code
// website/ pkg/moduledoc`. --claim is a one-time, human-invoked flag for the
// PR that migrates a module and adopts its previously hand-written page; it
// is never passed in CI.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state/modules"
)

func main() {
	var claimFlag string
	var root string
	flag.StringVar(&claimFlag, "claim", "", "comma-separated module names allowed to adopt an existing markerless page")
	flag.StringVar(&root, "root", ".", "repo root (containing website/ and pkg/moduledoc/)")
	flag.Parse()

	claimed := map[string]bool{}
	for m := range strings.SplitSeq(claimFlag, ",") {
		if m = strings.TrimSpace(m); m != "" {
			claimed[m] = true
		}
	}

	if err := run(root, claimed); err != nil {
		fmt.Fprintf(os.Stderr, "zester-docgen: %v\n", err)
		os.Exit(1)
	}
}

func run(root string, claimed map[string]bool) error {
	reg := buildStateRegistry()
	names := stateModuleNames(reg)

	// stateSpecd drives page emission (state-only: §8's PageGroups/families
	// are state-module slugs; an exec/dispatch module documents through its
	// state counterpart's dual-surface appendix instead of its own page —
	// Phase 3, §7). allInfos additionally feeds the schema artifact and
	// docdata.json, which are kind-agnostic and so include execmod entries
	// too, the moment one is migrated.
	var stateSpecd []modschema.ModuleInfo
	for _, name := range names {
		mi, ok := reg.Describe(name)
		if !ok {
			continue // legacy module: no Spec yet, nothing to describe/generate
		}
		stateSpecd = append(stateSpecd, mi)
	}

	// State module names, so a dual-surface module (cmd.run — both a state and
	// an execution module) is documented by its STATE page/schema/docdata and
	// its execution spec is excluded from the exec surface (it would otherwise
	// duplicate the module name in the schema $defs and docdata).
	stateNames := map[string]bool{}
	for _, mi := range stateSpecd {
		stateNames[mi.Module] = true
	}

	execReg := buildExecmodRegistry()
	var execOnly []modschema.ModuleInfo // execution-only: the exec page + docdata set
	for _, name := range execmodSpecNames(execReg) {
		mi, ok := execReg.Describe(name)
		if !ok {
			continue
		}
		if stateNames[mi.Module] {
			continue // dual-surface (cmd.run): the state page/schema/docdata own it
		}
		execOnly = append(execOnly, mi)
	}

	// allInfos drives Effects-by-kind coverage and docdata (both kind-agnostic):
	// every migrated state module plus every execution-only function. The JSON
	// Schema artifact, by contrast, is a STATE-FILE schema (state ID -> module ->
	// params) — execution functions are never state-file constructs, so they are
	// excluded from it and only stateSpecd feeds renderModuleSchemaArtifact.
	// Dispatch specials (state.apply, facts.*, settings.*, pillar.*, event.send)
	// join docdata so offline `zester doc` answers exactly what live sys.doc
	// answers. They stay out of the JSON Schema (not state-file constructs
	// beyond their own pages) and out of the exec reference page.
	var dispatchInfos []modschema.ModuleInfo
	for _, name := range modules.DispatchNames() {
		if mi, ok := modules.DispatchInfo(name); ok {
			dispatchInfos = append(dispatchInfos, mi)
		}
	}
	allInfos := append(append(append([]modschema.ModuleInfo(nil), stateSpecd...), execOnly...), dispatchInfos...)

	// Effects-by-kind coverage (§4/§9 gate 3) — a module reaching docgen with
	// an incomplete or fake Doc.Effects fails generation loudly rather than
	// publishing a bad page.
	for _, mi := range allInfos {
		if err := modschema.ValidateEffects(mi.Kind, mi.Doc.Effects); err != nil {
			return fmt.Errorf("module %s: %w", mi.Module, err)
		}
	}

	// Example/SeeAlso validation (§8) before writing anything.
	// Render + compile the artifact BEFORE example validation so every
	// self-contained state example is exercised against the schema editors
	// will actually consume (review finding).
	schemaJSON, err := renderModuleSchemaArtifact(stateSpecd)
	if err != nil {
		return err
	}
	artifactSchema, err := compileArtifactSchema(schemaJSON)
	if err != nil {
		return err
	}

	for _, mi := range stateSpecd {
		skips, err := validateModuleExamples(reg, artifactSchema, mi.Module, mi.Doc.Examples, sensitiveExampleKeys(mi.Params))
		if err != nil {
			return err
		}
		for _, s := range skips {
			fmt.Fprintf(os.Stderr, "zester-docgen: %s: example %q recorded-skipped: %s\n", s.Module, s.Title, s.Reason)
		}
	}
	for _, mi := range execOnly {
		skips, err := validateNonStateExamples(mi.Module, mi.Doc.Examples, sensitiveExampleKeys(mi.Params))
		if err != nil {
			return err
		}
		for _, s := range skips {
			fmt.Fprintf(os.Stderr, "zester-docgen: %s: example %q recorded-skipped: %s\n", s.Module, s.Title, s.Reason)
		}
	}

	modulesDir := filepath.Join(root, "website", "content", "docs", "guides", "modules")

	// meta.json: wholesale, independent of spec presence.
	metaJSON, err := renderMetaJSON()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(modulesDir, "meta.json"), metaJSON, 0644); err != nil {
		return fmt.Errorf("docgen: write meta.json: %w", err)
	}

	// Pages: gated on spec presence, state modules only (see stateSpecd above).
	// Group by page slug so the N:1 PageGroups (file.comment/file.uncomment share
	// file-comment) render ONE combined page instead of the members overwriting
	// each other. Slug order follows stateSpecd (registration) order for
	// determinism.
	slugOrder, bySlug, err := groupBySlug(stateSpecd)
	if err != nil {
		return err
	}
	for _, slug := range slugOrder {
		mis := bySlug[slug]
		claim := false
		for _, mi := range mis {
			if claimed[mi.Module] {
				claim = true
			}
		}
		path := filepath.Join(modulesDir, slug+".mdx")
		if err := writeModulePageGroup(path, mis, claim, isDistinctParamSlug(slug)); err != nil {
			return err
		}
	}

	// The FIRST-EVER execution-module reference page: ONE combined page under
	// guides/ (a flat .mdx like reactor.mdx / scheduling.mdx), covering every
	// execution-only function. Additive — no state page or URL changes.
	if len(execOnly) > 0 {
		guidesDir := filepath.Join(root, "website", "content", "docs", "guides")
		execPath := filepath.Join(guidesDir, execPageSlug+".mdx")
		if err := writeExecModulesPage(execPath, execOnly, claimed[execPageSlug]); err != nil {
			return err
		}
	}

	// Combined JSON Schema artifact (state-file schema): gated on spec presence,
	// STATE modules only (execution functions have no state-file representation).
	// schemaJSON was rendered above (and exercised by example validation).
	schemaDir := filepath.Join(root, "website", "public", "schema")
	if err := os.MkdirAll(schemaDir, 0755); err != nil {
		return fmt.Errorf("docgen: mkdir %s: %w", schemaDir, err)
	}
	if err := os.WriteFile(filepath.Join(schemaDir, "zester-modules.schema.json"), schemaJSON, 0644); err != nil {
		return fmt.Errorf("docgen: write schema artifact: %w", err)
	}

	// docdata.json: gated on spec presence.
	docdataJSON, err := renderDocdataJSON(allInfos)
	if err != nil {
		return err
	}
	moduledocDir := filepath.Join(root, "pkg", "moduledoc")
	if err := os.WriteFile(filepath.Join(moduledocDir, "docdata.json"), docdataJSON, 0644); err != nil {
		return fmt.Errorf("docgen: write docdata.json: %w", err)
	}

	return nil
}

// groupBySlug groups the state-module ModuleInfos by their page slug (the §8 N:1
// PageGroups: several module names — file.comment/file.uncomment, host.present/
// host.absent, … — share one page). first-seen order is preserved so the
// generated page order is deterministic (it follows stateSpecd registration
// order). A module absent from moduleToSlug is a generation error rather than a
// silently missing page.
func groupBySlug(stateSpecd []modschema.ModuleInfo) (order []string, bySlug map[string][]modschema.ModuleInfo, err error) {
	bySlug = map[string][]modschema.ModuleInfo{}
	for _, mi := range stateSpecd {
		slug, ok := moduleToSlug[mi.Module]
		if !ok {
			return nil, nil, fmt.Errorf("docgen: module %s has no page-group slug", mi.Module)
		}
		if _, seen := bySlug[slug]; !seen {
			order = append(order, slug)
		}
		bySlug[slug] = append(bySlug[slug], mi)
	}
	return order, bySlug, nil
}
