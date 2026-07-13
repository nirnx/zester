package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
)

// commentGroupInfos returns the live file.comment + file.uncomment ModuleInfos —
// the first fully-migrated N:1 PageGroup (ONE FileComment proto backs both
// registered names, so they share an identical parameter surface). They are the
// realest possible fixture for the multi-member page-group renderer.
func commentGroupInfos(t *testing.T) []modschema.ModuleInfo {
	t.Helper()
	reg := buildStateRegistry()
	var out []modschema.ModuleInfo
	for _, name := range []string{"file.comment", "file.uncomment"} {
		mi, ok := reg.Describe(name)
		if !ok {
			t.Fatalf("%s has no registered spec", name)
		}
		out = append(out, mi)
	}
	return out
}

// stateField is a compact constructor for a Parameters-table Field in tests.
func stateField(name, goType, semType, def string, required, primary bool) modschema.Field {
	return modschema.Field{
		Name:         name,
		GoType:       goType,
		SemanticType: semType,
		Default:      def,
		Required:     required,
		Primary:      primary,
	}
}

// fieldWithAliases builds a plain-string Field carrying the given aliases — used
// to exercise assertSharedParams's Aliases comparison.
func fieldWithAliases(name string, primary bool, aliases ...string) modschema.Field {
	return modschema.Field{
		Name:    name,
		GoType:  "string",
		Primary: primary,
		Aliases: aliases,
	}
}

// syntheticInfo builds a minimal state ModuleInfo with the given module name and
// parameter surface — enough to drive the page-group renderer without a live
// registry, used for the shared-params mismatch cases.
func syntheticInfo(module string, params ...modschema.Field) modschema.ModuleInfo {
	return modschema.ModuleInfo{
		Module: module,
		Kind:   modschema.KindState,
		Doc:    modschema.Doc{Summary: "synthetic " + module},
		Params: params,
	}
}

func TestJoinWithAnd(t *testing.T) {
	cases := []struct {
		name  string
		items []string
		want  string
	}{
		{"zero", nil, ""},
		{"one", []string{"a"}, "a"},
		{"two", []string{"a", "b"}, "a and b"},
		{"three", []string{"a", "b", "c"}, "a, b, and c"},
		{"four", []string{"a", "b", "c", "d"}, "a, b, c, and d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinWithAnd(tc.items); got != tc.want {
				t.Errorf("joinWithAnd(%v) = %q, want %q", tc.items, got, tc.want)
			}
		})
	}
}

func TestAssertSharedParams(t *testing.T) {
	base := func() modschema.ModuleInfo {
		return syntheticInfo("mod.a",
			stateField("name", "string", "", "", false, true),
			stateField("char", "string", "", "#", false, false),
		)
	}
	// A second member sharing the identical surface but a different module name.
	twin := func() modschema.ModuleInfo {
		mi := base()
		mi.Module = "mod.b"
		return mi
	}

	cases := []struct {
		name    string
		mis     []modschema.ModuleInfo
		wantErr bool
	}{
		{
			name: "single-member-trivially-ok",
			mis:  []modschema.ModuleInfo{base()},
		},
		{
			name: "identical-surface-ok",
			mis:  []modschema.ModuleInfo{base(), twin()},
		},
		{
			name: "different-param-count",
			mis: []modschema.ModuleInfo{base(), syntheticInfo("mod.b",
				stateField("name", "string", "", "", false, true),
			)},
			wantErr: true,
		},
		{
			name: "different-param-name",
			mis: []modschema.ModuleInfo{base(), syntheticInfo("mod.b",
				stateField("name", "string", "", "", false, true),
				stateField("comment", "string", "", "#", false, false),
			)},
			wantErr: true,
		},
		{
			name: "different-go-type",
			mis: []modschema.ModuleInfo{base(), syntheticInfo("mod.b",
				stateField("name", "string", "", "", false, true),
				stateField("char", "int", "", "#", false, false),
			)},
			wantErr: true,
		},
		{
			name: "different-required",
			mis: []modschema.ModuleInfo{base(), syntheticInfo("mod.b",
				stateField("name", "string", "", "", false, true),
				stateField("char", "string", "", "#", true, false),
			)},
			wantErr: true,
		},
		{
			name: "different-default",
			mis: []modschema.ModuleInfo{base(), syntheticInfo("mod.b",
				stateField("name", "string", "", "", false, true),
				stateField("char", "string", "", ";", false, false),
			)},
			wantErr: true,
		},
		{
			name: "different-semantic-type",
			mis: []modschema.ModuleInfo{base(), syntheticInfo("mod.b",
				stateField("name", "string", "", "", false, true),
				stateField("char", "paramtypes.FileMode", "FileMode", "#", false, false),
			)},
			wantErr: true,
		},
		{
			name: "different-primary",
			mis: []modschema.ModuleInfo{base(), syntheticInfo("mod.b",
				stateField("name", "string", "", "", false, false),
				stateField("char", "string", "", "#", false, false),
			)},
			wantErr: true,
		},
		{
			// assertSharedParams compares Aliases too (its doc comment lists
			// them): a member whose primary carries a divergent alias set is a
			// shared-surface mismatch.
			name: "different-aliases",
			mis: []modschema.ModuleInfo{
				syntheticInfo("mod.a",
					fieldWithAliases("name", true, "path"),
					stateField("char", "string", "", "#", false, false),
				),
				syntheticInfo("mod.b",
					fieldWithAliases("name", true, "target"),
					stateField("char", "string", "", "#", false, false),
				),
			},
			wantErr: true,
		},
		{
			// Identical alias sets on both members share the surface.
			name: "identical-aliases-ok",
			mis: []modschema.ModuleInfo{
				syntheticInfo("mod.a",
					fieldWithAliases("name", true, "path"),
					stateField("char", "string", "", "#", false, false),
				),
				syntheticInfo("mod.b",
					fieldWithAliases("name", true, "path"),
					stateField("char", "string", "", "#", false, false),
				),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := assertSharedParams(tc.mis)
			if tc.wantErr && err == nil {
				t.Fatalf("assertSharedParams: expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("assertSharedParams: unexpected error: %v", err)
			}
		})
	}
}

// TestRenderModulePageGroup_SingleMemberEqualsRenderModulePage pins the documented
// contract that a one-member group renders byte-identically to renderModulePage.
func TestRenderModulePageGroup_SingleMemberEqualsRenderModulePage(t *testing.T) {
	mi := samplePkgRemovedInfo(t)
	single, err := renderModulePage(mi)
	if err != nil {
		t.Fatalf("renderModulePage: %v", err)
	}
	group, err := renderModulePageGroup([]modschema.ModuleInfo{mi}, false)
	if err != nil {
		t.Fatalf("renderModulePageGroup: %v", err)
	}
	if single != group {
		t.Errorf("single-member group differs from renderModulePage:\n--- renderModulePage ---\n%s\n--- group ---\n%s", single, group)
	}
}

// TestRenderModulePageGroup_MultiMember pins the N:1 combined page anatomy: a
// joined title, one managed marker per module, the shared-parameters sentence
// (joinWithAnd), exactly ONE Parameters section, and a per-module banner with
// that module's own Effects.
func TestRenderModulePageGroup_MultiMember(t *testing.T) {
	mis := commentGroupInfos(t)
	got, err := renderModulePageGroup(mis, false)
	if err != nil {
		t.Fatalf("renderModulePageGroup: %v", err)
	}

	mustContain := []string{
		`title: "file.comment / file.uncomment"`,
		managedMarker("file.comment"),
		managedMarker("file.uncomment"),
		"`file.comment` and `file.uncomment` share the same parameters and implementation.",
		"## `file.comment`",
		"## `file.uncomment`",
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("multi-member page missing %q\n--- page ---\n%s", want, got)
		}
	}

	// The shared proto means exactly ONE Parameters/Parameter Types section, and
	// one requisites boilerplate paragraph — not one per member.
	if n := strings.Count(got, "## Parameters"); n != 1 {
		t.Errorf("expected exactly one Parameters section, got %d", n)
	}
	if n := strings.Count(got, requisitesBoilerplate); n != 1 {
		t.Errorf("expected exactly one requisites boilerplate, got %d", n)
	}
	// Each member contributes its own Effects section under its banner.
	if n := strings.Count(got, "## Effects"); n != 2 {
		t.Errorf("expected one Effects section per member (2), got %d", n)
	}
	// The banners must appear AFTER the shared Parameters section (per-module
	// behavior sections follow the shared header).
	if strings.Index(got, "## Parameters") > strings.Index(got, "## `file.comment`") {
		t.Error("shared Parameters section must precede the per-module banners")
	}
}

// TestRenderModulePageGroup_DistinctParamsRendersPerMember pins the N:1 page-group
// behavior when members do NOT share a parameter surface (distinct protos, like
// host.present/host.absent or ssh_auth.present/ssh_auth.absent) AND the caller
// explicitly opts in via distinctParams=true (isDistinctParamSlug): the renderer
// emits each member's FULL body — its own Source line and its own Parameters
// section — under a per-module banner, so no member is documented against
// another's parameters.
func TestRenderModulePageGroup_DistinctParamsRendersPerMember(t *testing.T) {
	mis := []modschema.ModuleInfo{
		syntheticInfo("mod.a",
			stateField("name", "string", "", "", false, true),
			stateField("char", "string", "", "#", false, false),
		),
		syntheticInfo("mod.b",
			stateField("name", "string", "", "", false, true),
		),
	}
	got, err := renderModulePageGroup(mis, true)
	if err != nil {
		t.Fatalf("renderModulePageGroup: %v", err)
	}

	mustContain := []string{
		`title: "mod.a / mod.b"`,
		managedMarker("mod.a"),
		managedMarker("mod.b"),
		"`mod.a` and `mod.b` are documented together on this page; each has its own parameters.",
		"## `mod.a`",
		"## `mod.b`",
		"**Source**: `pkg/state/modules/mod_a.go`",
		"**Source**: `pkg/state/modules/mod_b.go`",
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("distinct-param page missing %q\n--- page ---\n%s", want, got)
		}
	}

	// Each member documents its OWN Parameters (two sections, not one shared),
	// so mod.a's extra `char` field only appears once and mod.b never claims it.
	if n := strings.Count(got, "## Parameters"); n != 2 {
		t.Errorf("expected one Parameters section per member (2), got %d", n)
	}
	if n := strings.Count(got, "`char`"); n != 1 {
		t.Errorf("expected mod.a's `char` param to appear exactly once, got %d", n)
	}
	// The shared-params sentence must NOT appear — the members do not share params.
	if strings.Contains(got, "share the same parameters and implementation") {
		t.Error("distinct-param page must not claim members share parameters")
	}
}

// TestRenderModulePageGroup_MismatchWithoutFlagFails pins the K3 tightening: a
// multi-member group whose members do NOT share a parameter surface but was NOT
// declared distinctParams (isDistinctParamSlug=false) is a LOUD generation error,
// not a silent fallback to per-member rendering. This is what protects a genuinely
// shared-proto page group (file-comment) from silently degrading if a future edit
// makes its members' surfaces diverge.
func TestRenderModulePageGroup_MismatchWithoutFlagFails(t *testing.T) {
	mis := []modschema.ModuleInfo{
		syntheticInfo("mod.a",
			stateField("name", "string", "", "", false, true),
			stateField("char", "string", "", "#", false, false),
		),
		syntheticInfo("mod.b",
			stateField("name", "string", "", "", false, true),
		),
	}
	if _, err := renderModulePageGroup(mis, false); err == nil {
		t.Fatal("renderModulePageGroup: expected an error for a param-surface mismatch without the distinctParams opt-in")
	}
	// With the opt-in it renders fine (the positive path is pinned separately).
	if _, err := renderModulePageGroup(mis, true); err != nil {
		t.Fatalf("renderModulePageGroup with distinctParams=true: %v", err)
	}
}

// TestDistinctParamSlugsAreLiveGroups pins that every declared distinct-param slug
// is a real multi-member page group in moduleToSlug — a stale entry (e.g. after a
// group is merged or a member renamed) is caught here rather than silently
// mis-rendering. It also asserts the three known distinct groups are declared.
func TestDistinctParamSlugsAreLiveGroups(t *testing.T) {
	members := map[string]int{}
	for _, slug := range moduleToSlug {
		members[slug]++
	}
	for slug := range distinctParamSlugs {
		if members[slug] < 2 {
			t.Errorf("distinctParamSlugs[%q] is not a multi-member page group (has %d members)", slug, members[slug])
		}
	}
	for _, slug := range []string{"host", "ssh-auth", "test-helpers"} {
		if !isDistinctParamSlug(slug) {
			t.Errorf("expected %q to be a declared distinct-param slug", slug)
		}
	}
}

// TestWriteModulePageGroup_MultiMember writes the live file.comment/file.uncomment
// group to a fresh file (no --claim needed) and asserts BOTH members' markers land
// in the single combined page.
func TestWriteModulePageGroup_MultiMember(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file-comment.mdx")
	mis := commentGroupInfos(t)

	if err := writeModulePageGroup(path, mis, false, false); err != nil {
		t.Fatalf("writeModulePageGroup on a nonexistent file: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"file.comment", "file.uncomment"} {
		if !strings.Contains(string(got), managedMarker(name)) {
			t.Errorf("combined page missing managed marker for %s", name)
		}
	}
}

// TestWriteModulePageGroup_RefusesMarkerlessOverwrite pins that the markerless-
// overwrite guard applies to multi-member groups too: an existing hand-written
// page is not clobbered without --claim, and IS adopted with it.
func TestWriteModulePageGroup_RefusesMarkerlessOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file-comment.mdx")
	original := "---\ntitle: hand-written\n---\n\nDo not touch me.\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	mis := commentGroupInfos(t)

	if err := writeModulePageGroup(path, mis, false, false); err == nil {
		t.Fatal("writeModulePageGroup: expected refusal for markerless existing page")
	}
	if got, _ := os.ReadFile(path); string(got) != original {
		t.Errorf("markerless page modified despite refusal:\n%s", got)
	}

	if err := writeModulePageGroup(path, mis, true, false); err != nil {
		t.Fatalf("writeModulePageGroup with claim=true: %v", err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "Do not touch me.") {
		t.Error("claimed group page still contains the old hand-written body")
	}
	if !hasManagedMarker(got) {
		t.Error("claimed group page does not carry the managed marker")
	}
}

// TestGroupBySlug pins main.go's slug-grouping: N:1 members collapse to one slug,
// first-seen (registration) order is preserved, and an unmapped module is a
// generation error.
func TestGroupBySlug(t *testing.T) {
	info := func(module string) modschema.ModuleInfo {
		return modschema.ModuleInfo{Module: module, Kind: modschema.KindState}
	}

	t.Run("n-to-1-collapses-and-preserves-order", func(t *testing.T) {
		in := []modschema.ModuleInfo{
			info("file.line"),
			info("file.comment"),
			info("file.uncomment"), // shares the file-comment slug
			info("file.replace"),
		}
		order, bySlug, err := groupBySlug(in)
		if err != nil {
			t.Fatalf("groupBySlug: %v", err)
		}
		wantOrder := []string{"file-line", "file-comment", "file-replace"}
		if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
			t.Errorf("slug order = %v, want %v", order, wantOrder)
		}
		if got := len(bySlug["file-comment"]); got != 2 {
			t.Errorf("file-comment group has %d members, want 2", got)
		}
		if bySlug["file-comment"][0].Module != "file.comment" || bySlug["file-comment"][1].Module != "file.uncomment" {
			t.Errorf("file-comment member order wrong: %v", bySlug["file-comment"])
		}
		if got := len(bySlug["file-line"]); got != 1 {
			t.Errorf("file-line group has %d members, want 1", got)
		}
	})

	t.Run("single-module", func(t *testing.T) {
		order, bySlug, err := groupBySlug([]modschema.ModuleInfo{info("pkg.removed")})
		if err != nil {
			t.Fatalf("groupBySlug: %v", err)
		}
		if len(order) != 1 || order[0] != "pkg-removed" {
			t.Errorf("order = %v, want [pkg-removed]", order)
		}
		if len(bySlug) != 1 {
			t.Errorf("bySlug has %d slugs, want 1", len(bySlug))
		}
	})

	t.Run("unmapped-module-errors", func(t *testing.T) {
		if _, _, err := groupBySlug([]modschema.ModuleInfo{info("bogus.module")}); err == nil {
			t.Fatal("groupBySlug: expected error for a module with no page-group slug")
		}
	})
}
