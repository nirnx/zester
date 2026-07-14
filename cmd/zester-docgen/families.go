package main

import "strings"

// This file is the FULLY enumerated MetaFamily table: the nav structure for
// website/content/docs/guides/modules/meta.json, and the module-name →
// family-page mapping used both to emit meta.json wholesale and to validate
// every module's page/see-also targets resolve.
//
// The generated module reference is a Salt-style FAMILY TREE (maintainer
// decision, 2026-07): ONE page per module family (family = the module name's
// first dotted segment — modules → pkg → every pkg.* function on one page),
// with a stable explicit anchor per member. The former per-module and N:1
// page-group slugs (file-managed, test-helpers, host, …) all fold into their
// family page.

// metaFamily is one separator-delimited section of the modules nav.
type metaFamily struct {
	// Label is the separator text; rendered as the literal entry
	// "---<Label>---" (Fumadocs separator convention).
	Label string
	// Pages are the page slugs in this section, in nav order.
	Pages []string
}

// families is the complete, ordered modules nav — every section, every family
// page slug. The section labels and their order are unchanged from the
// per-module era; each section now lists FAMILY slugs.
var families = []metaFamily{
	{Label: "File", Pages: []string{"file"}},
	{Label: "Package", Pages: []string{"pkg", "pkgrepo"}},
	{Label: "Service", Pages: []string{"service"}},
	{Label: "Command", Pages: []string{"cmd"}},
	{Label: "User & Group", Pages: []string{"user", "group"}},
	{Label: "System", Pages: []string{"cron", "sysctl", "mount", "host", "ssh-auth"}},
	{Label: "Tooling", Pages: []string{"archive", "git", "pip", "timezone", "locale"}},
	{Label: "Other", Pages: []string{"module", "test", "query", "starlark", "developing"}},
}

// stateModules is the set of all 47 built-in state module names — the page
// and see-also membership table (registration truth, pinned against the live
// registry by TestStateModules_*). A see-also target resolves to a family
// page ONLY when it names a registered state module; family slugs and member
// anchors are DERIVED from the name (familySlug/memberAnchor), never listed
// here.
var stateModules = map[string]bool{
	"file.managed":      true,
	"file.directory":    true,
	"file.absent":       true,
	"file.append":       true,
	"file.symlink":      true,
	"file.recurse":      true,
	"file.blockreplace": true,
	"file.comment":      true,
	"file.uncomment":    true,
	"file.copy":         true,
	"file.keyvalue":     true,
	"file.line":         true,
	"file.replace":      true,
	"file.touch":        true,

	"pkg.installed":   true,
	"pkg.latest":      true,
	"pkg.purged":      true,
	"pkg.removed":     true,
	"pkgrepo.managed": true,

	"service.running": true,
	"service.enabled": true,
	"service.dead":    true,

	"cmd.run": true, // dual-surface: family page + execmod appendix in its description

	"user.present":  true,
	"user.absent":   true,
	"group.present": true,
	"group.absent":  true,

	"cron.present":     true,
	"cron.absent":      true,
	"sysctl.present":   true,
	"mount.mounted":    true,
	"host.present":     true,
	"host.absent":      true,
	"ssh_auth.present": true,
	"ssh_auth.absent":  true,

	"archive.extracted": true,
	"git.cloned":        true,
	"git.latest":        true,
	"pip.installed":     true,
	"timezone.system":   true,
	"locale.present":    true,

	"module.run":                   true,
	"test.ping":                    true,
	"test.nop":                     true,
	"test.fail_without_changes":    true,
	"test.succeed_with_changes":    true,
	"test.configurable_test_state": true,
}

// moduleFamily returns a module name's family — its first dotted segment
// ("ssh_auth.present" → "ssh_auth").
func moduleFamily(module string) string {
	family, _, _ := strings.Cut(module, ".")
	return family
}

// familySlug returns the family page slug for a state module name: the family
// with '_' dash-cased ("ssh_auth.present" → "ssh-auth").
func familySlug(module string) string {
	return strings.ReplaceAll(moduleFamily(module), "_", "-")
}

// memberAnchor returns a module's stable heading anchor on its family page:
// the full dotted name dash-cased ("test.fail_without_changes" →
// "test-fail-without-changes"). Also used for the execution-modules page's
// per-function anchors.
func memberAnchor(module string) string {
	return strings.NewReplacer(".", "-", "_", "-").Replace(module)
}

// modulePageURL returns the absolute docs URL for a state module: its family
// page plus its member anchor.
func modulePageURL(module string) string {
	return "/docs/guides/modules/" + familySlug(module) + "#" + memberAnchor(module)
}

// extraPages are nav entries not derived from any state module registration:
// "query" documents the peel dispatch specials (facts.*/settings.*/pillar.*/
// grains.*/sys.list_functions), "starlark" is the operator authoring guide,
// and "developing" is the developer howto for adding a built-in module with a
// schema. All are hand-maintained.
var extraPages = map[string]bool{
	"query":      true,
	"starlark":   true,
	"developing": true,
}

// allPageSlugs returns every page slug enumerated by families, in nav order,
// without duplicates.
func allPageSlugs() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, fam := range families {
		for _, p := range fam.Pages {
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}
