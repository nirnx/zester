package main

import "testing"

func TestModuleToSlug_CoversEveryRegisteredModule(t *testing.T) {
	reg := buildStateRegistry()
	for _, name := range stateModuleNames(reg) {
		if _, ok := moduleToSlug[name]; !ok {
			t.Errorf("module %q has no page-group slug in moduleToSlug", name)
		}
	}
}

func TestModuleToSlug_NoStaleEntries(t *testing.T) {
	reg := buildStateRegistry()
	known := map[string]bool{}
	for _, name := range stateModuleNames(reg) {
		known[name] = true
	}
	for name := range moduleToSlug {
		if !known[name] {
			t.Errorf("moduleToSlug has stale entry %q: not a registered module", name)
		}
	}
}

func TestModuleToSlug_EverySlugIsInFamilies(t *testing.T) {
	slugs := map[string]bool{}
	for _, s := range allPageSlugs() {
		slugs[s] = true
	}
	for module, slug := range moduleToSlug {
		if !slugs[slug] {
			t.Errorf("module %q maps to slug %q, which appears in no family", module, slug)
		}
	}
}

func TestAllPageSlugs_EveryNonExtraSlugHasAModule(t *testing.T) {
	haveModule := map[string]bool{}
	for _, slug := range moduleToSlug {
		haveModule[slug] = true
	}
	for _, slug := range allPageSlugs() {
		if extraPages[slug] {
			continue
		}
		if !haveModule[slug] {
			t.Errorf("page slug %q has no module mapped to it and is not an extraPages entry", slug)
		}
	}
}
