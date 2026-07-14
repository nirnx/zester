package modules

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// PkgInstalled implements the pkg.installed state.
// It ensures that a system package is installed using the injected PackageExec provider.
//
// PkgInstalled is also its own schema proto: the tagged exported Package,
// Version, and Refresh fields ARE the module's parameter declaration (one
// schema declaration per module) — all three are primitives, no semantic
// types are needed here. The unexported runtime field (pkg) is untagged, so
// the schema compiler skips it.
type PkgInstalled struct {
	id   string
	reqs state.Requisites

	// Package is the name of the package to install.
	Package string `zester:"name,primary" usage:"package to install (defaults to the state ID)"`

	// Version is an optional version constraint. When declared, Check
	// compares it against the provider's InstalledVersion — any other
	// installed version needs a change (upgrades AND downgrades converge).
	Version string `zester:"version" usage:"exact version pin; any other installed version converges via install or downgrade (format depends on the detected package manager)"`

	// Refresh: pkg.* family component (member-supplied default false).
	pkgRefreshParam

	// pkg is the injected package execution provider.
	pkg exec.PackageExec
}

// pkgInstalledSpec is the compiled schema + documentation for pkg.installed. It
// is compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is verified against the live
// Check/Apply/Revert behavior and the apt/dnf/yum/brew provider implementations
// (pkg/exec/pkg_*.go).
var pkgInstalledSpec = mustSpec("pkg.installed", modschema.KindState, PkgInstalled{}, pkgInstalledDoc,
	modschema.WithDefault("refresh", "false"))

var pkgInstalledDoc = modschema.Doc{
	Summary: "Ensure a system package is installed, optionally pinned to an exact version.",
	Description: "`pkg.installed` ensures the named package is present on the target through the " +
		"peel's auto-detected package manager (apt, dnf, yum, or brew). The package name defaults " +
		"to the state ID, so a bare `pkg.installed` under an `nginx:` key installs `nginx`. " +
		"Declaring `version` makes exact version equality part of the desired state: Check compares " +
		"the provider's installed version against the pin and reports drift for ANY other installed " +
		"version, so both upgrades and downgrades converge; an undeclared `version` is satisfied by " +
		"whatever version happens to be installed.\n\n" +
		"The version pin's on-the-wire format depends on the detected package manager:\n\n" +
		"| Manager | Format | Example |\n" +
		"|---|---|---|\n" +
		"| apt | `<name>=<version>` | `nginx=1.24.0-1ubuntu1` |\n" +
		"| yum/dnf | `<name>-<version>` | `nginx-1.24.0` |\n" +
		"| brew | `<name>@<version>` | `nginx@1.24` |",
	Effects: modschema.Effects{
		Check: "Queries the package provider whether the package is installed at all; an absent " +
			"package always needs a change. When `version` is declared and the package is present, " +
			"Check also compares the provider's InstalledVersion against the pin — any other " +
			"installed version, newer or older, reports a got/want diff and needs a change; without " +
			"a declared `version`, presence alone satisfies the state. The provider's installed-probe " +
			"is status-aware on Debian: a package left in the dpkg 'rc' state (removed, conffiles " +
			"remain) counts as NOT installed, so it converges by reinstalling instead of reporting " +
			"\"already installed\" forever.\n\n" +
			"The installed-probe command is manager-specific:\n\n" +
			"| Manager | Installed probe |\n" +
			"|---|---|\n" +
			"| apt | `dpkg-query -W -f='${db:Status-Status}\\n' <package>` — only a line reading `installed` counts; a package in the dpkg `rc` state (removed, conffiles remain) is NOT installed |\n" +
			"| yum / dnf | `rpm -q <package>` |\n" +
			"| brew | `brew list --formula <package>` |",
		Apply: "Refreshes the package cache first when `refresh` is true. Installs the package: an " +
			"undeclared `version` installs the latest available candidate; a declared `version` " +
			"installs exactly that pin. The apt provider passes `--allow-downgrades` whenever a " +
			"version is pinned (apt otherwise refuses a downgrade); the yum provider verifies the " +
			"pin actually landed and falls back to an explicit `yum downgrade` when a plain install " +
			"silently no-ops on a downgrade (`yum install pkg-<older>` prints \"Nothing to do\" and " +
			"exits 0); dnf converges an explicit version downgrade on its own. On Debian/Ubuntu the " +
			"apt provider runs the install (and the refresh and the Revert removal) fully " +
			"non-interactively: `DEBIAN_FRONTEND=noninteractive` suppresses debconf prompts, and " +
			"`-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold` resolves a " +
			"conffile prompt automatically (the packaged default where there is no local edit, " +
			"otherwise the admin's modified conffile) — without these, a prompt would hang the " +
			"peel's single serialized exec worker forever, since there is no TTY to answer it. " +
			"Reports Changed with the package name and manager in its details.\n\n" +
			"The base install command is manager-specific (the apt non-interactive env and the " +
			"`--allow-downgrades`/conffile flags described above are layered on top; a declared " +
			"`version` pins the target — `<package>=<version>` on apt, `<package>-<version>` on " +
			"yum/dnf, `<package>@<version>` on brew):\n\n" +
			"| Manager | Install command |\n" +
			"|---|---|\n" +
			"| apt | `apt-get install -y <package>` |\n" +
			"| yum | `yum install -y <package>` |\n" +
			"| dnf | `dnf install -y <package>` |\n" +
			"| brew | `brew install <package>` |",
		Revert: "Removes the package through the detected package manager. Revert always removes — " +
			"it does not check whether this run's Apply actually installed anything, and it does not " +
			"restore whatever version was installed before Apply ran.\n\n" +
			"The remove command is manager-specific:\n\n" +
			"| Manager | Remove command |\n" +
			"|---|---|\n" +
			"| apt | `apt-get remove -y <package>` (same non-interactive env and dpkg conffile flags as install) |\n" +
			"| yum | `yum remove -y <package>` |\n" +
			"| dnf | `dnf remove -y <package>` |\n" +
			"| brew | `brew uninstall <package>` |",
	},
	Examples: []modschema.Example{
		{
			Title:       "Install a package ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the package name.",
			Code:        "zester 'web-01' pkg.installed nginx",
		},
		{
			Title:       "Install with a cache refresh",
			Kind:        "state",
			Explanation: "refresh: true refreshes the package cache before installing.",
			Code: "install_nginx:\n  pkg.installed:\n    - name: nginx\n" +
				"    - refresh: true\n",
		},
		{
			Title:       "Pin an exact version",
			Kind:        "state",
			Explanation: "A declared version is part of the desired state: any other installed version converges via install or downgrade.",
			Code: "install_specific_version:\n  pkg.installed:\n    - name: nginx\n" +
				"    - version: \"1.24.0-1ubuntu1\"\n",
		},
		{
			Title:       "Order installs with requisites",
			Kind:        "state",
			Explanation: "require orders this install after a prerequisite package.",
			Code: "install_curl:\n  pkg.installed:\n    - name: curl\n    - require:\n" +
				"      - \"pkg.installed:install_build_tools\"\n",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "How the package manager is chosen",
			Body: "The active package manager comes from the peel's provider detection, run once " +
				"at startup: the `os.family` fact selects apt (debian), dnf-else-yum (the redhat " +
				"family), or brew (darwin, only when the `brew` binary is present); when the fact " +
				"does not match, PATH is probed in the order apt-get, dnf, yum, brew. When none is " +
				"found, the state never reaches Check or Apply: the builder itself refuses to " +
				"construct it (\"pkg.installed: no package provider available\"), so a host with no " +
				"detected package manager fails at state-build time, not at Check or Apply time.",
		},
		{
			Level: "info",
			Title: "apt runs fully non-interactively",
			Body: "On Debian/Ubuntu, every apt-get invocation the provider makes — refresh, install, " +
				"and Revert's removal — sets DEBIAN_FRONTEND=noninteractive and passes " +
				"-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold, so a debconf " +
				"prompt or a conffile conflict resolves automatically instead of hanging the peel's " +
				"single serialized exec worker (there is no TTY to answer a prompt).",
		},
		{
			Level: "info",
			Title: "Version pinning converges both directions",
			Body: "Declaring `version` turns exact version equality into desired state: any OTHER " +
				"installed version — newer or older — is drift, and Apply installs or downgrades to " +
				"the pin. Leaving `version` undeclared never compares versions at all, so any " +
				"installed version satisfies the state.",
		},
		{
			Level: "warning",
			Title: "Revert always removes",
			Body: "Unlike `pkg.removed`/`pkg.purged`, Revert unconditionally removes the package — " +
				"it never restores whatever version was present before Apply ran.",
		},
		{
			Level: "info",
			Title: "Multiple packages",
			Body: "Salt's `pkgs` (multi-package) parameter is not supported; use the generic " +
				"`names` attribute to expand one state per package.",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7"},
	SeeAlso:     []string{"pkg.latest", "pkg.purged", "pkg.removed"},
}

// NewPkgInstalledBuilder returns a state.Builder that creates PkgInstalled
// states using the given ModuleContext's package provider. Decode policy
// (unknown-key handling, reserved keys) is threaded via opts; the peel
// supplies it through modules.RegisterAll.
func NewPkgInstalledBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Package == nil {
			return nil, fmt.Errorf("pkg.installed: no package provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		p := &PkgInstalled{}
		if _, err := pkgInstalledSpec.Decode(id, config, p, opts); err != nil {
			return nil, fmt.Errorf("pkg.installed: %w", err)
		}
		p.id = id
		p.pkg = mctx.Package
		p.reqs = state.ParseRequisites(config)
		return p, nil
	}
}

func (p *PkgInstalled) Name() string           { return "pkg.installed:" + p.id }
func (p *PkgInstalled) Reqs() state.Requisites { return p.reqs }

func (p *PkgInstalled) Check(ctx context.Context) (state.CheckResult, error) {
	installed, err := p.pkg.IsInstalled(ctx, p.Package)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("pkg.installed: check %s: %w", p.Package, err)
	}

	if !installed {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("%s needs to be installed", p.Package),
		}, nil
	}

	// A declared version pin is part of the desired state: presence alone
	// does not satisfy it. Only compare when the state declares version:
	// — an undeclared pin must never churn on whatever happens to be
	// installed.
	if p.Version != "" {
		got, err := p.pkg.InstalledVersion(ctx, p.Package)
		if err != nil {
			return state.CheckResult{}, fmt.Errorf("pkg.installed: installed version of %s: %w", p.Package, err)
		}
		if got != p.Version {
			return state.CheckResult{
				NeedsChange: true,
				Diff:        fmt.Sprintf("%s version mismatch: got %q, want %q", p.Package, got, p.Version),
			}, nil
		}
	}

	return state.CheckResult{
		NeedsChange: false,
		Diff:        fmt.Sprintf("%s is already installed", p.Package),
	}, nil
}

func (p *PkgInstalled) Apply(ctx context.Context) (state.ApplyResult, error) {
	if p.Refresh {
		if err := p.pkg.Refresh(ctx); err != nil {
			return state.ApplyResult{}, fmt.Errorf("pkg.installed: refresh: %w", err)
		}
	}

	if err := p.pkg.Install(ctx, p.Package, p.Version); err != nil {
		return state.ApplyResult{}, fmt.Errorf("pkg.installed: install %s: %w", p.Package, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("installed %s via %s", p.Package, p.pkg.Name()),
		Details: map[string]string{
			"package": p.Package,
			"manager": p.pkg.Name(),
		},
	}, nil
}

func (p *PkgInstalled) Revert(ctx context.Context) (state.ApplyResult, error) {
	if err := p.pkg.Remove(ctx, p.Package); err != nil {
		return state.ApplyResult{}, fmt.Errorf("pkg.installed: remove %s: %w", p.Package, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed %s via %s", p.Package, p.pkg.Name()),
	}, nil
}
