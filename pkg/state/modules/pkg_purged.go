package modules

import (
	"context"
	"fmt"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
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

	// pkg reports installation status and handles reinstall on revert. May be nil.
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
	installed, err := p.isInstalled(ctx)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("pkg.purged: check %s: %w", p.Package, err)
	}

	if !installed {
		return state.CheckResult{
			NeedsChange: false,
			Diff:        fmt.Sprintf("%s is not installed", p.Package),
		}, nil
	}

	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%s is installed and needs to be purged", p.Package),
	}, nil
}

// isInstalled uses PackageExec when available, otherwise probes via the
// package manager's query tool (dpkg / rpm).
func (p *PkgPurged) isInstalled(ctx context.Context) (bool, error) {
	if p.pkg != nil {
		return p.pkg.IsInstalled(ctx, p.Package)
	}

	switch p.family {
	case "debian":
		res, err := p.cmd.Run(ctx, exec.CommandOpts{
			Command: "dpkg",
			Args:    []string{"-s", p.Package},
		})
		return err == nil && res != nil && res.ExitCode == 0, nil
	case "redhat":
		res, err := p.cmd.Run(ctx, exec.CommandOpts{
			Command: "rpm",
			Args:    []string{"-q", p.Package},
		})
		return err == nil && res != nil && res.ExitCode == 0, nil
	default:
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

func (p *PkgPurged) Revert(ctx context.Context) (state.ApplyResult, error) {
	if p.pkg != nil {
		if err := p.pkg.Install(ctx, p.Package, ""); err != nil {
			return state.ApplyResult{}, fmt.Errorf("pkg.purged: reinstall %s: %w", p.Package, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("reinstalled %s via %s", p.Package, p.pkg.Name()),
		}, nil
	}

	if p.family == "" {
		return state.ApplyResult{}, fmt.Errorf("pkg.purged: cannot determine package system for %s", p.Package)
	}

	if _, err := p.cmd.Run(ctx, exec.CommandOpts{
		Command: p.mgr,
		Args:    []string{"install", "-y", p.Package},
	}); err != nil {
		return state.ApplyResult{}, fmt.Errorf("pkg.purged: reinstall %s: %w", p.Package, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("reinstalled %s via %s", p.Package, p.mgr),
	}, nil
}
