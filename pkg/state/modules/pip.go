package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
)

// PipInstalled implements the pip.installed state.
// It ensures a Python package is installed via pip.
type PipInstalled struct {
	id   string
	reqs state.Requisites

	// Package is the pip package name. Mutually exclusive with Requirements.
	Package string

	// Version is an optional pinned version (e.g. "1.2.3").
	Version string

	// Requirements is a path to a requirements.txt file.
	// When set, Package is ignored.
	Requirements string

	// Bin is the pip binary to use (default: "pip3").
	Bin string

	cmd exec.CommandExec
}

// NewPipInstalledBuilder returns a state.Builder that creates PipInstalled states
// using the given ModuleContext's command provider.
func NewPipInstalledBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("pip.installed: no command provider available")
		}
		return newPipInstalled(id, config, mctx.Command)
	}
}

func newPipInstalled(id string, config map[string]any, cmd exec.CommandExec) (state.State, error) {
	p := &PipInstalled{id: id, cmd: cmd}

	p.Package, _ = config["name"].(string)
	if p.Package == "" {
		p.Package = id
	}

	p.Version, _ = config["version"].(string)
	p.Requirements, _ = config["requirements"].(string)

	p.Bin, _ = config["bin"].(string)
	if p.Bin == "" {
		p.Bin = "pip3"
	}

	p.reqs = state.ParseRequisites(config)
	return p, nil
}

func (p *PipInstalled) Name() string           { return "pip.installed:" + p.id }
func (p *PipInstalled) Reqs() state.Requisites { return p.reqs }

func (p *PipInstalled) Check(ctx context.Context) (state.CheckResult, error) {
	if p.Requirements != "" {
		// Cannot efficiently check a requirements file without installing; always apply.
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("requirements file %s: cannot verify without installing", p.Requirements),
		}, nil
	}

	result, err := p.cmd.Run(ctx, exec.CommandOpts{
		Command: p.Bin,
		Args:    []string{"show", p.Package},
	})
	if err != nil || (result != nil && result.ExitCode != 0) {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("%s is not installed", p.Package),
		}, nil
	}

	if p.Version != "" {
		installedVer := parsePipShowVersion(result.Stdout)
		if installedVer != p.Version {
			return state.CheckResult{
				NeedsChange: true,
				Diff:        fmt.Sprintf("%s version mismatch: got %q, want %q", p.Package, installedVer, p.Version),
			}, nil
		}
	}

	return state.CheckResult{
		NeedsChange: false,
		Diff:        fmt.Sprintf("%s is already installed", p.Package),
	}, nil
}

func (p *PipInstalled) Apply(ctx context.Context) (state.ApplyResult, error) {
	var args []string
	if p.Requirements != "" {
		args = []string{"install", "-r", p.Requirements}
	} else {
		pkg := p.Package
		if p.Version != "" {
			pkg = pkg + "==" + p.Version
		}
		args = []string{"install", pkg}
	}

	if _, err := p.cmd.Run(ctx, exec.CommandOpts{
		Command: p.Bin,
		Args:    args,
	}); err != nil {
		target := p.Package
		if p.Requirements != "" {
			target = p.Requirements
		}
		return state.ApplyResult{}, fmt.Errorf("pip.installed: install %s: %w", target, err)
	}

	diff := fmt.Sprintf("installed %s via %s", p.Package, p.Bin)
	if p.Requirements != "" {
		diff = fmt.Sprintf("installed requirements from %s via %s", p.Requirements, p.Bin)
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    diff,
		Details: map[string]string{
			"package": p.Package,
			"bin":     p.Bin,
		},
	}, nil
}

func (p *PipInstalled) Revert(ctx context.Context) (state.ApplyResult, error) {
	if p.Requirements != "" {
		return state.ApplyResult{
			Changed: false,
			Diff:    "pip.installed: cannot revert requirements file installation",
		}, nil
	}

	if _, err := p.cmd.Run(ctx, exec.CommandOpts{
		Command: p.Bin,
		Args:    []string{"uninstall", "-y", p.Package},
	}); err != nil {
		return state.ApplyResult{}, fmt.Errorf("pip.installed: uninstall %s: %w", p.Package, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("uninstalled %s via %s", p.Package, p.Bin),
	}, nil
}

// parsePipShowVersion extracts the Version field from `pip show` output.
func parsePipShowVersion(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "Version:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
		}
	}
	return ""
}
