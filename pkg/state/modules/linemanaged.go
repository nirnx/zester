package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// This file holds the helpers shared by the line-managed-file state modules —
// host.present/host.absent (/etc/hosts) and ssh_auth.present/ssh_auth.absent
// (authorized_keys) — plus archive.extracted's marker file. They are deliberately
// module-agnostic: reading a managed file safely, reverting one from a memoized
// backup, and the trailing-newline-preserving line split/join.

// readManagedFile reads a line-managed file, distinguishing a genuinely
// absent file from a failed read: fs.ErrNotExist is the ONLY absent signal
// (returned as "", false, nil); any other read error is returned to the
// caller so the phase FAILS instead of treating unreadable content as empty.
// Conflating the two would let a transient read failure (EIO, ESTALE, EACCES)
// followed by a successful write truncate /etc/hosts or authorized_keys down
// to just the managed line.
func readManagedFile(ctx context.Context, file exec.FileExec, path string) (content string, existed bool, err error) {
	data, err := file.ReadFile(ctx, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(data), true, nil
}

// revertHostsFile undoes a same-instance Apply on a line-managed file using
// the memos Apply recorded: remove the file when Apply created it, restore
// the backup (with the module's canonical permissions) when Apply captured
// one. When NO memo is set — Revert on a fresh instance where Apply never
// ran (the runner builds states fresh per execution, so ModeRevert always
// hits this) or Apply failed before reading — it is an explicit clean no-op:
// deleting or rewriting a shared file like /etc/hosts or authorized_keys to
// "revert" an unrecorded apply is never safe.
func revertHostsFile(ctx context.Context, file exec.FileExec, path string, backup []byte, backupSet, created bool, perm fs.FileMode, mod string) (state.ApplyResult, error) {
	switch {
	case created:
		if err := file.Remove(ctx, path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return state.ApplyResult{}, fmt.Errorf("%s: revert remove %s: %w", mod, path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("removed %s (revert; created by this apply)", path),
		}, nil
	case backupSet:
		if err := file.WriteFile(ctx, path, backup, perm); err != nil {
			return state.ApplyResult{}, fmt.Errorf("%s: revert %s: %w", mod, path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("reverted %s to previous content", path),
		}, nil
	default:
		return state.ApplyResult{
			Changed: false,
			Diff:    mod + ": nothing to revert (no apply recorded in this run)",
		}, nil
	}
}

// splitHostLines splits content into lines, dropping a single trailing newline.
func splitHostLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

// joinHostLines joins lines with newlines and ensures a trailing newline.
func joinHostLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// dropString returns ss with all occurrences of s removed.
func dropString(ss []string, s string) []string {
	out := make([]string, 0, len(ss))
	for _, v := range ss {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}
