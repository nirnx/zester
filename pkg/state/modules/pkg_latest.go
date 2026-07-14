package modules

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// PkgLatest implements the pkg.latest state.
// It ensures a package is installed and kept at the newest available version.
// Upgradability is probed via the package manager CLI (apt-get / dnf / yum)
// through the injected CommandExec; when it cannot be determined it falls back
// to a plain PackageExec.IsInstalled + install.
//
// PkgLatest is also its own schema proto: the tagged exported Package and
// Refresh fields ARE the module's parameter declaration — both primitives, no
// semantic types needed. Refresh carries an EAGER `default=true` (Salt parity:
// pkg.latest refreshes before deciding unless told not to) — the runtime
// fields (family, mgr, pkg, cmd, log) are untagged, so the schema compiler
// skips them.
type PkgLatest struct {
	id   string
	reqs state.Requisites

	// Package is the name of the package to keep up to date.
	Package string `zester:"name,primary" usage:"package to keep at the newest available version (defaults to the state ID)"`

	// Refresh: pkg.* family component (member-supplied default true).
	pkgRefreshParam

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

// pkgLatestSpec is the compiled schema + documentation for pkg.latest. It is
// compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is verified against the live
// Check/Apply/Revert behavior and the detectPkgSystem/provider logic below.
var pkgLatestSpec = mustSpec("pkg.latest", modschema.KindState, PkgLatest{}, pkgLatestDoc,
	modschema.WithDefault("refresh", "true"))

var pkgLatestDoc = modschema.Doc{
	Summary: "Ensure a package is installed and kept at the newest available version.",
	Description: "`pkg.latest` ensures the named package is installed and upgraded to the newest " +
		"version the package manager can see, refreshing its cache before deciding (by default) so " +
		"a release published since the box's last refresh is never invisible. The package name " +
		"defaults to the state ID. Upgradability is probed by shelling out to the package manager " +
		"CLI (`apt-get -s install`, `dnf`/`yum check-update`); when the probe cannot determine an " +
		"answer — no command provider, or an OS family Zester does not know how to probe (for " +
		"example macOS/Homebrew) — an already-installed package is treated as satisfied rather than " +
		"forced to churn.",
	Effects: modschema.Effects{
		Check: "Refreshes the package cache first when `refresh` is true (Check and Apply refresh " +
			"independently — a stale index at Check time, before Apply's refresh ever ran, was the " +
			"field bug this default fixes: a release published since the box's last refresh was " +
			"invisible forever). Reports a change when the package is not installed. When installed, " +
			"runs an upgradability probe keyed off the detected OS family (the `os.family` fact, " +
			"falling back to the active provider's name): on Debian/Ubuntu, `apt-get -s install " +
			"<pkg>` — an `Inst ` line means an upgrade is available, \"is already the newest " +
			"version\" means up to date; on the RedHat family, `<mgr> check-update <pkg>` — exit " +
			"code 100 means an upgrade is available, 0 means up to date, anything else is " +
			"inconclusive. When the probe cannot run or answer at all (no command provider, unknown " +
			"family, or an inconclusive exit code) an installed package is reported as satisfied " +
			"rather than forced to churn.",
		Apply: "Refreshes the package cache first when `refresh` is true, then installs the package " +
			"with no version pin — which the detected provider resolves to the latest available " +
			"candidate (apt, dnf, yum, or brew). A FAILED refresh only warns and proceeds to the " +
			"install (a rotted third-party repo makes `apt-get update` exit non-zero even though the " +
			"reachable repos still updated — refusing to proceed would break every `pkg.latest` on a " +
			"host with one dead repo). On Debian/Ubuntu the install (and the refresh, and Revert's " +
			"removal) goes through the same apt provider `pkg.installed` uses, which runs fully " +
			"non-interactively: `DEBIAN_FRONTEND=noninteractive` suppresses debconf prompts, and " +
			"`-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold` resolves a " +
			"conffile prompt automatically — without these, a prompt would hang the peel's single " +
			"serialized exec worker forever. Reports Changed with the package name and manager in " +
			"its details.",
		Revert: "Removes the package through the detected package manager. Revert always removes — " +
			"it does not check whether this run's Apply actually installed anything, and it does not " +
			"restore whatever version was installed before Apply ran.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Keep a package current ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the package name.",
			Code:        "zester 'web*' pkg.latest nginx",
		},
		{
			Title:       "Keep a package current",
			Kind:        "state",
			Explanation: "The package name defaults to the state ID; refresh defaults to true.",
			Code:        "nginx:\n  pkg.latest: []\n",
		},
		{
			Title:       "Skip the cache refresh",
			Kind:        "state",
			Explanation: "refresh: false answers from the existing cache only.",
			Code:        "htop:\n  pkg.latest:\n    - refresh: false\n",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "refresh defaults to true",
			Body: "`refresh` defaults to true, matching Salt's `pkg.latest` semantics, which " +
				"refreshes before deciding. Set `refresh: false` to answer from the existing cache " +
				"only.",
		},
		{
			Level: "info",
			Title: "Check and Apply refresh independently",
			Body: "Each phase runs its own refresh — there is no cross-phase memo. A watch-forced " +
				"Apply (which bypasses Check entirely) still refreshes on its own, and a Check-only " +
				"dry run always answers from a freshly refreshed index when `refresh` is true.",
		},
		{
			Level: "info",
			Title: "Which package manager runs the upgrade probe",
			Body: "The `os.family` fact selects debian/ubuntu → `apt-get`, the redhat family " +
				"(redhat, rhel, fedora, centos, suse, opensuse) → `dnf` unless the active package " +
				"provider is specifically named \"yum\" (then `yum`), and darwin/macos → `brew`. When " +
				"the fact is absent or unrecognized, the active package provider's own name resolves " +
				"it instead (apt → apt-get, dnf, yum, brew).",
		},
		{
			Level: "info",
			Title: "apt runs fully non-interactively",
			Body: "On Debian/Ubuntu, the underlying apt provider's refresh, install, and Revert's " +
				"removal all set DEBIAN_FRONTEND=noninteractive and pass " +
				"-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold, so a debconf " +
				"prompt or a conffile conflict resolves automatically instead of hanging the peel's " +
				"single serialized exec worker (there is no TTY to answer a prompt) — the same " +
				"provider `pkg.installed` uses.",
		},
		{
			Level: "warning",
			Title: "Upgradability probe limitations",
			Body: "On platforms where the probe is inconclusive or unavailable (no command " +
				"provider; a family Zester does not know how to probe, e.g. macOS/Homebrew) an " +
				"installed package is reported as satisfied to avoid needless churn — Salt would " +
				"compare against the repository's candidate version there. Salt's `version`, " +
				"`pkgs`, and `fromrepo` parameters are also not supported (use `pkg.installed` for " +
				"version pinning).",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7"},
	SeeAlso:     []string{"pkg.installed", "pkg.purged", "pkg.removed"},
}

// NewPkgLatestBuilder returns a state.Builder that creates PkgLatest states
// using the given ModuleContext's package and command providers. Decode
// policy (unknown-key handling, reserved keys) is threaded via opts.
func NewPkgLatestBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Package == nil {
			return nil, fmt.Errorf("pkg.latest: no package provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected providers, id, and
		// requisites MUST be assigned AFTER it.
		p := &PkgLatest{}
		if _, err := pkgLatestSpec.Decode(id, config, p, opts); err != nil {
			return nil, fmt.Errorf("pkg.latest: %w", err)
		}
		p.id = id
		p.pkg = mctx.Package
		p.cmd = mctx.Command
		p.log = mctx.Logger
		if p.log == nil {
			p.log = slog.Default()
		}
		p.family, p.mgr = detectPkgSystem(mctx.Facts, mctx.Package.Name())
		p.reqs = state.ParseRequisites(config)
		return p, nil
	}
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
		for line := range strings.SplitSeq(out, "\n") {
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
