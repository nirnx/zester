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

	execReg := buildExecmodRegistry()
	var execSpecd []modschema.ModuleInfo
	for _, name := range execmodSpecNames(execReg) {
		mi, ok := execReg.Describe(name)
		if !ok {
			continue
		}
		execSpecd = append(execSpecd, mi)
	}

	allInfos := append(append([]modschema.ModuleInfo(nil), stateSpecd...), execSpecd...)

	// Effects-by-kind coverage (§4/§9 gate 3) — a module reaching docgen with
	// an incomplete or fake Doc.Effects fails generation loudly rather than
	// publishing a bad page.
	for _, mi := range allInfos {
		if err := modschema.ValidateEffects(mi.Kind, mi.Doc.Effects); err != nil {
			return fmt.Errorf("module %s: %w", mi.Module, err)
		}
	}

	// Example/SeeAlso validation (§8) before writing anything.
	for _, mi := range stateSpecd {
		skips, err := validateModuleExamples(reg, mi.Module, mi.Doc.Examples, sensitiveExampleKeys(mi.Params))
		if err != nil {
			return err
		}
		for _, s := range skips {
			fmt.Fprintf(os.Stderr, "zester-docgen: %s: example %q recorded-skipped: %s\n", s.Module, s.Title, s.Reason)
		}
	}
	for _, mi := range execSpecd {
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
	for _, mi := range stateSpecd {
		slug, ok := moduleToSlug[mi.Module]
		if !ok {
			return fmt.Errorf("docgen: module %s has no page-group slug", mi.Module)
		}
		path := filepath.Join(modulesDir, slug+".mdx")
		if err := writeModulePage(path, mi, claimed[mi.Module]); err != nil {
			return err
		}
	}

	// Combined JSON Schema artifact: gated on spec presence.
	schemaJSON, err := renderModuleSchemaArtifact(allInfos)
	if err != nil {
		return err
	}
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
