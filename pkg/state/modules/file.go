// Package modules provides built-in state modules for Zester.
package modules

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os/user"
	"strconv"

	"github.com/nirnx/zester/pkg/exec"
)

// This file holds the helpers shared across the file.* state modules
// (file.managed lives in file_managed.go; file.directory, file.recurse, and
// file.copy reuse these). Keeping them here rather than in any one module's
// file avoids a cross-file ownership question as more file modules migrate.

// resolveOwnerIDs resolves declared user/group names to numeric ids, the same
// way the Apply-side ownership setters do. An undeclared (empty) name maps to
// -1 ("don't care", matching os.Chown semantics).
func resolveOwnerIDs(module, userName, groupName string) (uid, gid int, err error) {
	uid, gid = -1, -1

	if userName != "" {
		u, err := user.Lookup(userName)
		if err != nil {
			return -1, -1, fmt.Errorf("%s: lookup user %q: %w", module, userName, err)
		}
		uid, _ = strconv.Atoi(u.Uid)
	}

	if groupName != "" {
		g, err := user.LookupGroup(groupName)
		if err != nil {
			return -1, -1, fmt.Errorf("%s: lookup group %q: %w", module, groupName, err)
		}
		gid, _ = strconv.Atoi(g.Gid)
	}

	return uid, gid, nil
}

// checkOwnershipDrift compares path's current ownership against the declared
// user/group names. The facet fires only for declared config: with neither
// user nor group declared it never reports drift, and with only one declared
// only that half is compared.
func checkOwnershipDrift(ctx context.Context, file exec.FileExec, module, path, userName, groupName string) (diff string, drift bool, err error) {
	if userName == "" && groupName == "" {
		return "", false, nil
	}

	wantUID, wantGID, err := resolveOwnerIDs(module, userName, groupName)
	if err != nil {
		return "", false, err
	}

	uid, gid, err := file.Owner(ctx, path)
	if err != nil {
		return "", false, fmt.Errorf("%s: owner %s: %w", module, path, err)
	}

	if wantUID != -1 && uid != wantUID {
		return fmt.Sprintf("owner uid %d != %d (%s) for %s", uid, wantUID, userName, path), true, nil
	}
	if wantGID != -1 && gid != wantGID {
		return fmt.Sprintf("group gid %d != %d (%s) for %s", gid, wantGID, groupName, path), true, nil
	}
	return "", false, nil
}

// modeConfigToString converts a YAML-parsed mode value to a string.
// YAML parsers interpret "0750" (leading zero) as an octal integer,
// so mode may arrive as int (488) instead of string ("0750"). Used by
// file.directory and file.recurse, which still parse their mode parameters
// through the legacy string path (file.managed now decodes mode through the
// paramtypes.FileMode semantic type instead).
func modeConfigToString(v any) string {
	switch m := v.(type) {
	case string:
		return m
	case int:
		return fmt.Sprintf("%04o", m)
	case int64:
		return fmt.Sprintf("%04o", m)
	case float64:
		return fmt.Sprintf("%04o", int(m))
	}
	return ""
}

func hashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}
