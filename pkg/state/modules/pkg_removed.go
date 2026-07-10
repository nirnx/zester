package modules

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// PkgRemoved implements the pkg.removed state.
// It ensures that a system package is not installed.
type PkgRemoved struct {
	id   string
	reqs state.Requisites

	// Package is the name of the package to remove.
	Package string

	// pkg is the injected package execution provider.
	pkg exec.PackageExec
}

// NewPkgRemovedBuilder returns a state.Builder that creates PkgRemoved
// states using the given ModuleContext's package provider.
func NewPkgRemovedBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Package == nil {
			return nil, fmt.Errorf("pkg.removed: no package provider available")
		}
		return newPkgRemoved(id, config, mctx.Package)
	}
}

func newPkgRemoved(id string, config map[string]any, pkg exec.PackageExec) (state.State, error) {
	p := &PkgRemoved{id: id, pkg: pkg}

	p.Package, _ = config["name"].(string)
	if p.Package == "" {
		p.Package = id
	}

	p.reqs = state.ParseRequisites(config)

	return p, nil
}

func (p *PkgRemoved) Name() string           { return "pkg.removed:" + p.id }
func (p *PkgRemoved) Reqs() state.Requisites { return p.reqs }

func (p *PkgRemoved) Check(ctx context.Context) (state.CheckResult, error) {
	installed, err := p.pkg.IsInstalled(ctx, p.Package)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("pkg.removed: check %s: %w", p.Package, err)
	}

	if !installed {
		return state.CheckResult{
			NeedsChange: false,
			Diff:        fmt.Sprintf("%s is not installed", p.Package),
		}, nil
	}

	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%s is installed and needs to be removed", p.Package),
	}, nil
}

func (p *PkgRemoved) Apply(ctx context.Context) (state.ApplyResult, error) {
	if err := p.pkg.Remove(ctx, p.Package); err != nil {
		return state.ApplyResult{}, fmt.Errorf("pkg.removed: remove %s: %w", p.Package, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed %s via %s", p.Package, p.pkg.Name()),
		Details: map[string]string{
			"package": p.Package,
			"manager": p.pkg.Name(),
		},
	}, nil
}

// Revert is an explicit clean no-op: no phase records the version that was
// removed, and it cannot be re-derived after the package is gone, so a
// reinstall here would guess (install whatever the repo's latest candidate
// happens to be — not the inverse of Apply). Reinstall explicitly with
// pkg.installed instead.
func (p *PkgRemoved) Revert(ctx context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{
		Changed: false,
		Diff:    fmt.Sprintf("nothing to revert: removed version of %s was not recorded; reinstall explicitly with pkg.installed", p.Package),
	}, nil
}
