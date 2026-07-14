package gitmod

import (
	"context"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
)

// This file holds the rev-comparison helpers shared by git.cloned
// (git_cloned.go) and git.latest (git_latest.go) — resolving whether a
// declared rev/branch/tag denotes the commit currently checked out, without a
// network round-trip. They are deliberately module-agnostic.

// isHexRevPrefix reports whether rev could be an abbreviated commit sha:
// pure hex, at least 4 chars (git's minimum abbreviation), at most a full
// SHA-256 id.
func isHexRevPrefix(rev string) bool {
	if len(rev) < 4 || len(rev) > 64 {
		return false
	}
	for _, r := range rev {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

// isFullHexSHA reports whether s looks like a full commit id (40- or
// 64-hex-char sha).
func isFullHexSHA(s string) bool {
	return (len(s) == 40 || len(s) == 64) && isHexRevPrefix(s)
}

// resolveRevCommit resolves rev to a full commit id within the repo at path
// via `git rev-parse --verify <rev>^{commit}`, peeling annotated tags to the
// commits they point at. Local-only — no network round-trip. Returns ""
// when the rev does not resolve (e.g. a tag that has not been fetched yet:
// the caller reports NeedsChange and Apply's fetch+checkout converges it).
func resolveRevCommit(ctx context.Context, cmd exec.CommandExec, path, rev string) string {
	res, err := cmd.Run(ctx, exec.CommandOpts{
		Command: "git",
		Args:    []string{"-C", path, "rev-parse", "--verify", rev + "^{commit}"},
	})
	if err != nil || res == nil || res.ExitCode != 0 {
		return ""
	}
	out := strings.TrimSpace(res.Stdout)
	if !isFullHexSHA(out) {
		return ""
	}
	return out
}

// revAtHead reports whether the desired rev denotes the commit currently at
// head. Hex revs keep sha-prefix matching (no subprocess); anything else —
// a tag or other symbolic rev, whose name can never prefix-match a hex sha —
// is resolved locally and compared by commit id, so pinned tags CONVERGE
// after checkout instead of re-applying on every run.
func revAtHead(ctx context.Context, cmd exec.CommandExec, path, rev, head string) bool {
	if isHexRevPrefix(rev) && strings.HasPrefix(strings.ToLower(head), strings.ToLower(rev)) {
		return true
	}
	resolved := resolveRevCommit(ctx, cmd, path, rev)
	return resolved != "" && resolved == head
}
