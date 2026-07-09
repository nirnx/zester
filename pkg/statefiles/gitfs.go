package statefiles

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// GitFSConfig configures the Git-based state file syncer.
type GitFSConfig struct {
	// Remotes is a list of Git repository URLs to clone/pull.
	Remotes []string

	// StatesDir is the root directory where repos are cloned into.
	StatesDir string

	// Interval is the pull frequency. Defaults to 5 minutes.
	Interval time.Duration

	// SSHKeyPath is an optional path to an SSH private key for Git auth.
	SSHKeyPath string

	// Logger is the structured logger. Defaults to slog.Default().
	Logger *slog.Logger

	// OnSyncSuccess, if set, is invoked after each sync cycle in which
	// every remote pulled successfully AND the state files were published.
	// Optional health/metric hook: nil means no-op. Used by the master to
	// track last-successful-sync age for its readiness check.
	OnSyncSuccess func()
}

// GitFS clones and periodically pulls Git repositories containing state files,
// then publishes them to the state-files KV bucket via a Publisher.
type GitFS struct {
	cfg       GitFSConfig
	publisher *Publisher
}

// NewGitFS creates a Git-based state file syncer.
func NewGitFS(cfg GitFSConfig, publisher *Publisher) *GitFS {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Interval == 0 {
		cfg.Interval = 5 * time.Minute
	}
	return &GitFS{
		cfg:       cfg,
		publisher: publisher,
	}
}

// Run performs an initial clone/pull of all remotes, publishes state files,
// then enters a periodic pull loop. Blocks until ctx is cancelled.
func (g *GitFS) Run(ctx context.Context) error {
	if g.syncOnce(ctx, true) && g.cfg.OnSyncSuccess != nil {
		g.cfg.OnSyncSuccess()
	}

	// Periodic pull loop. Sync/publish errors are logged and retried on
	// the next tick; they never terminate the loop.
	ticker := time.NewTicker(g.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if g.syncOnce(ctx, false) && g.cfg.OnSyncSuccess != nil {
				g.cfg.OnSyncSuccess()
			}
		}
	}
}

// syncOnce pulls all remotes and republishes state files (which rewrites the
// _manifest and garbage-collects stale KV keys). Publishing is skipped when a
// failed remote has no VALID local clone: publishing without it would emit a
// manifest missing that repo's files and propagate their deletion fleet-wide.
// A stale-but-valid clone, by contrast, is safe to publish — clones are
// staged in a temp dir and renamed into place (cloneFresh), so a valid repo
// directory is always the product of a fully successful sync (last known
// good). Directory presence alone is NOT enough — an interrupted/partial
// clone leaves a directory without the repo's files. Returns true only when
// every remote pulled AND the publish succeeded.
func (g *GitFS) syncOnce(ctx context.Context, initial bool) bool {
	ok := true
	publishSafe := true
	for _, remote := range g.cfg.Remotes {
		if err := g.cloneOrPull(ctx, remote); err != nil {
			g.cfg.Logger.Error("git sync failed", "remote", remote, "error", err, "initial", initial)
			ok = false
			repoDir := filepath.Join(g.cfg.StatesDir, RepoName(remote))
			if !g.validRepo(ctx, repoDir) {
				publishSafe = false
				g.cfg.Logger.Error("remote has no valid last-known-good clone, skipping publish to avoid propagating deletions",
					"remote", remote, "dir", repoDir)
			}
		}
	}
	if !publishSafe {
		return false
	}

	res, err := g.publisher.Publish(ctx)
	if err != nil {
		g.cfg.Logger.Error("publish failed", "error", err, "initial", initial)
		return false
	}
	switch {
	case initial:
		g.cfg.Logger.Info("initial state files published", "count", res.Files, "changed", res.Changed)
	case res.Changed:
		g.cfg.Logger.Info("state files republished", "count", res.Files)
	default:
		g.cfg.Logger.Debug("state files unchanged, publish skipped", "count", res.Files)
	}
	return ok
}

// cloneOrPull pulls when a VALID local clone exists, and clones from scratch
// otherwise. A directory that exists but is not a valid git repository (e.g.
// a clone interrupted by process kill or lease loss) is removed and re-cloned
// — pulling it would fail forever and never self-heal.
func (g *GitFS) cloneOrPull(ctx context.Context, remote string) error {
	repoName := RepoName(remote)
	targetDir := filepath.Join(g.cfg.StatesDir, repoName)

	if _, err := os.Stat(targetDir); err == nil {
		if g.validRepo(ctx, targetDir) {
			g.cfg.Logger.Debug("pulling", "remote", remote, "dir", targetDir)
			return g.gitCommand(ctx, "-C", targetDir, "pull", "--ff-only")
		}
		g.cfg.Logger.Warn("existing directory is not a valid git repository, re-cloning from scratch",
			"remote", remote, "dir", targetDir)
		if err := os.RemoveAll(targetDir); err != nil {
			return fmt.Errorf("statefiles: remove invalid repo dir %s: %w", targetDir, err)
		}
	}

	return g.cloneFresh(ctx, remote, repoName, targetDir)
}

// cloneFresh clones the remote into a temporary sibling directory and renames
// it into place, so an interrupted clone never leaves a half-populated
// targetDir (the temp dir is hidden — dot-prefixed — so the publisher's walk
// skips it, and stale temp dirs from earlier interrupted clones are removed
// first).
func (g *GitFS) cloneFresh(ctx context.Context, remote, repoName, targetDir string) error {
	if err := os.MkdirAll(g.cfg.StatesDir, 0o755); err != nil {
		return fmt.Errorf("statefiles: create states dir %s: %w", g.cfg.StatesDir, err)
	}

	// Sweep temp dirs abandoned by interrupted clones of this repo.
	if leftovers, err := filepath.Glob(filepath.Join(g.cfg.StatesDir, cloneTmpPrefix(repoName)+"*")); err == nil {
		for _, dir := range leftovers {
			if err := os.RemoveAll(dir); err != nil {
				g.cfg.Logger.Warn("failed to remove stale clone temp dir", "dir", dir, "error", err)
			}
		}
	}

	tmpDir, err := os.MkdirTemp(g.cfg.StatesDir, cloneTmpPrefix(repoName))
	if err != nil {
		return fmt.Errorf("statefiles: create clone temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck // no-op after successful rename

	g.cfg.Logger.Info("cloning", "remote", remote, "dir", targetDir)
	if err := g.gitCommand(ctx, "clone", "--depth", "1", remote, tmpDir); err != nil {
		return err
	}
	if err := os.Rename(tmpDir, targetDir); err != nil {
		return fmt.Errorf("statefiles: move cloned repo into place at %s: %w", targetDir, err)
	}
	return nil
}

// cloneTmpPrefix is the hidden temp-dir name prefix used for in-progress
// clones of the given repo. Dot-prefixed so loadStateFiles' walk skips it.
func cloneTmpPrefix(repoName string) string {
	return "." + repoName + ".clone-"
}

// validRepo reports whether dir is a working git repository: it must carry
// its own .git entry (rev-parse alone would walk up to an enclosing repo and
// false-positive) and rev-parse must resolve it. A merely-present directory
// left behind by an interrupted clone fails this check.
//
// The check deliberately runs under context.Background(): it is a fast,
// purely local git invocation, and it must stay accurate even when the sync
// context was just cancelled (a cancelled pull would otherwise make a
// perfectly valid last-known-good clone look broken).
func (g *GitFS) validRepo(_ context.Context, dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	return g.gitCommand(context.Background(), "-C", dir, "rev-parse", "--git-dir") == nil
}

// gitCommand runs a git command with optional SSH key configuration.
func (g *GitFS) gitCommand(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if g.cfg.SSHKeyPath != "" {
		cmd.Env = append(os.Environ(),
			fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o StrictHostKeyChecking=accept-new", g.cfg.SSHKeyPath),
		)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("statefiles: git %s: %s: %w", args[0], strings.TrimSpace(string(output)), err)
	}
	return nil
}

// RepoName extracts the repository name from a Git remote URL.
// Handles SSH-style (git@host:org/repo.git) and HTTPS URLs.
func RepoName(remote string) string {
	// Handle SSH-style URLs: git@github.com:org/repo.git
	if idx := strings.Index(remote, ":"); idx != -1 && !strings.Contains(remote, "://") {
		remote = remote[idx+1:]
	}

	name := path.Base(remote)
	name = strings.TrimSuffix(name, ".git")
	return name
}
