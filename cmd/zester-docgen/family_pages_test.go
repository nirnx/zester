package main

import (
	"sort"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
)

// familyInfos returns the live ModuleInfos of one family, in the page order
// main.go produces (stateSpecd is sorted by module name, so members are
// alphabetical).
func familyInfos(t *testing.T, family string) []modschema.ModuleInfo {
	t.Helper()
	reg := buildStateRegistry()
	var out []modschema.ModuleInfo
	for _, name := range stateModuleNames(reg) {
		if moduleFamily(name) != family {
			continue
		}
		mi, ok := reg.Describe(name)
		if !ok {
			t.Fatalf("%s has no registered spec", name)
		}
		out = append(out, mi)
	}
	if len(out) == 0 {
		t.Fatalf("family %q has no registered members", family)
	}
	return out
}

// componentField builds a Field carrying a DeclaredBy stamp — the shape a
// family parameter component produces on a live ModuleInfo.
func componentField(name, declaredBy, usage string) modschema.Field {
	return modschema.Field{
		Name:       name,
		GoType:     "string",
		Usage:      usage,
		DeclaredBy: declaredBy,
	}
}

// syntheticInfo builds a minimal state ModuleInfo with the given module name
// and parameter surface — enough to drive the family-page renderer without a
// live registry.
func syntheticInfo(module string, params ...modschema.Field) modschema.ModuleInfo {
	return modschema.ModuleInfo{
		Module: module,
		Kind:   modschema.KindState,
		Doc:    modschema.Doc{Summary: "synthetic " + module},
		Params: params,
	}
}

// TestRenderFamilyPage_File_Anatomy renders the LIVE file family page — the
// richest family (14 members, four parameter components, member-supplied
// defaults AND requiredness, partial exposure) — and pins the whole anatomy.
func TestRenderFamilyPage_File_Anatomy(t *testing.T) {
	mis := familyInfos(t, "file")
	if len(mis) != 14 {
		t.Fatalf("file family has %d members, want 14", len(mis))
	}
	got, err := renderFamilyPage("file", mis)
	if err != nil {
		t.Fatalf("renderFamilyPage: %v", err)
	}

	mustContain := []string{
		`title: "file"`,
		`description: "The file.* family of state modules."`,
		// One managed marker per member.
		managedMarker("file.managed"),
		managedMarker("file.uncomment"),
		// Function index rows link the member anchors.
		"| [`file.managed`](#file-managed) |",
		"| [`file.touch`](#file-touch) |",
		// Family Parameters: each component param rendered once, with the
		// member-supplied dimensions deferred to sub-tables.
		"## Family Parameters",
		"| `makedirs` | `bool` | No | `false` |",
		"| `mode` | `FileMode` | No | member-specific (see below) |",
		"| `source` | `string` | member-specific (see below) | *(none)* |",
		"| `user` | `string` | No | *(none)* |",
		"| `group` | `string` | No | *(none)* |",
		// Partial exposure lists (no file.* component is on all 14 members).
		"Not every member exposes every family parameter:",
		"- `makedirs` — [`file.copy`](#file-copy), [`file.directory`](#file-directory), [`file.managed`](#file-managed), [`file.recurse`](#file-recurse), [`file.symlink`](#file-symlink), [`file.touch`](#file-touch)",
		// Member-supplied defaults (mode) and requiredness (source).
		"### `mode` defaults",
		"| [`file.directory`](#file-directory) | `0755` (lazy) |",
		"| [`file.managed`](#file-managed) | `0644` (lazy) |",
		"### `source` requiredness",
		"| [`file.copy`](#file-copy) | Yes |",
		"| [`file.managed`](#file-managed) | No |",
		// Semantic types render once for the whole page.
		"## Parameter Types",
		// Member banners carry explicit stable anchors (Fumadocs [#id]).
		"## `file.managed` [#file-managed]",
		"## `file.uncomment` [#file-uncomment]",
		// Member sections nest below their banner.
		"### Parameters",
		"### Effects",
		"#### Check",
		// Members exposing component params point at the shared section.
		"`file.managed` also accepts the family parameters `source`, `mode`, `user`, `group`, `makedirs` — see [Family Parameters](#family-parameters).",
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("family page missing %q", want)
		}
	}

	// The requisites boilerplate renders ONCE (family-level), never per member.
	if n := strings.Count(got, requisitesBoilerplate); n != 1 {
		t.Errorf("expected exactly one requisites boilerplate, got %d", n)
	}
	// A component param appears as a table ROW only in the Family Parameters
	// section — member tables exclude it (`| `makedirs` | ...` would otherwise
	// repeat per member).
	if n := strings.Count(got, "| `makedirs` |"); n != 1 {
		t.Errorf("expected `makedirs` to appear in exactly one table (Family Parameters), got %d rows", n)
	}
	// FileMode's semantic-type doc renders once, not once per member.
	if n := strings.Count(got, "## Parameter Types"); n != 1 {
		t.Errorf("expected one page-level Parameter Types section, got %d", n)
	}
	// Notes and Divergences are DROPPED from rendered pages (maintainer
	// decision): file.managed carries Notes and file family modules carry BD
	// divergences in their Docs, none of which may reach the page.
	for _, banned := range []string{"# Notes", "# Divergences"} {
		if strings.Contains(got, banned) {
			t.Errorf("family page must not render a Notes/Divergences section (found %q)", banned)
		}
	}
	// No member section heading may collide with the H2 banners.
	for line := range strings.SplitSeq(got, "\n") {
		switch line {
		case "## Parameters", "## Effects", "## Examples", "## See Also", "## Notes", "## Divergences":
			t.Errorf("member section %q must not render as an H2 (collides with the module banner)", line)
		}
	}
	// Members render in sorted-name order.
	if strings.Index(got, "## `file.absent`") > strings.Index(got, "## `file.managed`") {
		t.Error("members must render in sorted module-name order")
	}
	// Determinism: rendering twice is byte-identical.
	again, err := renderFamilyPage("file", mis)
	if err != nil {
		t.Fatalf("renderFamilyPage (second): %v", err)
	}
	if got != again {
		t.Error("renderFamilyPage is not deterministic (two renders differ)")
	}
}

// TestRenderFamilyPage_Host pins the fully-shared-component case: both host
// members embed hostFileParam, so the family table carries `config` (with its
// `path` alias and /etc/hosts default) and NO exposure list is rendered.
func TestRenderFamilyPage_Host(t *testing.T) {
	mis := familyInfos(t, "host")
	got, err := renderFamilyPage("host", mis)
	if err != nil {
		t.Fatalf("renderFamilyPage: %v", err)
	}
	for _, want := range []string{
		`title: "host"`,
		"## Family Parameters",
		"| `config` (alias `path`) | `string` | No | `/etc/hosts` |",
		"## `host.absent` [#host-absent]",
		"## `host.present` [#host-present]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("host family page missing %q", want)
		}
	}
	if strings.Contains(got, "Not every member exposes") {
		t.Error("host family page must not render an exposure list — both members share the component")
	}
	// host.present's own `ip` param stays in ITS member table only.
	if !strings.Contains(got, "| `ip` |") {
		t.Error("host.present's own ip parameter missing from its member table")
	}
}

// TestRenderFamilyPage_SingleMember pins the single-member family shape (cmd):
// same anatomy, one index row, one banner.
func TestRenderFamilyPage_SingleMember(t *testing.T) {
	mis := familyInfos(t, "cmd")
	got, err := renderFamilyPage("cmd", mis)
	if err != nil {
		t.Fatalf("renderFamilyPage: %v", err)
	}
	for _, want := range []string{
		`title: "cmd"`,
		"| [`cmd.run`](#cmd-run) |",
		"## `cmd.run` [#cmd-run]",
		"**Source**: `pkg/state/modules/cmd/cmd_run.go`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("cmd family page missing %q", want)
		}
	}
	// cmd has no parameter components: no Family Parameters section at all.
	if strings.Contains(got, "## Family Parameters") {
		t.Error("cmd family page must not render an empty Family Parameters section")
	}
}

// TestRenderFamilyPage_ParameterlessMember pins the no-own-params member shape
// (test.ping): the member still gets a Parameters section stating it takes no
// parameters, and the requisites boilerplate stays family-level.
func TestRenderFamilyPage_ParameterlessMember(t *testing.T) {
	mis := familyInfos(t, "test")
	got, err := renderFamilyPage("test", mis)
	if err != nil {
		t.Fatalf("renderFamilyPage: %v", err)
	}
	if !strings.Contains(got, "This module takes no parameters of its own.") {
		t.Error("parameterless member must state it takes no parameters")
	}
	if n := strings.Count(got, requisitesBoilerplate); n != 1 {
		t.Errorf("expected exactly one requisites boilerplate on the test family page, got %d", n)
	}
	for _, want := range []string{
		"## `test.ping` [#test-ping]",
		"## `test.configurable_test_state` [#test-configurable-test-state]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("test family page missing %q", want)
		}
	}
}

// TestCollectFamilyParams_Synthetic pins collection order (first appearance),
// exposure tracking, and the divergence guard: a component-declared key whose
// contract differs across members is a LOUD generation error.
func TestCollectFamilyParams_Synthetic(t *testing.T) {
	a := syntheticInfo("mod.a",
		modschema.Field{Name: "name", GoType: "string", Primary: true},
		componentField("shared", "mod.component", "the canonical usage"),
	)
	b := syntheticInfo("mod.b",
		modschema.Field{Name: "name", GoType: "string", Primary: true},
		componentField("shared", "mod.component", "the canonical usage"),
	)

	t.Run("agreeing-members-collect-once", func(t *testing.T) {
		fps, err := collectFamilyParams([]modschema.ModuleInfo{a, b})
		if err != nil {
			t.Fatalf("collectFamilyParams: %v", err)
		}
		if len(fps) != 1 {
			t.Fatalf("got %d family params, want 1", len(fps))
		}
		if fps[0].field.Name != "shared" || len(fps[0].members) != 2 {
			t.Errorf("family param = %q with %d members, want shared/2", fps[0].field.Name, len(fps[0].members))
		}
	})

	t.Run("usage-divergence-errors", func(t *testing.T) {
		c := syntheticInfo("mod.c", componentField("shared", "mod.component", "DIFFERENT usage"))
		if _, err := collectFamilyParams([]modschema.ModuleInfo{a, c}); err == nil {
			t.Fatal("collectFamilyParams: expected error for diverging component usage")
		}
	})

	t.Run("declarer-divergence-errors", func(t *testing.T) {
		c := syntheticInfo("mod.c", componentField("shared", "mod.otherComponent", "the canonical usage"))
		if _, err := collectFamilyParams([]modschema.ModuleInfo{a, c}); err == nil {
			t.Fatal("collectFamilyParams: expected error for a second component declaring the same key")
		}
	})

	t.Run("member-supplied-default-tolerated", func(t *testing.T) {
		d1 := componentField("mode", "mod.component", "usage")
		d1.DefaultMemberSupplied, d1.HasDefault, d1.Default = true, true, "0644"
		d2 := componentField("mode", "mod.component", "usage")
		d2.DefaultMemberSupplied, d2.HasDefault, d2.Default = true, true, "0755"
		fps, err := collectFamilyParams([]modschema.ModuleInfo{
			syntheticInfo("mod.a", d1), syntheticInfo("mod.b", d2),
		})
		if err != nil {
			t.Fatalf("collectFamilyParams: member-supplied defaults must not conflict: %v", err)
		}
		if len(fps) != 1 || len(fps[0].members) != 2 {
			t.Fatalf("unexpected aggregation: %+v", fps)
		}
	})

	t.Run("fixed-default-divergence-errors", func(t *testing.T) {
		d1 := componentField("flag", "mod.component", "usage")
		d1.HasDefault, d1.Default = true, "true"
		d2 := componentField("flag", "mod.component", "usage")
		d2.HasDefault, d2.Default = true, "false"
		if _, err := collectFamilyParams([]modschema.ModuleInfo{
			syntheticInfo("mod.a", d1), syntheticInfo("mod.b", d2),
		}); err == nil {
			t.Fatal("collectFamilyParams: expected error for a fixed default diverging without memberdefault")
		}
	})
}

// TestGroupByFamily pins main.go's family grouping: first-seen order, family
// membership, and the unmapped-module guard.
func TestGroupByFamily(t *testing.T) {
	info := func(module string) modschema.ModuleInfo {
		return modschema.ModuleInfo{Module: module, Kind: modschema.KindState}
	}

	t.Run("groups-and-preserves-order", func(t *testing.T) {
		in := []modschema.ModuleInfo{
			info("file.absent"),
			info("file.managed"),
			info("host.absent"),
			info("host.present"),
		}
		order, byFamily, err := groupByFamily(in)
		if err != nil {
			t.Fatalf("groupByFamily: %v", err)
		}
		if strings.Join(order, ",") != "file,host" {
			t.Errorf("family order = %v, want [file host]", order)
		}
		if got := len(byFamily["file"]); got != 2 {
			t.Errorf("file family has %d members, want 2", got)
		}
		if byFamily["file"][0].Module != "file.absent" || byFamily["file"][1].Module != "file.managed" {
			t.Errorf("file member order wrong: %v", byFamily["file"])
		}
	})

	t.Run("unmapped-module-errors", func(t *testing.T) {
		if _, _, err := groupByFamily([]modschema.ModuleInfo{info("bogus.module")}); err == nil {
			t.Fatal("groupByFamily: expected error for a module missing from stateModules")
		}
	})
}

// TestGroupByFamily_LiveRegistryCoversNav ties the live registry to the nav:
// grouping every spec'd module yields exactly the non-extra nav slugs.
func TestGroupByFamily_LiveRegistryCoversNav(t *testing.T) {
	reg := buildStateRegistry()
	var mis []modschema.ModuleInfo
	for _, name := range stateModuleNames(reg) {
		mi, ok := reg.Describe(name)
		if !ok {
			t.Fatalf("%s has no registered spec", name)
		}
		mis = append(mis, mi)
	}
	order, _, err := groupByFamily(mis)
	if err != nil {
		t.Fatalf("groupByFamily: %v", err)
	}
	var gotSlugs []string
	for _, family := range order {
		gotSlugs = append(gotSlugs, strings.ReplaceAll(family, "_", "-"))
	}
	sort.Strings(gotSlugs)
	var wantSlugs []string
	for _, slug := range allPageSlugs() {
		if !extraPages[slug] {
			wantSlugs = append(wantSlugs, slug)
		}
	}
	sort.Strings(wantSlugs)
	if strings.Join(gotSlugs, ",") != strings.Join(wantSlugs, ",") {
		t.Errorf("live families %v != non-extra nav slugs %v", gotSlugs, wantSlugs)
	}
}
