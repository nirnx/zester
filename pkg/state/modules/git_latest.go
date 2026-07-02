package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
)

// GitLatest implements the git.latest state.
// Like git.cloned, it ensures a repository is present at a target directory,
// but it also updates an existing clone to the latest commit on the tracked
// branch/rev (fetch + fast-forward, or reset when force is set).
type GitLatest struct {
	id   string
	reqs state.Requisites

	// URL is the remote repository URL (the state name).
	URL string

	// Target is the filesystem path the repo lives in (required).
	Target string

	// Branch is an optional branch to track.
	Branch string

	// Rev is an optional specific commit/ref to check out.
	Rev string

	// Force resets local changes (git reset --hard) instead of fast-forward.
	Force bool

	cmd  exec.CommandExec
	file exec.FileExec

	// createdByApply tracks whether Apply cloned the repo, so Revert can
	// safely remove the directory.
	createdByApply bool

	// prevHead stores the pre-update HEAD sha for an existing clone so Revert
	// can reset back to it.
	prevHead string
}

// NewGitLatestBuilder returns a state.Builder that creates GitLatest states.
func NewGitLatestBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("git.latest: no command provider available")
		}
		if mctx.File == nil {
			return nil, fmt.Errorf("git.latest: no file provider available")
		}
		return newGitLatest(id, config, mctx.Command, mctx.File)
	}
}

func newGitLatest(id string, config map[string]any, cmd exec.CommandExec, file exec.FileExec) (state.State, error) {
	g := &GitLatest{id: id, cmd: cmd, file: file}

	g.URL, _ = config["name"].(string)
	if g.URL == "" {
		g.URL = id
	}
	if g.URL == "" {
		return nil, fmt.Errorf("git.latest: %s: url (name) is required", id)
	}

	g.Target, _ = config["target"].(string)
	if g.Target == "" {
		return nil, fmt.Errorf("git.latest: %s: target is required", id)
	}

	g.Branch, _ = config["branch"].(string)
	g.Rev, _ = config["rev"].(string)
	g.Force, _ = config["force"].(bool)

	g.reqs = state.ParseRequisites(config)
	return g, nil
}

func (g *GitLatest) Name() string           { return "git.latest:" + g.id }
func (g *GitLatest) Reqs() state.Requisites { return g.reqs }

func (g *GitLatest) Check(ctx context.Context) (state.CheckResult, error) {
	if _, err := g.file.Stat(ctx, g.Target); err != nil {
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

	// Pinned rev: compare directly, no network needed.
	if g.Rev != "" {
		if strings.HasPrefix(head, g.Rev) {
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
	_, statErr := g.file.Stat(ctx, g.Target)
	if statErr != nil {
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
		Diff:    "git.latest: nothing to revert",
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
