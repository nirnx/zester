package main

// This file is the FULLY enumerated MetaFamily table (keystone spec §8): the
// nav structure for website/content/docs/guides/modules/meta.json, and the
// module→page-slug mapping (PageGroups) used both to emit meta.json wholesale
// and to validate every migrated module's page/see-also targets resolve.
//
// The table is a literal reproduction of the live meta.json (byte-exact
// separators, "index" never listed) — NOT derived from the module registry —
// because most of it enumerates modules that have no Spec yet and whose pages
// remain hand-written. Migrating a module never changes its family placement;
// only whether docgen (vs. a human) owns that page's content.

// metaFamily is one separator-delimited section of the modules nav.
type metaFamily struct {
	// Label is the separator text; rendered as the literal entry
	// "---<Label>---" (Fumadocs separator convention).
	Label string
	// Pages are the page slugs in this section, in nav order.
	Pages []string
}

// families is the complete, ordered modules nav — every family, every page
// slug, byte-exact to the live meta.json.
var families = []metaFamily{
	{Label: "File", Pages: []string{
		"file-managed", "file-directory", "file-absent", "file-append",
		"file-symlink", "file-recurse", "file-blockreplace", "file-comment",
		"file-copy", "file-keyvalue", "file-line", "file-replace", "file-touch",
	}},
	{Label: "Package", Pages: []string{
		"pkg-installed", "pkg-latest", "pkg-purged", "pkg-removed", "pkgrepo-managed",
	}},
	{Label: "Service", Pages: []string{
		"service-running", "service-enabled", "service-dead",
	}},
	{Label: "Command", Pages: []string{
		"cmd-run",
	}},
	{Label: "User & Group", Pages: []string{
		"user-present", "user-absent", "group-present", "group-absent",
	}},
	{Label: "System", Pages: []string{
		"cron-present", "cron-absent", "sysctl-present", "mount-mounted", "host", "ssh-auth",
	}},
	{Label: "Tooling", Pages: []string{
		"archive-extracted", "git-cloned", "git-latest", "pip-installed",
		"timezone-system", "locale-present",
	}},
	{Label: "Other", Pages: []string{
		"module-run", "test-ping", "test-helpers", "query", "starlark",
	}},
}

// moduleToSlug is the N:1 PageGroups mapping (§8): every one of the 47
// built-in state module names to the page slug that documents it. Several
// slugs are shared by more than one module name (file-comment, host,
// ssh-auth, test-helpers) — those groupings are the spec's named exceptions;
// every other module maps to its own dash-cased slug.
var moduleToSlug = map[string]string{
	"file.managed":      "file-managed",
	"file.directory":    "file-directory",
	"file.absent":       "file-absent",
	"file.append":       "file-append",
	"file.symlink":      "file-symlink",
	"file.recurse":      "file-recurse",
	"file.blockreplace": "file-blockreplace",
	"file.comment":      "file-comment",
	"file.uncomment":    "file-comment", // N:1 PageGroup
	"file.copy":         "file-copy",
	"file.keyvalue":     "file-keyvalue",
	"file.line":         "file-line",
	"file.replace":      "file-replace",
	"file.touch":        "file-touch",

	"pkg.installed":   "pkg-installed",
	"pkg.latest":      "pkg-latest",
	"pkg.purged":      "pkg-purged",
	"pkg.removed":     "pkg-removed",
	"pkgrepo.managed": "pkgrepo-managed",

	"service.running": "service-running",
	"service.enabled": "service-enabled",
	"service.dead":    "service-dead",

	"cmd.run": "cmd-run", // cmd-run dual-surface: state page + execmod appendix

	"user.present":  "user-present",
	"user.absent":   "user-absent",
	"group.present": "group-present",
	"group.absent":  "group-absent",

	"cron.present":     "cron-present",
	"cron.absent":      "cron-absent",
	"sysctl.present":   "sysctl-present",
	"mount.mounted":    "mount-mounted",
	"host.present":     "host",     // N:1 PageGroup
	"host.absent":      "host",     // N:1 PageGroup
	"ssh_auth.present": "ssh-auth", // N:1 PageGroup
	"ssh_auth.absent":  "ssh-auth", // N:1 PageGroup

	"archive.extracted": "archive-extracted",
	"git.cloned":        "git-cloned",
	"git.latest":        "git-latest",
	"pip.installed":     "pip-installed",
	"timezone.system":   "timezone-system",
	"locale.present":    "locale-present",

	"module.run":                   "module-run",
	"test.ping":                    "test-ping",
	"test.nop":                     "test-helpers", // N:1 PageGroup
	"test.fail_without_changes":    "test-helpers", // N:1 PageGroup
	"test.succeed_with_changes":    "test-helpers", // N:1 PageGroup
	"test.configurable_test_state": "test-helpers", // N:1 PageGroup
}

// extraPages are nav entries not derived from any state module registration:
// "query" documents the peel dispatch specials (facts.*/settings.*/pillar.*/
// grains.*/sys.list_functions — generated from DispatchSpecials in Phase 3,
// §7) and "starlark" is a general authoring guide. Both are hand-maintained
// until their own tranche.
var extraPages = map[string]bool{
	"query":    true,
	"starlark": true,
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
