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

// GitCloned implements the git.cloned state.
// It ensures a Git repository is cloned at a target path and optionally
// pinned to a specific branch or revision.
//
// GitCloned is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module) — all
// five are primitives (three strings, an int, and a bool), no semantic types
// are needed. `name`'s PRIMARY MEANING is the clone PATH (contrast with
// git.latest, whose primary is the remote URL — see the Notes section below,
// unmissable in both generated pages). `url` is `required`; `depth` is a
// plain int, so a msgpack-delivered sized integer (a reactor dispatch encodes
// `depth: 2` as an int8) is now honored where the legacy `config["depth"].
// (int)` assertion dropped every non-int kind to 0 (BD-1). Under the uniform
// decoder a numeric `name`/`url`/`branch`/`rev` coerces to its string form
// and a composite is rejected (BD-6); `force` honors an integer 1/0 (BD-7)
// and a truthy/falsy string (BD-2) where the legacy `.(bool)` assertion
// silently dropped them. The unexported runtime fields (id, reqs, cmd, file,
// revert memo) are untagged, so the schema compiler skips them.
type GitCloned struct {
	id   string
	reqs state.Requisites

	// Path is the filesystem path where the repo should be cloned; it
	// defaults to the state ID.
	Path string `zester:"name,primary" usage:"filesystem path where the repo should be cloned; defaults to the state ID (contrast with git.latest, whose name/primary is the remote URL)"`

	// URL is the remote repository URL (required).
	URL string `zester:"url,required" usage:"remote repository URL; required"`

	// Branch is an optional branch or tag to checkout.
	Branch string `zester:"branch" usage:"branch or tag to checkout after cloning"`

	// Rev is an optional revision to checkout: a commit sha (full or
	// abbreviated), tag, or any rev git can resolve locally.
	Rev string `zester:"rev" usage:"specific commit sha (full or abbreviated), tag, or any locally-resolvable rev to check out; takes precedence over branch for comparison"`

	// Depth is the shallow clone depth (0 = full clone).
	Depth int `zester:"depth" usage:"shallow clone depth passed to git clone --depth; 0 (the default) means a full clone"`

	// Force resets local changes before checkout.
	Force bool `zester:"force" usage:"reset local changes (git reset --hard HEAD) before checkout; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	cmd  exec.CommandExec
	file exec.FileExec

	// createdByApply tracks whether a same-instance Apply cloned the
	// directory, so Revert knows it is safe to remove it. Valid only for a
	// same-instance Apply→Revert sequence; a fresh instance (any runner
	// ModeRevert run — states are rebuilt per execution) leaves it false and
	// Revert is an explicit clean no-op.
	createdByApply bool
}

// gitClonedSpec is the compiled schema + documentation for git.cloned. It is
// compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is drift-corrected against
// the live Check/Apply/Revert behavior — notably that a pinned tag/symbolic
// rev DOES converge (resolved locally by commit id), not just a hex sha
// prefix.
var gitClonedSpec = mustSpec("git.cloned", modschema.KindState, GitCloned{}, modschema.Doc{
	Summary: "Ensure a Git repository is cloned at a target path, optionally pinned to a branch or revision.",
	Description: "`git.cloned` ensures a Git repository is present at `name` (the clone PATH, defaulting " +
		"to the state ID — NOTE: this is the opposite of `git.latest`, whose `name`/primary is the remote " +
		"URL). `url` is required. `branch` or `rev` optionally pins the checkout; `rev` takes precedence " +
		"when both are set. `depth` shallow-clones (0 is a full clone) and `force` discards local changes " +
		"before checkout. Unlike `git.latest`, an existing clone is never fetched/updated unless a " +
		"declared `rev`/`branch` requires a different checkout — `git.cloned` only ensures presence at a " +
		"revision, it does not track a moving branch tip.",
	Effects: modschema.Effects{
		Check: "Reports a change when the target directory does not exist (any stat error OTHER than " +
			"not-exist fails the phase, rather than risking a blind clone over an unreadable existing " +
			"checkout). Otherwise compares `git -C <path> remote get-url origin` against `url`; a mismatch " +
			"needs a change. With `rev` or `branch` declared, also compares HEAD: a hex `rev` matches by " +
			"sha PREFIX (no subprocess); a tag or other symbolic rev/branch is resolved LOCALLY (`git " +
			"rev-parse --verify <rev>^{commit}`, no network) and compared by commit id, so a checked-out " +
			"tag CONVERGES rather than perpetually reporting a change. `branch` also documents tags: " +
			"`git clone --branch <tag>` leaves a detached HEAD with no local branch, so branch comparison " +
			"falls back to the same symbolic-rev resolution when no matching local branch ref exists.",
		Apply: "Clones with `git clone [--depth N] [--branch B] <url> <path>` when the directory is " +
			"missing; otherwise corrects the remote URL with `git remote set-url origin <url>` if it " +
			"drifted (same not-exist discrimination as Check). When `rev` or `branch` is set: optionally " +
			"`git reset --hard HEAD` first when `force` is set, then `git fetch origin` and `git checkout " +
			"<rev-or-branch>`. Reports the url and path in its details.",
		Revert: "Removes the directory with `RemoveAll` ONLY if this run's Apply created it (a same-" +
			"instance memo). If the directory existed before Apply (for example only the remote URL was " +
			"fixed), Revert is an explicit clean no-op — it never deletes a pre-existing checkout.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Clone a public repo with no pinned version",
			Kind:        "state",
			Explanation: "name is the clone path (defaults to the state ID); url is required.",
			Code:        "/opt/myapp:\n  git.cloned:\n    - url: https://github.com/example/myapp\n",
		},
		{
			Title:       "Clone a specific branch with shallow depth",
			Kind:        "state",
			Explanation: "branch checks out a branch/tag; depth shallow-clones.",
			Code: "/srv/deploy/myapp:\n  git.cloned:\n    - url: git@github.com:example/myapp.git\n" +
				"    - branch: production\n    - depth: 1\n",
		},
		{
			Title:       "Pin to a specific commit",
			Kind:        "state",
			Explanation: "rev takes precedence over branch for the HEAD comparison; require orders git installation first.",
			Code: "/opt/tools/mylib:\n  git.cloned:\n    - url: https://github.com/example/mylib\n" +
				"    - rev: a1b2c3d4e5f6\n    - require:\n      - pkg.installed:git\n",
		},
		{
			Title:       "Clone a repo ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the clone PATH; url is a key=value.",
			Code:        "zester '*' git.cloned /opt/myapp url=https://github.com/example/myapp",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "warn",
			Title: "name means the PATH here — the opposite of git.latest",
			Body: "`git.cloned`'s `name` (and state-ID default) is the CLONE PATH. `git.latest`'s `name` " +
				"is instead the remote URL, and its clone path is the separate `target` parameter. Mixing " +
				"the two up under a shared naming assumption is the most common authoring mistake between " +
				"these two modules.",
		},
		{
			Level: "info",
			Title: "rev and branch: rev takes precedence, both resolve tags locally",
			Body: "`rev` and `branch` can both be set; `rev` takes precedence when comparing whether HEAD " +
				"matches. A hex-looking rev matches by sha PREFIX; a tag name or other symbolic rev/branch " +
				"is resolved with a local, network-free `git rev-parse --verify <rev>^{commit}` and " +
				"compared by commit id, so a pinned tag converges after checkout instead of re-applying " +
				"every run.",
		},
		{
			Level: "info",
			Title: "git must be installed; auth is ambient",
			Body: "The state shells out to the `git` binary via the command execution provider; `git` " +
				"must be installed on the target node. TLS verification and SSH key configuration are " +
				"handled by the system Git config, not by this module.",
		},
	},
	Divergences: []string{"BD-1", "BD-2", "BD-6", "BD-7"},
	SeeAlso:     []string{"git.latest"},
})

// NewGitClonedBuilder returns a state.Builder that creates GitCloned states
// using the given ModuleContext's command and file providers. Decode policy
// (unknown-key handling, reserved keys) is threaded via opts; the peel
// supplies it through modules.RegisterAll.
func NewGitClonedBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("git.cloned: no command provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected providers, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		g := &GitCloned{}
		if _, err := gitClonedSpec.Decode(id, config, g, opts); err != nil {
			return nil, fmt.Errorf("git.cloned: %w", err)
		}
		g.id = id
		g.cmd = mctx.Command
		g.file = mctx.File
		g.reqs = state.ParseRequisites(config)
		return g, nil
	}
}

func (g *GitCloned) Name() string           { return "git.cloned:" + g.id }
func (g *GitCloned) Reqs() state.Requisites { return g.reqs }

func (g *GitCloned) Check(ctx context.Context) (state.CheckResult, error) {
	// Only fs.ErrNotExist means absent; any other stat error (EACCES, EIO,
	// ESTALE) fails the phase — reporting a bogus pending clone for an
	// unreadable existing checkout would misdiagnose the real failure.
	if _, err := g.file.Stat(ctx, g.Path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return state.CheckResult{}, fmt.Errorf("git.cloned: stat %s: %w", g.Path, err)
		}
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("directory %s does not exist", g.Path),
		}, nil
	}

	// Directory exists — check the remote URL.
	result, err := g.cmd.Run(ctx, exec.CommandOpts{
		Command: "git",
		Args:    []string{"-C", g.Path, "remote", "get-url", "origin"},
	})
	if err != nil {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("could not get remote URL for %s: %v", g.Path, err),
		}, nil
	}

	currentURL := strings.TrimSpace(result.Stdout)
	if currentURL != g.URL {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("remote URL mismatch: got %q, want %q", currentURL, g.URL),
		}, nil
	}

	// If a specific rev/branch is requested, compare HEAD.
	desired := g.Rev
	if desired == "" {
		desired = g.Branch
	}
	if desired == "" {
		return state.CheckResult{
			NeedsChange: false,
			Diff:        fmt.Sprintf("%s is cloned from %s", g.Path, g.URL),
		}, nil
	}

	headResult, err := g.cmd.Run(ctx, exec.CommandOpts{
		Command: "git",
		Args:    []string{"-C", g.Path, "rev-parse", "HEAD"},
	})
	if err != nil {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("could not get HEAD for %s: %v", g.Path, err),
		}, nil
	}
	head := strings.TrimSpace(headResult.Stdout)

	if g.Rev != "" {
		// Sha revs match by prefix; symbolic revs (tags, refs) resolve
		// locally and compare by commit id — a checked-out tag converges
		// instead of perpetually re-applying.
		if revAtHead(ctx, g.cmd, g.Path, g.Rev, head) {
			return state.CheckResult{
				NeedsChange: false,
				Diff:        fmt.Sprintf("%s is at rev %s", g.Path, g.Rev),
			}, nil
		}
	} else {
		// Branch comparison: resolve the desired branch ref. branch: also
		// documents tags — `git clone --branch <tag>` leaves a detached HEAD
		// with no local branch, so fall back to symbolic-rev resolution.
		refResult, refErr := g.cmd.Run(ctx, exec.CommandOpts{
			Command: "git",
			Args:    []string{"-C", g.Path, "rev-parse", "refs/heads/" + g.Branch},
		})
		if refErr == nil && refResult != nil && refResult.ExitCode == 0 &&
			strings.TrimSpace(refResult.Stdout) == head {
			return state.CheckResult{
				NeedsChange: false,
				Diff:        fmt.Sprintf("%s is on branch %s", g.Path, g.Branch),
			}, nil
		}
		if revAtHead(ctx, g.cmd, g.Path, g.Branch, head) {
			return state.CheckResult{
				NeedsChange: false,
				Diff:        fmt.Sprintf("%s is at %s", g.Path, g.Branch),
			}, nil
		}
	}

	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%s needs checkout to %s (HEAD: %s)", g.Path, desired, head),
	}, nil
}

func (g *GitCloned) Apply(ctx context.Context) (state.ApplyResult, error) {
	// Same absence discrimination as Check: a non-not-exist stat error must
	// fail the phase, not select the clone branch — cloning over an existing
	// checkout that merely failed to stat either errors misleadingly or, for
	// an empty pre-created dir, re-clones it and mis-arms createdByApply.
	_, statErr := g.file.Stat(ctx, g.Path)
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return state.ApplyResult{}, fmt.Errorf("git.cloned: stat %s: %w", g.Path, statErr)
	}
	dirMissing := statErr != nil

	if dirMissing {
		// Clone the repository.
		args := []string{"clone"}
		if g.Depth > 0 {
			args = append(args, "--depth", fmt.Sprintf("%d", g.Depth))
		}
		if g.Branch != "" {
			args = append(args, "--branch", g.Branch)
		}
		args = append(args, g.URL, g.Path)

		if _, err := g.cmd.Run(ctx, exec.CommandOpts{Command: "git", Args: args}); err != nil {
			return state.ApplyResult{}, fmt.Errorf("git.cloned: clone %s: %w", g.URL, err)
		}
		g.createdByApply = true
	} else {
		// Directory exists — fix remote URL if needed.
		result, _ := g.cmd.Run(ctx, exec.CommandOpts{
			Command: "git",
			Args:    []string{"-C", g.Path, "remote", "get-url", "origin"},
		})
		if result == nil || strings.TrimSpace(result.Stdout) != g.URL {
			if _, err := g.cmd.Run(ctx, exec.CommandOpts{
				Command: "git",
				Args:    []string{"-C", g.Path, "remote", "set-url", "origin", g.URL},
			}); err != nil {
				return state.ApplyResult{}, fmt.Errorf("git.cloned: set-url %s: %w", g.URL, err)
			}
		}
	}

	// Checkout branch or rev if specified.
	target := g.Rev
	if target == "" {
		target = g.Branch
	}
	if target != "" {
		if g.Force {
			if _, err := g.cmd.Run(ctx, exec.CommandOpts{
				Command: "git",
				Args:    []string{"-C", g.Path, "reset", "--hard", "HEAD"},
			}); err != nil {
				return state.ApplyResult{}, fmt.Errorf("git.cloned: reset %s: %w", g.Path, err)
			}
		}
		if _, err := g.cmd.Run(ctx, exec.CommandOpts{
			Command: "git",
			Args:    []string{"-C", g.Path, "fetch", "origin"},
		}); err != nil {
			return state.ApplyResult{}, fmt.Errorf("git.cloned: fetch %s: %w", g.Path, err)
		}
		if _, err := g.cmd.Run(ctx, exec.CommandOpts{
			Command: "git",
			Args:    []string{"-C", g.Path, "checkout", target},
		}); err != nil {
			return state.ApplyResult{}, fmt.Errorf("git.cloned: checkout %s: %w", target, err)
		}
	}

	diff := fmt.Sprintf("cloned %s to %s", g.URL, g.Path)
	if target != "" {
		diff = fmt.Sprintf("cloned %s to %s and checked out %s", g.URL, g.Path, target)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    diff,
		Details: map[string]string{
			"path": g.Path,
			"url":  g.URL,
		},
	}, nil
}

func (g *GitCloned) Revert(ctx context.Context) (state.ApplyResult, error) {
	if !g.createdByApply {
		return state.ApplyResult{
			Changed: false,
			Diff:    "git.cloned: nothing to revert (no clone recorded in this run)",
		}, nil
	}

	if err := g.file.RemoveAll(ctx, g.Path); err != nil {
		return state.ApplyResult{}, fmt.Errorf("git.cloned: remove %s: %w", g.Path, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed cloned directory %s", g.Path),
	}, nil
}
