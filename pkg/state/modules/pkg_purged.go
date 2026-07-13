package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// PkgPurged implements the pkg.purged state.
// It ensures a package is removed along with its configuration files
// (apt-get purge on Debian; dnf/yum remove on RedHat). The purge itself is
// performed via CommandExec so it works regardless of what PackageExec.Remove
// does under the hood.
//
// PkgPurged is also its own schema proto: the tagged exported Package field IS
// the module's parameter declaration (one schema declaration per module) — a
// primitive, no semantic type needed. The unexported runtime fields (family,
// mgr, pkg, cmd) are untagged, so the schema compiler skips them.
type PkgPurged struct {
	id   string
	reqs state.Requisites

	// Package is the name of the package to purge.
	Package string `zester:"name,primary" usage:"package to purge, including its configuration files (defaults to the state ID)"`

	// family is the detected OS family ("debian", "redhat", "darwin", "").
	family string

	// mgr is the package manager CLI ("apt-get", "dnf", "yum", "brew").
	mgr string

	// pkg is the unknown-family fallback (probe + remove); the debian/redhat
	// paths deliberately do NOT use it — PackageExec.IsInstalled answers
	// "fully installed?" for pkg.installed/pkg.removed, while pkg.purged must
	// also see residual dpkg 'rc' state (see needsPurge). May be nil.
	pkg exec.PackageExec

	// cmd performs the purge/reinstall by shelling out.
	cmd exec.CommandExec
}

// pkgPurgedSpec is the compiled schema + documentation for pkg.purged. It is
// compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is verified against the live
// Check/Apply/Revert behavior and the detectPkgSystem logic (pkg_latest.go).
var pkgPurgedSpec = mustSpec("pkg.purged", modschema.KindState, PkgPurged{}, modschema.Doc{
	Summary: "Ensure a package is removed along with its configuration files.",
	Description: "`pkg.purged` ensures the named package is fully gone — not just removed, but " +
		"purged of residual package-manager state. On Debian, `apt-get purge` clears the conffiles a " +
		"plain remove leaves behind in dpkg's 'rc' status; RedHat's rpm has no separate purge " +
		"concept, so purge is identical to remove there. The package name defaults to the state ID. " +
		"Purging shells out to the package-manager CLI directly (via `CommandExec`), independent of " +
		"whatever the injected `PackageExec` provider's `Remove` does — so it works even when " +
		"`pkg.installed`'s and `pkg.removed`'s presence probe would already call the package \"not " +
		"installed\".",
	Effects: modschema.Effects{
		Check: "Reports converged only when NO package-manager record exists at all. On Debian the " +
			"probe is `dpkg-query -W -f='${db:Status-Status}' <pkg>` (one status line per installed " +
			"instance, so multi-arch packages are handled correctly): any live status — `installed`, " +
			"the residual `config-files` ('rc') state, `half-installed`, or similar — needs a purge; " +
			"only `not-installed` or no dpkg record at all is converged. On the RedHat family the " +
			"probe is `rpm -q <pkg>` (rpm has no 'rc' equivalent, so a query hit alone means " +
			"installed). On an unknown OS family, Check falls back to the injected `PackageExec`'s " +
			"installed probe (no residual-config concept there either, e.g. brew); without a " +
			"provider, Check errors rather than silently reporting converged. A probe that never " +
			"runs at all (spawn failure, context death) is also a real error, never reported as " +
			"converged — that would silently skip the purge on a broken host.",
		Apply: "Purges the package via the manager-specific CLI command: `apt-get purge -y <pkg>` on " +
			"Debian, `<mgr> remove -y <pkg>` on the RedHat family (dnf or yum — identical to a plain " +
			"remove there). On an unknown OS family it falls back to the injected `PackageExec`'s " +
			"`Remove`; without either a command provider or a package provider, Apply errors. Reports " +
			"Changed with the package name and manager in its details.",
		Revert: "Explicit no-op (same contract as `pkg.removed`): the purged version and the purged " +
			"configuration files are never recorded and cannot be reconstructed, so a reinstall here " +
			"would only guess at whatever the repo's current latest candidate is — not the inverse of " +
			"Apply. Reinstall explicitly with `pkg.installed`.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Purge a package ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the package name.",
			Code:        "zester 'web-01' pkg.purged apache2",
		},
		{
			Title:       "Purge a package",
			Kind:        "state",
			Explanation: "The package name defaults to the state ID.",
			Code:        "apache2:\n  pkg.purged: []\n",
		},
		{
			Title:       "Purge before installing a replacement",
			Kind:        "state",
			Explanation: "require_in orders this purge ahead of a replacement's install: apache2's package and configuration are fully gone before nginx's pkg.installed runs.",
			Code: "remove-old-webserver:\n  pkg.purged:\n    - name: apache2\n    - require_in:\n" +
				"      - \"pkg.installed:nginx\"\n",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Purge's value over pkg.removed",
			Body: "pkg.removed's and pkg.installed's installed-probe deliberately reports a dpkg " +
				"'rc'-state package as NOT installed (so a fresh install/removal doesn't churn on " +
				"leftover conffiles). pkg.purged's whole purpose is clearing that residual state, so " +
				"its own Check probe deliberately does NOT go through the shared " +
				"`PackageExec.IsInstalled` — it queries dpkg/rpm status directly.",
		},
		{
			Level: "info",
			Title: "RedHat purge equals remove",
			Body: "rpm tracks no separate purge state, so on the RedHat family `pkg.purged` and " +
				"`pkg.removed` run the identical remove command.",
		},
		{
			Level: "info",
			Title: "Multiple packages",
			Body: "Salt's `pkgs` (multi-package) parameter is not supported; use the generic " +
				"`names` attribute to expand one state per package.",
		},
	},
	Divergences: []string{"BD-6"},
	SeeAlso:     []string{"pkg.installed", "pkg.latest", "pkg.removed"},
})

// NewPkgPurgedBuilder returns a state.Builder that creates PkgPurged states
// using the given ModuleContext's command and package providers. Decode
// policy (unknown-key handling, reserved keys) is threaded via opts.
func NewPkgPurgedBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("pkg.purged: no command provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected providers, id, and
		// requisites MUST be assigned AFTER it.
		p := &PkgPurged{}
		if _, err := pkgPurgedSpec.Decode(id, config, p, opts); err != nil {
			return nil, fmt.Errorf("pkg.purged: %w", err)
		}
		p.id = id
		p.cmd = mctx.Command
		p.pkg = mctx.Package

		providerName := ""
		if mctx.Package != nil {
			providerName = mctx.Package.Name()
		}
		p.family, p.mgr = detectPkgSystem(mctx.Facts, providerName)

		p.reqs = state.ParseRequisites(config)
		return p, nil
	}
}

func (p *PkgPurged) Name() string           { return "pkg.purged:" + p.id }
func (p *PkgPurged) Reqs() state.Requisites { return p.reqs }

func (p *PkgPurged) Check(ctx context.Context) (state.CheckResult, error) {
	needs, err := p.needsPurge(ctx)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("pkg.purged: check %s: %w", p.Package, err)
	}

	if !needs {
		return state.CheckResult{
			NeedsChange: false,
			Diff:        fmt.Sprintf("%s has no package record", p.Package),
		}, nil
	}

	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%s is installed or has residual package state and needs to be purged", p.Package),
	}, nil
}

// needsPurge reports whether ANY package-manager record exists for the
// package. pkg.purged's entire value over pkg.removed is clearing residual
// state: on Debian a removed-but-not-purged package (dpkg 'rc' state —
// conffiles remain) NEEDS a purge, which is exactly what Apply's
// `apt-get purge` clears — converged means no dpkg record at all.
// PackageExec.IsInstalled cannot answer this (it deliberately reports 'rc'
// as NOT installed for pkg.installed/pkg.removed), so the probe runs its own
// status query.
func (p *PkgPurged) needsPurge(ctx context.Context) (bool, error) {
	switch p.family {
	case "debian":
		// The \n separator matters for multi-arch: instance statuses would
		// otherwise concatenate ("installedinstalled") and match nothing.
		res, err := p.cmd.Run(ctx, exec.CommandOpts{
			Command: "dpkg-query",
			Args:    []string{"-W", "-f=${db:Status-Status}\n", p.Package},
		})
		if res == nil {
			// The probe never ran (spawn failure, context death) — a REAL
			// error, not "no record"; reporting converged would silently
			// skip the purge on broken hosts.
			return false, fmt.Errorf("query %s: %w", p.Package, err)
		}
		if err != nil {
			return false, nil // ran, non-zero exit: no dpkg record at all
		}
		for line := range strings.SplitSeq(res.Stdout, "\n") {
			s := strings.TrimSpace(line)
			// Any live status — installed, config-files ('rc'), half-*,
			// unpacked — leaves state behind that purge clears.
			if s != "" && s != "not-installed" {
				return true, nil
			}
		}
		return false, nil
	case "redhat":
		// rpm has no 'rc' equivalent: a query hit means installed.
		res, err := p.cmd.Run(ctx, exec.CommandOpts{
			Command: "rpm",
			Args:    []string{"-q", p.Package},
		})
		if res == nil {
			return false, fmt.Errorf("query %s: %w", p.Package, err)
		}
		return err == nil && res.ExitCode == 0, nil
	default:
		// Unknown family: mirror Apply's PackageExec fallback (no residual
		// config-state concept there either, e.g. brew).
		if p.pkg != nil {
			return p.pkg.IsInstalled(ctx, p.Package)
		}
		return false, fmt.Errorf("cannot determine package system for %s", p.Package)
	}
}

func (p *PkgPurged) Apply(ctx context.Context) (state.ApplyResult, error) {
	var opts exec.CommandOpts
	switch p.family {
	case "debian":
		opts = exec.CommandOpts{Command: p.mgr, Args: []string{"purge", "-y", p.Package}}
	case "redhat":
		opts = exec.CommandOpts{Command: p.mgr, Args: []string{"remove", "-y", p.Package}}
	default:
		// Fall back to PackageExec.Remove when the manager is unknown.
		if p.pkg != nil {
			if err := p.pkg.Remove(ctx, p.Package); err != nil {
				return state.ApplyResult{}, fmt.Errorf("pkg.purged: remove %s: %w", p.Package, err)
			}
			return state.ApplyResult{
				Changed: true,
				Diff:    fmt.Sprintf("removed %s via %s", p.Package, p.pkg.Name()),
				Details: map[string]string{"package": p.Package, "manager": p.pkg.Name()},
			}, nil
		}
		return state.ApplyResult{}, fmt.Errorf("pkg.purged: cannot determine package system for %s", p.Package)
	}

	if _, err := p.cmd.Run(ctx, opts); err != nil {
		return state.ApplyResult{}, fmt.Errorf("pkg.purged: purge %s: %w", p.Package, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("purged %s via %s", p.Package, p.mgr),
		Details: map[string]string{
			"package": p.Package,
			"manager": p.mgr,
		},
	}, nil
}

// Revert is an explicit clean no-op (same contract as pkg.removed): no phase
// records the version that was purged, and the purged configuration files
// cannot be restored at all, so a reinstall here would guess — install
// whatever the repo's latest candidate happens to be, without the purged
// conffiles: doubly not the inverse of Apply. Reinstall explicitly with
// pkg.installed instead.
func (p *PkgPurged) Revert(ctx context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{
		Changed: false,
		Diff:    fmt.Sprintf("nothing to revert: purged version and configuration of %s were not recorded; reinstall explicitly with pkg.installed", p.Package),
	}, nil
}
