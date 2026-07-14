package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
)

// family_file.go — the file.* FAMILY PARAMETER COMPONENTS and canonical
// runtime contracts (keystone spec §13, Amendment A1).
//
// A component is the single declaration of a family-canonical parameter
// contract: name, value type, alias set, canonical usage semantics, and the
// runtime behavioral contract are FIXED here; the default and requiredness
// dimensions are member-supplied ONLY where a component explicitly declares
// them so (`memberdefault`/`memberrequired` — the member then passes
// modschema.WithDefault/WithRequired to mustSpec, mandatorily). Members embed
// these structs; a private redeclaration of a componentized key is a
// vocabulary-gate failure.
//
// Components are strictly scoped to the file.* family — no other family may
// embed them (§13: no cross-family components, ever; the same spelling in
// another family is a different word that gets its own component there).
//
// NOT componentized, deliberately:
//   - the primary (`name`): its meaning IS each member's target — genuinely
//     module-specific, contextual usage by design;
//   - `dir_mode`: member-specific meanings (file.directory: fallback source
//     for the managed directory's mode; file.recurse: creation mode for new
//     directories) that share one contract signature — gate-consistent as
//     member declarations;
//   - keys whose contract already agrees and whose meaning is per-operation
//     (`content`, `force`, `append_if_not_found`, …): consistent today,
//     gate-guarded, and their usage is contextual prose.

// fileMakeDirsParam is the file.* family's canonical `makedirs` component.
//
// CANONICAL RUNTIME CONTRACT (§13, maintainer-approved): `makedirs` controls
// creation of missing PARENT directories of the operation's target — never
// the target itself. When false (the default), a missing parent fails BOTH
// Check and Apply with an error naming the parent and the remedy, and nothing
// is partially created. When true, missing parents are created (mode 0755)
// before the operation; existing parents are never modified. Revert never
// removes parent directories that makedirs created. Every embedding member
// must pass the shared four-case behavior suite
// (TestFileFamily_MakeDirsContract).
type fileMakeDirsParam struct {
	MakeDirs bool `zester:"makedirs,default=false" usage:"create missing parent directories of the target (mode 0755); when false, a missing parent fails the operation; never creates the target itself; a boolean that also accepts the integers 1 (true) and 0 (false)"`
}

// fileModeParam is the file.* family's canonical `mode` component: the
// target's permission mode as a FileMode (octal string or integer), lazy,
// with a MEMBER-SUPPLIED default (file.managed supplies 0644, file.directory
// 0755 — modschema.WithDefault("mode", …) is mandatory for embedders).
// file.line's action-selector `mode` is a pinned in-family compatibility
// exception, explicitly OUTSIDE this contract.
type fileModeParam struct {
	Mode paramtypes.FileMode `zester:"mode,lazy,memberdefault" usage:"permission mode for the target in octal (\"0644\", \"0755\", \"4755\"); setuid/setgid/sticky bits are honored; the default is member-specific"`
}

// fileOwnershipParam is the file.* family's canonical ownership component:
// `user` and `group` name the target's owner; ownership is left unchanged
// when unset.
type fileOwnershipParam struct {
	User  string `zester:"user" usage:"owner username for the target; ownership is left unchanged when unset"`
	Group string `zester:"group" usage:"group name for the target; ownership is left unchanged when unset"`
}

// fileSourceParam is the file.* family's canonical `source` component: the
// path content is taken from. Requiredness is MEMBER-SUPPLIED
// (file.copy requires it; file.managed and file.recurse do not —
// modschema.WithRequired("source", …) is mandatory for embedders).
type fileSourceParam struct {
	Source string `zester:"source,memberrequired" usage:"source path content is taken from; whether it is required is member-specific"`
}

// fileParentMode is the fixed mode for parent directories makedirs creates.
const fileParentMode fs.FileMode = 0o755

// requireParentDirs is the CHECK-side arm of the canonical makedirs contract:
// it FAILS (never creates) when makedirs is false and the target's parent is
// missing. With makedirs true it is a no-op — creation happens in Apply.
func requireParentDirs(ctx context.Context, file exec.FileExec, module, target string, makedirs bool) error {
	if makedirs {
		return nil
	}
	parent := filepath.Dir(target)
	if parent == "" || parent == "." || parent == "/" {
		return nil
	}
	if _, err := file.Stat(ctx, parent); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s: parent directory %s does not exist (set makedirs: true to create it)", module, parent)
		}
		return fmt.Errorf("%s: stat parent %s: %w", module, parent, err)
	}
	return nil
}

// ensureParentDirs is the APPLY-side arm of the canonical makedirs contract:
// with makedirs false it behaves exactly like requireParentDirs (fail, no
// partial creation); with makedirs true it creates missing parents at 0755
// and never modifies existing ones.
func ensureParentDirs(ctx context.Context, file exec.FileExec, module, target string, makedirs bool) error {
	if !makedirs {
		return requireParentDirs(ctx, file, module, target, makedirs)
	}
	parent := filepath.Dir(target)
	if parent == "" || parent == "." || parent == "/" {
		return nil
	}
	if _, err := file.Stat(ctx, parent); err == nil {
		return nil
	}
	if err := file.MkdirAll(ctx, parent, fileParentMode); err != nil {
		return fmt.Errorf("%s: create parent %s: %w", module, parent, err)
	}
	return nil
}
