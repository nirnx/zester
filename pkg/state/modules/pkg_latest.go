package modules

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// PkgLatest implements the pkg.latest state.
// It ensures a package is installed and kept at the newest available version.
// Upgradability is probed via the package manager CLI (apt-get / dnf / yum)
// through the injected CommandExec; when it cannot be determined it falls back
// to a plain PackageExec.IsInstalled + install.
type PkgLatest struct {
	id   string
	reqs state.Requisites

	// Package is the name of the package to keep up to date.
	Package string

	// Refresh runs a package cache refresh at the start of BOTH Check and
	// Apply. Defaults to true (matching Salt's pkg.latest semantics, which
	// refresh before deciding). Each phase refreshes independently — no
	// cross-phase state — so a watch-forced Apply that bypasses Check still
	// acts on a fresh index, and a Check-only dry run answers from a fresh
	// index too. Refreshing mutates only the manager's metadata cache, never
	// the managed system state, so it is legitimate in Check.
	Refresh bool

	// family is the detected OS family ("debian", "redhat", "darwin", "").
	family string

	// mgr is the package manager CLI ("apt-get", "dnf", "yum", "brew").
	mgr string

	// pkg performs the actual install/remove and installed check.
	pkg exec.PackageExec

	// cmd probes the package manager for upgradability. May be nil.
	cmd exec.CommandExec

	// log reports non-fatal conditions (refresh failures). Never nil.
	log *slog.Logger
}

// NewPkgLatestBuilder returns a state.Builder that creates PkgLatest states
// using the given ModuleContext's package and command providers.
func NewPkgLatestBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Package == nil {
			return nil, fmt.Errorf("pkg.latest: no package provider available")
		}
		return newPkgLatest(id, config, mctx.Package, mctx.Command, mctx.Facts, mctx.Logger)
	}
}

func newPkgLatest(id string, config map[string]any, pkg exec.PackageExec,
	cmd exec.CommandExec, facts map[string]any, log *slog.Logger) (state.State, error) {

	if log == nil {
		log = slog.Default()
	}
	p := &PkgLatest{id: id, pkg: pkg, cmd: cmd, log: log}

	p.Package, _ = config["name"].(string)
	if p.Package == "" {
		p.Package = id
	}

	// Refresh defaults to true; an explicit bool overrides it.
	p.Refresh = true
	if r, ok := config["refresh"].(bool); ok {
		p.Refresh = r
	}

	p.family, p.mgr = detectPkgSystem(facts, pkg.Name())

	p.reqs = state.ParseRequisites(config)

	return p, nil
}

func (p *PkgLatest) Name() string           { return "pkg.latest:" + p.id }
func (p *PkgLatest) Reqs() state.Requisites { return p.reqs }

// refresh runs the package-cache refresh when enabled, warning (never
// failing) on error: apt-get update exits non-zero when ANY configured repo
// is unreachable/rotted, yet still updates the reachable ones — a hard
// failure here would break every pkg.latest on hosts with one dead
// third-party repo, a worse outcome than answering from a stale (or
// best-effort refreshed) index. Called at the start of BOTH Check and Apply;
// see the Refresh field doc for why the phases refresh independently.
func (p *PkgLatest) refresh(ctx context.Context) {
	if !p.Refresh {
		return
	}
	if err := p.pkg.Refresh(ctx); err != nil {
		p.log.Warn("pkg.latest: cache refresh failed; proceeding with existing index",
			"package", p.Package, "manager", p.pkg.Name(), "error", err)
	}
}

func (p *PkgLatest) Check(ctx context.Context) (state.CheckResult, error) {
	// Refresh BEFORE deciding: the upgradability probe reads the on-disk
	// index, and a stale one short-circuits "already latest" — which would
	// skip Apply and with it any chance of ever refreshing (the bug that
	// made a fleet-wide pkg.latest a silent no-op after a repo publish).
	p.refresh(ctx)

	installed, err := p.pkg.IsInstalled(ctx, p.Package)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("pkg.latest: check %s: %w", p.Package, err)
	}

	if !installed {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("%s is not installed", p.Package),
		}, nil
	}

	upgradable, determined := p.upgradable(ctx)
	if !determined {
		// Could not determine upgradability; treat an installed package as
		// satisfied to avoid needless churn.
		return state.CheckResult{
			NeedsChange: false,
			Diff:        fmt.Sprintf("%s is installed (upgradability unknown)", p.Package),
		}, nil
	}

	if upgradable {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("%s has a newer version available", p.Package),
		}, nil
	}

	return state.CheckResult{
		NeedsChange: false,
		Diff:        fmt.Sprintf("%s is at the latest version", p.Package),
	}, nil
}

// upgradable reports whether a newer version of the package is available.
// The second return value is false when upgradability could not be determined.
func (p *PkgLatest) upgradable(ctx context.Context) (upgradable bool, determined bool) {
	if p.cmd == nil {
		return false, false
	}

	switch p.family {
	case "debian":
		res, _ := p.cmd.Run(ctx, exec.CommandOpts{
			Command: p.mgr, // apt-get
			Args:    []string{"-s", "install", p.Package},
		})
		if res == nil {
			return false, false
		}
		out := res.Stdout + "\n" + res.Stderr
		if strings.Contains(out, "is already the newest version") {
			return false, true
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "Inst ") {
				return true, true
			}
		}
		return false, false

	case "redhat":
		// `<mgr> check-update <pkg>` exits 100 when an update is available,
		// 0 when nothing needs updating. Any other code is inconclusive.
		res, _ := p.cmd.Run(ctx, exec.CommandOpts{
			Command: p.mgr, // dnf or yum
			Args:    []string{"check-update", p.Package},
		})
		if res == nil {
			return false, false
		}
		switch res.ExitCode {
		case 100:
			return true, true
		case 0:
			return false, true
		default:
			return false, false
		}

	default:
		return false, false
	}
}

func (p *PkgLatest) Apply(ctx context.Context) (state.ApplyResult, error) {
	p.refresh(ctx)

	// An empty version installs/upgrades to the latest available candidate.
	if err := p.pkg.Install(ctx, p.Package, ""); err != nil {
		return state.ApplyResult{}, fmt.Errorf("pkg.latest: install %s: %w", p.Package, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("installed/upgraded %s to latest via %s", p.Package, p.pkg.Name()),
		Details: map[string]string{
			"package": p.Package,
			"manager": p.pkg.Name(),
		},
	}, nil
}

func (p *PkgLatest) Revert(ctx context.Context) (state.ApplyResult, error) {
	if err := p.pkg.Remove(ctx, p.Package); err != nil {
		return state.ApplyResult{}, fmt.Errorf("pkg.latest: remove %s: %w", p.Package, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed %s via %s", p.Package, p.pkg.Name()),
	}, nil
}

// pkgFactFamily returns the lowercased os.family fact, or "" when absent.
func pkgFactFamily(facts map[string]any) string {
	m, ok := facts["os"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m["family"].(string)
	return strings.ToLower(strings.TrimSpace(s))
}

// detectPkgSystem classifies the host package system from facts (preferred)
// and the active package provider name (fallback). It returns a normalized
// family ("debian", "redhat", "darwin", or "") and the package manager CLI
// to shell out to ("apt-get", "dnf", "yum", "brew", or "").
func detectPkgSystem(facts map[string]any, providerName string) (family, mgr string) {
	switch pkgFactFamily(facts) {
	case "debian", "ubuntu":
		return "debian", "apt-get"
	case "redhat", "rhel", "fedora", "centos", "suse", "opensuse":
		if providerName == "yum" {
			return "redhat", "yum"
		}
		return "redhat", "dnf"
	case "darwin", "macos":
		return "darwin", "brew"
	}

	// Fall back to the provider name when facts are unavailable.
	switch providerName {
	case "apt":
		return "debian", "apt-get"
	case "dnf":
		return "redhat", "dnf"
	case "yum":
		return "redhat", "yum"
	case "brew":
		return "darwin", "brew"
	}

	return "", ""
}
