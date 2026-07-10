package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// PkgPurged implements the pkg.purged state.
// It ensures a package is removed along with its configuration files
// (apt-get purge on Debian; dnf/yum remove on RedHat). The purge itself is
// performed via CommandExec so it works regardless of what PackageExec.Remove
// does under the hood.
type PkgPurged struct {
	id   string
	reqs state.Requisites

	// Package is the name of the package to purge.
	Package string

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

// NewPkgPurgedBuilder returns a state.Builder that creates PkgPurged states
// using the given ModuleContext's command and package providers.
func NewPkgPurgedBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("pkg.purged: no command provider available")
		}
		return newPkgPurged(id, config, mctx.Command, mctx.Package, mctx.Facts)
	}
}

func newPkgPurged(id string, config map[string]any, cmd exec.CommandExec,
	pkg exec.PackageExec, facts map[string]any) (state.State, error) {

	p := &PkgPurged{id: id, cmd: cmd, pkg: pkg}

	p.Package, _ = config["name"].(string)
	if p.Package == "" {
		p.Package = id
	}

	providerName := ""
	if pkg != nil {
		providerName = pkg.Name()
	}
	p.family, p.mgr = detectPkgSystem(facts, providerName)

	p.reqs = state.ParseRequisites(config)

	return p, nil
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
		for _, line := range strings.Split(res.Stdout, "\n") {
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
