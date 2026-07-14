package main

import "testing"

func TestStateModules_CoversEveryRegisteredModule(t *testing.T) {
	reg := buildStateRegistry()
	for _, name := range stateModuleNames(reg) {
		if !stateModules[name] {
			t.Errorf("module %q is missing from the stateModules membership table", name)
		}
	}
}

func TestStateModules_NoStaleEntries(t *testing.T) {
	reg := buildStateRegistry()
	known := map[string]bool{}
	for _, name := range stateModuleNames(reg) {
		known[name] = true
	}
	for name := range stateModules {
		if !known[name] {
			t.Errorf("stateModules has stale entry %q: not a registered module", name)
		}
	}
}

func TestFamilySlug_EverySlugIsInFamilies(t *testing.T) {
	slugs := map[string]bool{}
	for _, s := range allPageSlugs() {
		slugs[s] = true
	}
	for module := range stateModules {
		if slug := familySlug(module); !slugs[slug] {
			t.Errorf("module %q maps to family slug %q, which appears in no nav section", module, slug)
		}
	}
}

func TestAllPageSlugs_EveryNonExtraSlugHasAModule(t *testing.T) {
	haveModule := map[string]bool{}
	for module := range stateModules {
		haveModule[familySlug(module)] = true
	}
	for _, slug := range allPageSlugs() {
		if extraPages[slug] {
			continue
		}
		if !haveModule[slug] {
			t.Errorf("page slug %q has no module family mapped to it and is not an extraPages entry", slug)
		}
	}
}

// TestFamilySlugDerivation pins the name→slug/anchor/URL derivations,
// including the '_'→'-' dash-casing that keeps slugs and anchors URL-clean.
func TestFamilySlugDerivation(t *testing.T) {
	cases := []struct {
		module string
		family string
		slug   string
		anchor string
		url    string
	}{
		{"file.managed", "file", "file", "file-managed", "/docs/guides/modules/file#file-managed"},
		{"ssh_auth.present", "ssh_auth", "ssh-auth", "ssh-auth-present", "/docs/guides/modules/ssh-auth#ssh-auth-present"},
		{"test.fail_without_changes", "test", "test", "test-fail-without-changes", "/docs/guides/modules/test#test-fail-without-changes"},
		{"cmd.run", "cmd", "cmd", "cmd-run", "/docs/guides/modules/cmd#cmd-run"},
	}
	for _, tc := range cases {
		if got := moduleFamily(tc.module); got != tc.family {
			t.Errorf("moduleFamily(%q) = %q, want %q", tc.module, got, tc.family)
		}
		if got := familySlug(tc.module); got != tc.slug {
			t.Errorf("familySlug(%q) = %q, want %q", tc.module, got, tc.slug)
		}
		if got := memberAnchor(tc.module); got != tc.anchor {
			t.Errorf("memberAnchor(%q) = %q, want %q", tc.module, got, tc.anchor)
		}
		if got := modulePageURL(tc.module); got != tc.url {
			t.Errorf("modulePageURL(%q) = %q, want %q", tc.module, got, tc.url)
		}
	}
}

// TestFamilies_OnePageSlugPerFamily pins that the nav lists each family slug
// exactly once and that every non-extra slug is a real family (no page-group
// leftovers like "file-managed" or "test-helpers").
func TestFamilies_OnePageSlugPerFamily(t *testing.T) {
	seen := map[string]int{}
	for _, fam := range families {
		for _, p := range fam.Pages {
			seen[p]++
		}
	}
	for slug, n := range seen {
		if n != 1 {
			t.Errorf("nav slug %q listed %d times, want exactly once", slug, n)
		}
	}
}
