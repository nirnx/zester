package pkgmod

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// PkgRemoved implements the pkg.removed state.
// It ensures that a system package is not installed.
//
// PkgRemoved is also its own schema proto: the tagged exported Package field IS
// the module's parameter declaration (one schema declaration per module). The
// unexported runtime fields (id, reqs, pkg) are untagged, so the schema compiler
// skips them.
type PkgRemoved struct {
	id   string
	reqs state.Requisites

	// Package is the name of the package to remove.
	Package string `zester:"name,primary" usage:"package to remove (defaults to the state ID)"`

	// pkg is the injected package execution provider.
	pkg exec.PackageExec
}

// pkgRemovedSpec is the compiled schema + documentation for pkg.removed. It is
// compiled once at package init and executed by every decode path (the builder
// below, and Registry.Parse). The prose is verified against Check/Apply/Revert.
var pkgRemovedSpec = regdef.MustSpec("pkg.removed", modschema.KindState, PkgRemoved{}, modschema.Doc{
	Summary: "Ensure a system package is not installed.",
	Description: "`pkg.removed` ensures the named package is absent from the target, " +
		"removing it through the host's detected package manager (apt, dnf, yum, or " +
		"brew). The package name defaults to the state ID, so a bare `pkg.removed` " +
		"under an `nginx:` key removes `nginx`.",
	Effects: modschema.Effects{
		Check: "Queries the package provider whether the named package is installed " +
			"and needs a change only when it is. The provider's probe is status-aware, " +
			"so a Debian package left in the config-files (\"rc\") state after removal " +
			"reads as not installed and does not re-trigger removal.",
		Apply: "Removes the package through the detected package manager. Reports " +
			"Changed with the package name and the manager in its details.",
		Revert: "Cannot restore the package: the version that was removed is not " +
			"recorded and cannot be re-derived, so Revert is an explicit no-op rather " +
			"than a guessed reinstall — reinstall explicitly with pkg.installed.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Remove a package by name",
			Kind:        "state",
			Explanation: "The name parameter selects the package to remove.",
			Code:        "remove-telnet:\n  pkg.removed:\n    - name: telnet\n",
		},
		{
			Title:       "Remove a package ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the package name.",
			Code:        "zester '*' pkg.removed nginx",
		},
		{
			Title:       "Stop dependent services before removing",
			Kind:        "state",
			Explanation: "Use require to ensure a dependent service is stopped before its package is removed.",
			Code: "remove-old-client:\n  pkg.removed:\n    - name: curl\n    - require:\n" +
				"      - \"cmd.run:stop-service\"\n",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Revert does not reinstall",
			Body: "Because the removed version is never recorded, Revert cannot be " +
				"Apply's inverse. It is an honest no-op; reinstall with pkg.installed.",
		},
		{
			Level: "info",
			Title: "Stop dependent services first",
			Body:  "Use `require` to ensure dependent services are stopped before removal.",
		},
	},
	Divergences: []string{"BD-6"},
})

// NewPkgRemovedBuilder returns a state.Builder that creates PkgRemoved states
// using the given ModuleContext's package provider. Decode policy (unknown-key
// handling, reserved keys) is threaded via opts; the peel supplies it through
// modules.RegisterAll.
func NewPkgRemovedBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Package == nil {
			return nil, fmt.Errorf("pkg.removed: no package provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		p := &PkgRemoved{}
		if _, err := pkgRemovedSpec.Decode(id, config, p, opts); err != nil {
			return nil, fmt.Errorf("pkg.removed: %w", err)
		}
		p.id = id
		p.pkg = mctx.Package
		p.reqs = state.ParseRequisites(config)
		return p, nil
	}
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
