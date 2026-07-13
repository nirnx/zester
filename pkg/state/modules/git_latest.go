package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// GitLatest implements the git.latest state.
// Like git.cloned, it ensures a repository is present at a target directory,
// but it also updates an existing clone to the latest commit on the tracked
// branch/rev (fetch + fast-forward, or reset when force is set).
//
// GitLatest is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module) — all
// four are primitives (three strings and a bool), no semantic types are
// needed. `name`'s PRIMARY MEANING is the remote URL (contrast with
// git.cloned, whose primary is the clone PATH — see the Notes section below,
// unmissable in both generated pages); the clone path is the separate
// `target` parameter, `required`. Under the uniform decoder a numeric
// `name`/`target`/`branch`/`rev` coerces to its string form and a composite
// is rejected (BD-6); `force` honors an integer 1/0 (BD-7) and a truthy/falsy
// string (BD-2) where the legacy `.(bool)` assertion silently dropped them.
// `name` is `primary`, not `required` — it legitimately falls back to the
// state ID — but the builder tail restores the legacy required-error for the
// case where BOTH are empty (parity restoration, not a BD; see the builder).
// The unexported runtime fields (id, reqs, cmd, file, revert memos) are
// untagged, so the schema compiler skips them.
type GitLatest struct {
	id   string
	reqs state.Requisites

	// URL is the remote repository URL; it defaults to the state ID. The
	// builder tail errors if both are empty (see NewGitLatestBuilder).
	URL string `zester:"name,primary" usage:"remote repository URL; defaults to the state ID (contrast with git.cloned, whose name/primary is the clone path); required if the state ID is also empty"`

	// Target is the filesystem path the repo lives in (required).
	Target string `zester:"target,required" usage:"filesystem path for the working tree; required"`

	// Branch is an optional branch to track.
	Branch string `zester:"branch" usage:"branch to track; used for git clone --branch, checkout, and the remote-tip comparison"`

	// Rev is an optional specific commit/ref to check out.
	Rev string `zester:"rev" usage:"pin to a specific commit/ref; when set, the up-to-date check is a local HEAD comparison (no network)"`

	// Force resets local changes (git reset --hard) instead of fast-forward.
	Force bool `zester:"force" usage:"discard local changes: update with git reset --hard (to rev, origin/<branch>, or origin/HEAD) instead of a fast-forward merge; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	cmd  exec.CommandExec
	file exec.FileExec

	// createdByApply tracks whether a same-instance Apply cloned the repo, so
	// Revert can safely remove the directory. Valid only for a same-instance
	// Apply→Revert sequence; a fresh instance (any runner ModeRevert run —
	// states are rebuilt per execution) leaves it false.
	createdByApply bool

	// prevHead stores the pre-update HEAD sha for an existing clone so Revert
	// can reset back to it. Same same-instance-only validity as
	// createdByApply; with both memos unset Revert is an explicit clean no-op.
	prevHead string
}

// gitLatestSpec is the compiled schema + documentation for git.latest. It is
// compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is drift-corrected against
// the live Check/Apply/Revert behavior — notably that `rev` converges via a
// local commit-id comparison (not just a sha-prefix match), so a pinned TAG
// name does converge once checked out.
var gitLatestSpec = mustSpec("git.latest", modschema.KindState, GitLatest{}, modschema.Doc{
	Summary: "Ensure a Git repository is cloned at a target directory and kept up to date with its remote.",
	Description: "`git.latest` ensures a repository is present at `target` (required) AND keeps it " +
		"current with its remote: an existing clone is fetched and fast-forwarded (or hard-reset with " +
		"`force`). `name` (defaulting to the state ID) is the remote URL — NOTE: this is the opposite of " +
		"`git.cloned`, whose `name`/primary is the clone PATH and whose `target`-equivalent is instead " +
		"folded into `name`. Compare with [`git.cloned`](/docs/guides/modules/git-cloned), which only " +
		"ensures the clone exists at a revision and never updates a moving branch tip. `name`'s state-ID " +
		"fallback still applies, but a state declaring neither `name` NOR a non-empty state ID has no URL " +
		"at all — that combination is rejected with a required-parameter error.",
	Effects: modschema.Effects{
		Check: "Reports a change when `target` does not exist (any stat error OTHER than not-exist fails " +
			"the phase). Otherwise compares `git remote get-url origin` against `name`; a mismatch needs a " +
			"change. With `rev` set: no change needed if local `HEAD` matches `rev` — a hex-looking rev by " +
			"sha PREFIX (no subprocess), a tag or other symbolic rev resolved LOCALLY via `git rev-parse " +
			"--verify <rev>^{commit}` (no network) and compared by commit id, so a checked-out tag " +
			"CONVERGES rather than perpetually reporting a change. Without `rev`: `git ls-remote origin " +
			"<branch|HEAD>` is compared against the local `HEAD`; a differing remote tip needs a change.",
		Apply: "Fresh clone (target missing): `git clone [--branch <branch>] <name> <target>`, then `git " +
			"checkout <rev>` when `rev` is set. Existing clone: corrects the origin URL with `git remote " +
			"set-url` if it drifted, records the current HEAD (for revert), then `git fetch origin`. Update " +
			"strategy: `force: true` → `git reset --hard` to `rev`, `origin/<branch>`, or `origin/HEAD`; " +
			"`rev` set → `git checkout <rev>`; `branch` set → `git checkout <branch>` + `git merge --ff-only " +
			"origin/<branch>`; neither → `git merge --ff-only` (fails if the local branch diverged — use " +
			"`force`). Reports the url and target in its details.",
		Revert: "If Apply cloned the repository, removes the target directory recursively. If Apply " +
			"updated an existing clone, runs `git reset --hard <previous HEAD>` to the HEAD recorded before " +
			"the update. A fresh instance (a standalone revert) recorded nothing and is an explicit clean " +
			"no-op.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Track a branch",
			Kind:        "state",
			Explanation: "name (the state ID here) is the remote URL; target is the clone path.",
			Code: "https://github.com/example/app.git:\n  git.latest:\n    - target: /opt/app\n" +
				"    - branch: main\n",
		},
		{
			Title:       "Force-sync, discarding local changes",
			Kind:        "state",
			Explanation: "force replaces a fast-forward merge with a hard reset when the local branch has diverged.",
			Code: "deploy-config:\n  git.latest:\n    - name: git@git.internal:ops/config.git\n" +
				"    - target: /etc/app-config\n    - branch: production\n    - force: true\n" +
				"    - require:\n      - pkg.installed:git\n",
		},
		{
			Title:       "Clone/update ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the remote URL; target is a key=value.",
			Code:        "zester '*' git.latest https://github.com/example/app.git target=/opt/app",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "warn",
			Title: "name means the remote URL here — the opposite of git.cloned",
			Body: "`git.latest`'s `name` (and state-ID default) is the remote URL; the clone path is the " +
				"separate `target` parameter. `git.cloned`'s `name` is instead the clone PATH. Mixing the " +
				"two up under a shared naming assumption is the most common authoring mistake between these " +
				"two modules.",
		},
		{
			Level: "info",
			Title: "rev resolves tags locally, no network round-trip",
			Body: "A hex-looking `rev` matches by sha PREFIX against local `HEAD`. A tag name or other " +
				"symbolic rev is resolved with a local, network-free `git rev-parse --verify " +
				"<rev>^{commit}` and compared by commit id, so a pinned tag converges once checked out — " +
				"it is not limited to a literal prefix match against a hash.",
		},
		{
			Level: "info",
			Title: "Divergences from Salt",
			Body: "Salt's `user`, `identity` (SSH key), `submodules`, and `depth` parameters are not " +
				"supported. Authentication relies on the peel's ambient git configuration (credential " +
				"helpers, ssh-agent).",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7"},
	SeeAlso:     []string{"git.cloned"},
})

// NewGitLatestBuilder returns a state.Builder that creates GitLatest states.
// Decode policy (unknown-key handling, reserved keys) is threaded via opts;
// the peel supplies it through modules.RegisterAll.
func NewGitLatestBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("git.latest: no command provider available")
		}
		if mctx.File == nil {
			return nil, fmt.Errorf("git.latest: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected providers, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		g := &GitLatest{}
		if _, err := gitLatestSpec.Decode(id, config, g, opts); err != nil {
			return nil, fmt.Errorf("git.latest: %w", err)
		}
		// Builder-tail parity check (not schema): `name` is `primary`, not
		// `required` — it legitimately falls back to the state ID. But when
		// BOTH are empty there is no URL to clone/track at all, so restore the
		// legacy required-error rather than silently proceeding with an empty
		// URL (parity restoration, not a BD — the empty-primary fallback rule
		// itself is unchanged).
		if g.URL == "" {
			return nil, fmt.Errorf("git.latest: %s: url (name) is required", id)
		}
		g.id = id
		g.cmd = mctx.Command
		g.file = mctx.File
		g.reqs = state.ParseRequisites(config)
		return g, nil
	}
}

func (g *GitLatest) Name() string           { return "git.latest:" + g.id }
func (g *GitLatest) Reqs() state.Requisites { return g.reqs }

func (g *GitLatest) Check(ctx context.Context) (state.CheckResult, error) {
	// Only fs.ErrNotExist means absent; any other stat error fails the
	// phase — see GitCloned.Check.
	if _, err := g.file.Stat(ctx, g.Target); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return state.CheckResult{}, fmt.Errorf("git.latest: stat %s: %w", g.Target, err)
		}
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("target %s does not exist", g.Target),
		}, nil
	}

	// Verify the remote URL matches.
	urlRes, err := g.git(ctx, "-C", g.Target, "remote", "get-url", "origin")
	if err != nil {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("could not read remote for %s: %v", g.Target, err),
		}, nil
	}
	if strings.TrimSpace(urlRes.Stdout) != g.URL {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("remote URL mismatch in %s", g.Target),
		}, nil
	}

	headRes, err := g.git(ctx, "-C", g.Target, "rev-parse", "HEAD")
	if err != nil {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("could not read HEAD for %s: %v", g.Target, err),
		}, nil
	}
	head := strings.TrimSpace(headRes.Stdout)

	// Pinned rev: local comparison only, no network. Sha revs match by
	// prefix; symbolic revs (tags) resolve via rev-parse ^{commit} and
	// compare by commit id, so a pinned tag converges after checkout.
	if g.Rev != "" {
		if revAtHead(ctx, g.cmd, g.Target, g.Rev, head) {
			return state.CheckResult{NeedsChange: false, Diff: fmt.Sprintf("%s is at rev %s", g.Target, g.Rev)}, nil
		}
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("%s needs checkout to rev %s", g.Target, g.Rev),
		}, nil
	}

	// Compare local HEAD against the remote tip without mutating the repo.
	ref := g.Branch
	if ref == "" {
		ref = "HEAD"
	}
	lsRes, err := g.git(ctx, "-C", g.Target, "ls-remote", "origin", ref)
	if err != nil {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("could not query remote tip for %s: %v", g.Target, err),
		}, nil
	}
	remote := firstField(lsRes.Stdout)
	if remote != "" && remote == head {
		return state.CheckResult{NeedsChange: false, Diff: fmt.Sprintf("%s is up to date", g.Target)}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%s is behind origin (HEAD %s)", g.Target, head),
	}, nil
}

func (g *GitLatest) Apply(ctx context.Context) (state.ApplyResult, error) {
	// Same absence discrimination as Check: a non-not-exist stat error must
	// fail the phase, not select the clone branch — see GitCloned.Apply.
	_, statErr := g.file.Stat(ctx, g.Target)
	if statErr != nil {
		if !errors.Is(statErr, fs.ErrNotExist) {
			return state.ApplyResult{}, fmt.Errorf("git.latest: stat %s: %w", g.Target, statErr)
		}
		return g.clone(ctx)
	}
	return g.update(ctx)
}

func (g *GitLatest) clone(ctx context.Context) (state.ApplyResult, error) {
	args := []string{"clone"}
	if g.Branch != "" {
		args = append(args, "--branch", g.Branch)
	}
	args = append(args, g.URL, g.Target)

	if _, err := g.git(ctx, args...); err != nil {
		return state.ApplyResult{}, fmt.Errorf("git.latest: clone %s: %w", g.URL, err)
	}
	g.createdByApply = true

	if g.Rev != "" {
		if _, err := g.git(ctx, "-C", g.Target, "checkout", g.Rev); err != nil {
			return state.ApplyResult{}, fmt.Errorf("git.latest: checkout %s: %w", g.Rev, err)
		}
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("cloned %s to %s", g.URL, g.Target),
		Details: map[string]string{"url": g.URL, "target": g.Target},
	}, nil
}

func (g *GitLatest) update(ctx context.Context) (state.ApplyResult, error) {
	// Fix the remote URL if it drifted.
	urlRes, _ := g.git(ctx, "-C", g.Target, "remote", "get-url", "origin")
	if urlRes == nil || strings.TrimSpace(urlRes.Stdout) != g.URL {
		if _, err := g.git(ctx, "-C", g.Target, "remote", "set-url", "origin", g.URL); err != nil {
			return state.ApplyResult{}, fmt.Errorf("git.latest: set-url %s: %w", g.URL, err)
		}
	}

	// Record the current HEAD so Revert can restore it.
	if headRes, err := g.git(ctx, "-C", g.Target, "rev-parse", "HEAD"); err == nil {
		g.prevHead = strings.TrimSpace(headRes.Stdout)
	}

	if _, err := g.git(ctx, "-C", g.Target, "fetch", "origin"); err != nil {
		return state.ApplyResult{}, fmt.Errorf("git.latest: fetch %s: %w", g.Target, err)
	}

	switch {
	case g.Force:
		ref := g.updateRef()
		if _, err := g.git(ctx, "-C", g.Target, "reset", "--hard", ref); err != nil {
			return state.ApplyResult{}, fmt.Errorf("git.latest: reset %s: %w", g.Target, err)
		}
	case g.Rev != "":
		if _, err := g.git(ctx, "-C", g.Target, "checkout", g.Rev); err != nil {
			return state.ApplyResult{}, fmt.Errorf("git.latest: checkout %s: %w", g.Rev, err)
		}
	case g.Branch != "":
		if _, err := g.git(ctx, "-C", g.Target, "checkout", g.Branch); err != nil {
			return state.ApplyResult{}, fmt.Errorf("git.latest: checkout %s: %w", g.Branch, err)
		}
		if _, err := g.git(ctx, "-C", g.Target, "merge", "--ff-only", "origin/"+g.Branch); err != nil {
			return state.ApplyResult{}, fmt.Errorf("git.latest: merge %s: %w", g.Branch, err)
		}
	default:
		if _, err := g.git(ctx, "-C", g.Target, "merge", "--ff-only"); err != nil {
			return state.ApplyResult{}, fmt.Errorf("git.latest: fast-forward %s: %w", g.Target, err)
		}
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("updated %s to latest", g.Target),
		Details: map[string]string{"url": g.URL, "target": g.Target},
	}, nil
}

// updateRef returns the ref used for a forced reset.
func (g *GitLatest) updateRef() string {
	switch {
	case g.Rev != "":
		return g.Rev
	case g.Branch != "":
		return "origin/" + g.Branch
	default:
		return "origin/HEAD"
	}
}

func (g *GitLatest) Revert(ctx context.Context) (state.ApplyResult, error) {
	if g.createdByApply {
		if err := g.file.RemoveAll(ctx, g.Target); err != nil {
			return state.ApplyResult{}, fmt.Errorf("git.latest: remove %s: %w", g.Target, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("removed cloned directory %s", g.Target),
		}, nil
	}

	if g.prevHead != "" {
		if _, err := g.git(ctx, "-C", g.Target, "reset", "--hard", g.prevHead); err != nil {
			return state.ApplyResult{}, fmt.Errorf("git.latest: revert reset %s: %w", g.Target, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("reset %s to %s", g.Target, g.prevHead),
		}, nil
	}

	return state.ApplyResult{
		Changed: false,
		Diff:    "git.latest: nothing to revert (no apply recorded in this run)",
	}, nil
}

// git runs a git subcommand and returns the result.
func (g *GitLatest) git(ctx context.Context, args ...string) (*exec.CommandResult, error) {
	return g.cmd.Run(ctx, exec.CommandOpts{Command: "git", Args: args})
}

// firstField returns the first whitespace-separated field of s, or "".
func firstField(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
