package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/nirnx/zester/pkg/modschema"
)

// This file owns the FIRST-EVER execution-module reference page (keystone spec
// §8, exec surface). Unlike state modules — one page each under guides/modules/,
// grouped by nav family — the execution functions are collected onto ONE
// combined page, guides/execution-modules.mdx, because there are relatively few
// and they read well as a single scannable reference. cmd.run is deliberately
// NOT here: it is a dual-surface module documented by its cmd.run STATE page
// (with the "also an execution module" appendix), so its execution spec exists
// only for coverage/consistency, never its own rendered page.

// execPageSlug is the flat page slug (a single .mdx directly under guides/, like
// reactor.mdx / scheduling.mdx). The URL is /docs/guides/execution-modules.
const execPageSlug = "execution-modules"

// execPageURL is where a See-Also entry links an execution module (all of them
// live on the one combined page).
const execPageURL = "/docs/guides/" + execPageSlug

// execPageOrder is the order execution functions appear on the combined page,
// grouped by family (test, pkg, service, disk, grains, sys) for readability. It
// is the exec analogue of moduleToSlug: every spec-registered execution-only
// function must appear here, or generation fails (execPageOrderCovers*). cmd.run
// is excluded (dual-surface — documented on the state page).
var execPageOrder = []string{
	"test.echo", "test.version", "test.true", "test.false",
	"pkg.version", "pkg.list_pkgs",
	"service.status", "service.start", "service.stop", "service.restart",
	"disk.usage",
	"grains.item", "grains.items",
	"sys.doc", "sys.list_functions",
}

// execModuleSet is the set of execution-only module names that live on the
// combined page — derived from execPageOrder so it can never drift. renderSee
// AlsoSection resolves a See-Also target to the combined page when it names one
// of these (a state target still resolves via moduleToSlug first).
var execModuleSet = func() map[string]bool {
	m := make(map[string]bool, len(execPageOrder))
	for _, n := range execPageOrder {
		m[n] = true
	}
	return m
}()

// execSourcePath returns the implementation file for an execution function.
// Unlike state modules (one file per module), the built-ins live in two shared
// files: sys.doc / sys.list_functions in sysdoc.go, everything else in
// builtins.go. This is the honest Source line for the generated page.
func execSourcePath(module string) string {
	switch module {
	case "sys.doc", "sys.list_functions":
		return "pkg/execmod/sysdoc.go"
	default:
		return "pkg/execmod/builtins.go"
	}
}

// execPageIntro is the CommonMark lede rendered between the frontmatter and the
// first function. It must stay JSX-free (assertNoJSX).
const execPageIntro = "Execution modules are Zester's imperative remote-execution functions — the " +
	"Salt-style ad-hoc commands you run directly against peels, without writing a state file. Unlike " +
	"state modules they have no Check/Apply/Revert lifecycle: each call runs a query or action and returns " +
	"a plain string result.\n\n" +
	"Invoke one from the CLI as `zester '<target>' <module.function> [id] [key=value ...]`. The first " +
	"positional argument becomes the request ID, which is also the primary parameter's default (the package " +
	"name, service name, fact key, and so on). A name that matches an execution function and is not a " +
	"registered state module dispatches here; state-module names (like `cmd.run`) always win, so `cmd.run` " +
	"is documented on its [state module page](/docs/guides/modules/cmd#cmd-run).\n"

// orderExecInfos returns the execution-only ModuleInfos in execPageOrder,
// verifying every one is placed (a spec-registered execution-only function with
// no page slot is a generation error, mirroring moduleToSlug's coverage guard).
func orderExecInfos(execOnly []modschema.ModuleInfo) ([]modschema.ModuleInfo, error) {
	byName := make(map[string]modschema.ModuleInfo, len(execOnly))
	for _, mi := range execOnly {
		byName[mi.Module] = mi
	}
	var ordered []modschema.ModuleInfo
	placed := map[string]bool{}
	for _, name := range execPageOrder {
		mi, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("docgen: execPageOrder lists %q but it has no execution spec", name)
		}
		ordered = append(ordered, mi)
		placed[name] = true
	}
	for _, mi := range execOnly {
		if !placed[mi.Module] {
			return nil, fmt.Errorf("docgen: execution module %s has no slot in execPageOrder (add it)", mi.Module)
		}
	}
	return ordered, nil
}

// renderExecModulesPage renders the combined execution-modules reference page:
// frontmatter → one managed marker per function → intro → per-function body
// (## banner with an explicit stable anchor → Source → Description → ###
// Parameters → ### Effects → ### Examples → ### See Also; rendered pages
// carry NO Notes and NO Divergences — those Doc fields remain on the terminal
// sys.doc / `zester doc` surface). It reuses the shared section renderers at
// nestedSectionLevel (they handle the KindExec cases: no requisites
// boilerplate, an Execution-only Effects block), so each function's own
// sections render one level below its "## `module`" banner (M5: no colliding
// H2s between the banner and its own Parameters/Effects/… headings) while the
// exec page and the state pages stay one shared anatomy.
func renderExecModulesPage(infos []modschema.ModuleInfo) (string, error) {
	ordered, err := orderExecInfos(infos)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "---\ntitle: %q\ndescription: %q\n---\n\n",
		"Execution Modules",
		"Imperative remote-execution functions (Salt-style ad-hoc commands) — query and action modules with no state lifecycle.")
	for _, mi := range ordered {
		b.WriteString(managedMarker(mi.Module))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(execPageIntro)

	for _, mi := range ordered {
		fmt.Fprintf(&b, "\n---\n\n## `%s` [#%s]\n\n", mi.Module, memberAnchor(mi.Module))
		fmt.Fprintf(&b, "**Source**: `%s`\n", execSourcePath(mi.Module))
		renderDescriptionSection(&b, mi.Doc.Description)
		renderParamsSection(&b, mi, nestedSectionLevel)
		renderParamTypesSection(&b, mi, nestedSectionLevel)
		renderPageEffects(&b, mi.Doc.Effects, nestedSectionLevel)
		renderExamplesSection(&b, mi.Doc.Examples, nestedSectionLevel)
		if err := renderSeeAlsoSection(&b, mi.Module, mi.Doc.SeeAlso, nestedSectionLevel); err != nil {
			return "", err
		}
	}

	rendered := b.String()
	if err := assertNoJSX(execPageSlug, rendered); err != nil {
		return "", err
	}
	return rendered, nil
}

// writeExecModulesPage renders and writes the combined execution-modules page,
// honoring the same markerless-overwrite guard as the state pages: a
// pre-existing file without the managed marker is never blindly overwritten
// unless claim is set.
func writeExecModulesPage(path string, infos []modschema.ModuleInfo, claim bool) error {
	rendered, err := renderExecModulesPage(infos)
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil {
		if !hasManagedMarker(existing) && !claim {
			return fmt.Errorf("docgen: refusing to overwrite markerless page %s (pass --claim to adopt it)", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("docgen: read %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(rendered), 0644); err != nil {
		return fmt.Errorf("docgen: write %s: %w", path, err)
	}
	return nil
}
