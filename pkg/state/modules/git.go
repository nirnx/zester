package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// GitCloned implements the git.cloned state.
// It ensures a Git repository is cloned at a target path and optionally
// pinned to a specific branch or revision.
type GitCloned struct {
	id   string
	reqs state.Requisites

	// Path is the filesystem path where the repo should be cloned.
	Path string

	// URL is the remote repository URL (required).
	URL string

	// Branch is an optional branch or tag to checkout.
	Branch string

	// Rev is an optional specific commit hash to checkout.
	Rev string

	// Depth is the shallow clone depth (0 = full clone).
	Depth int

	// Force resets local changes before checkout.
	Force bool

	cmd  exec.CommandExec
	file exec.FileExec

	// createdByApply tracks whether Apply created the directory,
	// so Revert knows whether it's safe to remove it.
	createdByApply bool
}

// NewGitClonedBuilder returns a state.Builder that creates GitCloned states
// using the given ModuleContext's command and file providers.
func NewGitClonedBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("git.cloned: no command provider available")
		}
		return newGitCloned(id, config, mctx.Command, mctx.File)
	}
}

func newGitCloned(id string, config map[string]any, cmd exec.CommandExec, file exec.FileExec) (state.State, error) {
	g := &GitCloned{id: id, cmd: cmd, file: file}

	g.Path, _ = config["name"].(string)
	if g.Path == "" {
		g.Path = id
	}

	g.URL, _ = config["url"].(string)
	if g.URL == "" {
		return nil, fmt.Errorf("git.cloned: %s: url is required", id)
	}

	g.Branch, _ = config["branch"].(string)
	g.Rev, _ = config["rev"].(string)

	if depth, ok := config["depth"].(int); ok {
		g.Depth = depth
	}
	g.Force, _ = config["force"].(bool)

	g.reqs = state.ParseRequisites(config)
	return g, nil
}

func (g *GitCloned) Name() string           { return "git.cloned:" + g.id }
func (g *GitCloned) Reqs() state.Requisites { return g.reqs }

func (g *GitCloned) Check(ctx context.Context) (state.CheckResult, error) {
	_, err := g.file.Stat(ctx, g.Path)
	if err != nil {
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

	// For a full SHA, compare directly; otherwise the checkout is needed.
	if g.Rev != "" && strings.HasPrefix(head, g.Rev) {
		return state.CheckResult{
			NeedsChange: false,
			Diff:        fmt.Sprintf("%s is at rev %s", g.Path, g.Rev),
		}, nil
	}

	if g.Rev == "" {
		// Branch comparison: resolve the desired branch ref.
		refResult, refErr := g.cmd.Run(ctx, exec.CommandOpts{
			Command: "git",
			Args:    []string{"-C", g.Path, "rev-parse", "refs/heads/" + g.Branch},
		})
		if refErr == nil && strings.TrimSpace(refResult.Stdout) == head {
			return state.CheckResult{
				NeedsChange: false,
				Diff:        fmt.Sprintf("%s is on branch %s", g.Path, g.Branch),
			}, nil
		}
	}

	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%s needs checkout to %s (HEAD: %s)", g.Path, desired, head),
	}, nil
}

func (g *GitCloned) Apply(ctx context.Context) (state.ApplyResult, error) {
	_, statErr := g.file.Stat(ctx, g.Path)
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
			Diff:    "git.cloned: directory was not created by this apply; skipping removal",
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
