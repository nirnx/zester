package modules

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// PkgInstalled implements the pkg.installed state.
// It ensures that a system package is installed using the injected PackageExec provider.
type PkgInstalled struct {
	id   string
	reqs state.Requisites

	// Package is the name of the package to install.
	Package string

	// Version is an optional version constraint. When declared, Check
	// compares it against the provider's InstalledVersion — any other
	// installed version needs a change (upgrades AND downgrades converge).
	Version string

	// Refresh forces a package cache refresh before install.
	Refresh bool

	// pkg is the injected package execution provider.
	pkg exec.PackageExec
}

// NewPkgInstalledBuilder returns a state.Builder that creates PkgInstalled
// states using the given ModuleContext's package provider.
func NewPkgInstalledBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Package == nil {
			return nil, fmt.Errorf("pkg.installed: no package provider available")
		}
		return newPkgInstalled(id, config, mctx.Package)
	}
}

func newPkgInstalled(id string, config map[string]any, pkg exec.PackageExec) (state.State, error) {
	p := &PkgInstalled{id: id, pkg: pkg}

	p.Package, _ = config["name"].(string)
	if p.Package == "" {
		p.Package = id
	}

	p.Version, _ = config["version"].(string)
	p.Refresh, _ = config["refresh"].(bool)

	p.reqs = state.ParseRequisites(config)

	return p, nil
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
