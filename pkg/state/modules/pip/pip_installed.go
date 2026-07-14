package pipmod

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// PipInstalled implements the pip.installed state.
// It ensures a Python package is installed via pip.
//
// PipInstalled is also its own schema proto: the tagged exported fields ARE
// the module's parameter declaration (one schema declaration per module) —
// all four are primitive strings, no semantic types are needed. `bin` carries
// an EAGER `default=pip3`, reproducing the legacy construction-time default.
// Under the uniform decoder a numeric `name`/`version`/`requirements`/`bin`
// coerces to its string form and a composite is rejected (BD-6). The
// unexported runtime field (cmd) is untagged, so the schema compiler skips
// it.
type PipInstalled struct {
	id   string
	reqs state.Requisites

	// Package is the pip package name; it defaults to the state ID. Ignored
	// when Requirements is set.
	Package string `zester:"name,primary" usage:"pip package name; defaults to the state ID; ignored when requirements is set"`

	// Version is an optional pinned version (e.g. "1.2.3").
	Version string `zester:"version" usage:"optional pinned version (e.g. 1.2.3), appended as package==version"`

	// Requirements is a path to a requirements.txt file.
	// When set, Package is ignored.
	Requirements string `zester:"requirements" usage:"path to a requirements.txt file; when set, name is ignored for install"`

	// Bin is the pip binary to use (default: "pip3").
	Bin string `zester:"bin,default=pip3" usage:"path or name of the pip binary to use; defaults to pip3"`

	cmd exec.CommandExec
}

// pipInstalledSpec is the compiled schema + documentation for pip.installed.
// It is compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is verified against the live
// Check/Apply/Revert behavior.
var pipInstalledSpec = regdef.MustSpec("pip.installed", modschema.KindState, PipInstalled{}, modschema.Doc{
	Summary: "Ensure a Python package is installed via pip, optionally pinned or from a requirements file.",
	Description: "`pip.installed` ensures a Python package (`name`, defaulting to the state ID) is " +
		"installed via `bin` (defaulting to `pip3`). Declaring `requirements` switches to installing from " +
		"a requirements.txt file instead — `name`/`version` are then ignored for the install itself, " +
		"though `name` still names the state.",
	Effects: modschema.Effects{
		Check: "With `requirements` set, ALWAYS reports a change: a requirements file cannot be verified " +
			"without actually running the install. Otherwise runs `<bin> show <name>`; a non-zero exit " +
			"means the package needs installing. When `version` is declared and the package is present, " +
			"parses the `Version:` line from `pip show` output and reports a change on any mismatch " +
			"(compared as strings, so pre-release suffixes matter).",
		Apply: "Package mode: `<bin> install <name>[==version]`. Requirements mode: `<bin> install -r " +
			"<requirements>`. Reports the package and bin in its details.",
		Revert: "Package mode: `<bin> uninstall -y <name>`. Requirements mode: an explicit no-op " +
			"(`Changed: false`) — there is no record of which packages a requirements file installed, so " +
			"there is nothing safe to uninstall.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Install the latest version",
			Kind:        "state",
			Explanation: "The package name defaults to the state ID.",
			Code:        "requests:\n  pip.installed: []\n",
		},
		{
			Title:       "Pin to a specific version",
			Kind:        "state",
			Explanation: "version makes exact version equality part of the desired state.",
			Code:        "requests:\n  pip.installed:\n    - version: \"2.28.0\"\n",
		},
		{
			Title:       "Install from a requirements file",
			Kind:        "state",
			Explanation: "requirements installs every dependency listed in the file; name is ignored for install.",
			Code: "myapp-dependencies:\n  pip.installed:\n    - requirements: /opt/myapp/requirements.txt\n" +
				"    - require:\n      - pkg.installed:python3-pip\n",
		},
		{
			Title:       "Use a virtual environment's pip",
			Kind:        "state",
			Explanation: "bin overrides the pip binary, e.g. to target a venv.",
			Code: "myapp-venv-requests:\n  pip.installed:\n    - name: requests\n" +
				"    - bin: /opt/myapp/venv/bin/pip\n",
		},
		{
			Title:       "Install a package ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the package name.",
			Code:        "zester '*' pip.installed requests",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "bin must be on PATH (or an absolute path)",
			Body:  "The module requires `pip3` (or the configured `bin`) to be resolvable. There is no cache refresh step; run `pip install --upgrade pip` separately if needed.",
		},
		{
			Level: "info",
			Title: "Version comparison is string-based",
			Body:  "Version comparison uses the `Version:` field from `pip show` output, compared as a string. Pre-release suffixes (e.g. `2.28.0rc1`) are compared literally, not semantically.",
		},
	},
	Divergences: []string{"BD-6"},
})

// NewPipInstalledBuilder returns a state.Builder that creates PipInstalled
// states using the given ModuleContext's command provider. Decode policy
// (unknown-key handling, reserved keys) is threaded via opts; the peel
// supplies it through modules.RegisterAll.
func NewPipInstalledBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("pip.installed: no command provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		p := &PipInstalled{}
		if _, err := pipInstalledSpec.Decode(id, config, p, opts); err != nil {
			return nil, fmt.Errorf("pip.installed: %w", err)
		}
		p.id = id
		p.cmd = mctx.Command
		p.reqs = state.ParseRequisites(config)
		return p, nil
	}
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
	for line := range strings.SplitSeq(output, "\n") {
		if rest, ok := strings.CutPrefix(line, "Version:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
