package filemod

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// FileSymlink implements the file.symlink state.
// It ensures a symbolic link exists at Path pointing to Target.
//
// FileSymlink is also its own schema proto: the tagged exported fields ARE
// the module's parameter declaration (one schema declaration per module).
// The unexported runtime fields (id, reqs, file) are untagged, so the schema
// compiler skips them.
type FileSymlink struct {
	id   string
	reqs state.Requisites

	// Path is the absolute path for the symbolic link; it defaults to the
	// state ID.
	Path string `zester:"name,primary,aliases=path" usage:"absolute path for the symbolic link (defaults to the state ID); the path alias is accepted"`

	// Target is the destination the symlink should point to.
	Target string `zester:"target" usage:"destination the symlink should point to"`

	// Force removes an existing file/symlink at Path if it points elsewhere.
	Force bool `zester:"force" usage:"replace an existing file or wrong-target symlink at the link path; without force an existing mismatch is an error; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	// MakeDirs: file.* family component (canonical parent-creation contract).
	fileMakeDirsParam

	file exec.FileExec
}

// fileSymlinkSpec is the compiled schema + documentation for file.symlink. It
// is compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is verified against the live
// Check/Apply/Revert code.
var fileSymlinkSpec = regdef.MustSpec("file.symlink", modschema.KindState, FileSymlink{}, modschema.Doc{
	Summary: "Ensure a symbolic link exists at a path, pointing to a target.",
	Description: "`file.symlink` ensures a symbolic link exists at the path (defaulting to the " +
		"state ID) pointing to `target`. Without `force`, a pre-existing file or wrong-target " +
		"symlink at the path is an error rather than being replaced.",
	Effects: modschema.Effects{
		Check: "Reports a would-change first — naming the missing parent and the remedy — when the target's parent directory is missing and `makedirs` is unset (the canonical file.* contract: an earlier state in the run may create it; the strict failure happens at apply). " +
			"Reads the link at the path. A missing link, or one whose current target differs " +
			"from the declared `target`, needs a change; a matching link needs none.",
		Apply: "Creates missing parent directories first when `makedirs` is set. If a symlink " +
			"already points to `target`, Apply is a no-op. If something else exists at the path (a " +
			"wrong-target symlink, or a non-symlink file/directory), it is only removed when `force` " +
			"is set — otherwise Apply fails with an error naming what is in the way. Reports the " +
			"created link and its target in its details.",
		Revert: "Removes the symlink unconditionally. It does not restore any file that `force` " +
			"replaced, and a fresh instance's Revert still removes the symlink at the path (Revert " +
			"is not gated on this instance having run Apply).",
	},
	Examples: []modschema.Example{
		{
			Title:       "Create a symlink using the state ID as the link path",
			Kind:        "state",
			Explanation: "The state ID is the link path; target is the only parameter needed.",
			Code:        "/usr/local/bin/python:\n  file.symlink:\n    - target: /usr/bin/python3\n",
		},
		{
			Title:       "Replace whatever exists, creating parent directories",
			Kind:        "state",
			Explanation: "force replaces an existing mismatched file or symlink; makedirs creates missing parent directories.",
			Code: "/opt/app/config:\n  file.symlink:\n    - target: /etc/app/config\n" +
				"    - force: true\n    - makedirs: true\n    - require:\n      - \"file.directory:/opt/app\"\n",
		},
		{
			Title:       "Create a symlink ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the link path; target is a key=value arg.",
			Code:        "zester 'web-01' file.symlink /usr/local/bin/python target=/usr/bin/python3",
		},
	},
	Notes: []modschema.Note{
		{
			Title: "Without force, a mismatch is an error",
			Body:  "If `force` is false (the default) and a file or symlink already exists at the path with a different target, Apply returns an error rather than replacing it.",
		},
		{
			Title: "makedirs uses a fixed mode",
			Body:  "makedirs creates the full parent directory path with mode 0755. Use file.directory first if you need a specific mode.",
		},
		{
			Title: "Revert does not restore a replaced file",
			Body:  "Revert removes the symlink but does not restore any file that was replaced when force: true was used.",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7", "BD-9"},
	SeeAlso:     []string{"file.managed", "file.absent"},
})

// NewFileSymlinkBuilder returns a state.Builder that creates FileSymlink
// states. Decode policy (unknown-key handling, reserved keys) is threaded via
// opts; the peel supplies it through modules.RegisterAll.
func NewFileSymlinkBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		s := &FileSymlink{}
		if _, err := fileSymlinkSpec.Decode(id, config, s, opts); err != nil {
			return nil, fmt.Errorf("file.symlink: %w", err)
		}
		s.id = id
		s.file = mctx.File
		s.reqs = state.ParseRequisites(config)
		return s, nil
	}
}

func (s *FileSymlink) Name() string           { return "file.symlink:" + s.id }
func (s *FileSymlink) Reqs() state.Requisites { return s.reqs }

func (s *FileSymlink) Check(ctx context.Context) (state.CheckResult, error) {
	// Canonical makedirs contract (§13, Check re-ruled 2026-07-14): a
	// missing parent with makedirs unset is a WOULD-CHANGE — an earlier
	// state in the run may create it, so dry runs of ordered trees stay
	// valid; Apply stays strict.
	if detail, err := checkParentDirs(ctx, s.file, "file.symlink", s.Path, s.MakeDirs); err != nil {
		return state.CheckResult{}, err
	} else if detail != "" {
		return state.CheckResult{NeedsChange: true, Diff: "file.symlink: " + detail}, nil
	}
	current, err := s.file.Readlink(ctx, s.Path)
	if err != nil {
		// Symlink doesn't exist.
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("symlink %s does not exist", s.Path),
		}, nil
	}

	if current != s.Target {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("symlink %s points to %q, want %q", s.Path, current, s.Target),
		}, nil
	}

	return state.CheckResult{NeedsChange: false}, nil
}

func (s *FileSymlink) Apply(ctx context.Context) (state.ApplyResult, error) {
	if err := ensureParentDirs(ctx, s.file, "file.symlink", s.Path, s.MakeDirs); err != nil {
		return state.ApplyResult{}, err
	}

	// Check if something already exists at path.
	current, err := s.file.Readlink(ctx, s.Path)
	if err == nil {
		// Symlink exists — check if it already points to the right target.
		if current == s.Target {
			return state.ApplyResult{Changed: false}, nil
		}
		// Wrong target — remove if force is set.
		if !s.Force {
			return state.ApplyResult{}, fmt.Errorf("file.symlink: %s already exists pointing to %q; use force: true to replace", s.Path, current)
		}
		if err := s.file.Remove(ctx, s.Path); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.symlink: remove existing %s: %w", s.Path, err)
		}
	} else {
		// Check if a non-symlink file/dir exists at path.
		if _, statErr := s.file.Stat(ctx, s.Path); statErr == nil {
			if !s.Force {
				return state.ApplyResult{}, fmt.Errorf("file.symlink: %s exists as a non-symlink; use force: true to replace", s.Path)
			}
			if err := s.file.RemoveAll(ctx, s.Path); err != nil {
				return state.ApplyResult{}, fmt.Errorf("file.symlink: remove existing %s: %w", s.Path, err)
			}
		}
	}

	if err := s.file.Symlink(ctx, s.Target, s.Path); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.symlink: create %s -> %s: %w", s.Path, s.Target, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("created symlink %s -> %s", s.Path, s.Target),
		Details: map[string]string{
			"path":   s.Path,
			"target": s.Target,
		},
	}, nil
}

func (s *FileSymlink) Revert(ctx context.Context) (state.ApplyResult, error) {
	if err := s.file.Remove(ctx, s.Path); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.symlink: revert remove %s: %w", s.Path, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed symlink %s (revert)", s.Path),
	}, nil
}
