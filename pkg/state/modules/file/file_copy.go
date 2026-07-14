package filemod

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// fsModeDefault is the permission mode used for new files when no mode is
// preserved or specified.
const fsModeDefault fs.FileMode = 0644

// FileCopy implements the file.copy state.
// It copies a source file to a destination path. Modeled after Salt's
// file.copy module.
//
// FileCopy is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). Path is
// declared `name,primary,aliases=path` (the file_managed.go alias tag is the
// exemplar — `path` is accepted for Salt compatibility, matching the legacy
// fsxResolvePath helper); Source is `required` (a missing/empty source is a
// typed MissingRequired error, matching the legacy explicit check). The
// unexported runtime fields are untagged, so the schema compiler skips them.
type FileCopy struct {
	id   string
	reqs state.Requisites

	// Path is the destination path; it defaults to the state ID. The path
	// alias is accepted for Salt compatibility.
	Path string `zester:"name,primary,aliases=path" usage:"destination path; the path alias is accepted for Salt compatibility (defaults to the state ID)"`

	// Source: file.* family component (requiredness member-supplied — required here).
	fileSourceParam

	// Force overwrites the destination when it already exists.
	Force bool `zester:"force" usage:"overwrite the destination when it already exists; without force an existing destination is left untouched; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	// Preserve copies the source file's permission mode to the destination.
	Preserve bool `zester:"preserve" usage:"copy the source file's permission mode to the destination, instead of the 0644 default; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	// MakeDirs: file.* family component (canonical parent-creation contract).
	fileMakeDirsParam

	file exec.FileExec

	backup    []byte
	backupSet bool

	// wasCreated records that Apply created the destination (it did not
	// pre-exist), so a same-instance Revert removes it. With neither memo
	// set, Revert is a clean no-op.
	wasCreated bool
}

// fileCopySpec is the compiled schema + documentation for file.copy. It is
// compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is verified against the live
// Check/Apply/Revert code.
var fileCopySpec = regdef.MustSpec("file.copy", modschema.KindState, FileCopy{}, fileCopyDoc,
	modschema.WithRequired("source", true))

var fileCopyDoc = modschema.Doc{
	Summary: "Copy a source file that already exists on the peel to a destination path.",
	Description: "`file.copy` copies `source` (a path local to the peel) to the destination. The " +
		"destination path defaults to the state ID; `path` is accepted as an alias. Without `force`, " +
		"an existing destination is left untouched (the copy is a one-time seed); with `force`, the " +
		"destination is kept in sync with the source by content hash.",
	Effects: modschema.Effects{
		Check: "Reports a would-change first — naming the missing parent and the remedy — when the target's parent directory is missing and `makedirs` is unset (the canonical file.* contract: an earlier state in the run may create it; the strict failure happens at apply). " +
			"Reads the source — a missing source is an error, not a reported diff. If the " +
			"destination does not exist, a change is needed. If the destination exists and `force` is " +
			"not set, no change is needed (the existing file wins). With `force`, source and " +
			"destination contents are compared by SHA-256 hash; a difference needs a change, and, " +
			"only when `preserve` is also set, a permission-mode difference needs a change too.",
		Apply: "Reads the source file. If the destination exists and `force` is not set, Apply is a " +
			"no-op. With `force` on an existing destination, the current content is captured for " +
			"revert (a non-not-exist read error fails the apply rather than overwriting content it " +
			"could not capture) before it is overwritten. Creates missing parent directories when " +
			"`makedirs` is set, then writes the destination with mode 0644, or the source's " +
			"permission mode (via an explicit chmod) when `preserve` is set. Reports the byte count " +
			"in its details.",
		Revert: "Restores what this run's Apply changed: a destination that pre-existed (and was " +
			"overwritten under `force`) is rewritten with its captured prior content; a destination " +
			"this instance created is removed (tolerating an already-missing file). A fresh instance " +
			"(a standalone revert) recorded nothing and is an explicit clean no-op.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Back up a config file before editing it",
			Kind:        "state",
			Explanation: "A one-time seed copy: without force, the backup is created once and never overwritten.",
			Code:        "/etc/nginx/nginx.conf.bak:\n  file.copy:\n    - source: /etc/nginx/nginx.conf\n",
		},
		{
			Title:       "Deploy from a staging area, keeping it in sync",
			Kind:        "state",
			Explanation: "force keeps the destination synced to the source; preserve copies the source's permission mode; makedirs creates missing parent directories.",
			Code: "/opt/app/config.yaml:\n  file.copy:\n    - source: /opt/staging/config.yaml\n" +
				"    - force: true\n    - preserve: true\n    - makedirs: true\n",
		},
		{
			Title:       "Copy a file ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the destination path; source is a key=value arg.",
			Code:        "zester 'web-01' file.copy /etc/nginx/nginx.conf.bak source=/etc/nginx/nginx.conf",
		},
	},
	Notes: []modschema.Note{
		{
			Title: "Only regular files are copied",
			Body:  "Salt's recursive directory copy (`subdir`), `user`/`group` ownership, and `mode` parameters are not supported.",
		},
		{
			Title: "Ownership is not preserved",
			Body:  "Only the permission mode is copied, and only when `preserve` is set.",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7", "BD-9"},
	SeeAlso:     []string{"file.managed", "file.touch"},
}

// NewFileCopyBuilder returns a state.Builder that creates FileCopy states.
// Decode policy (unknown-key handling, reserved keys) is threaded via opts;
// the peel supplies it through modules.RegisterAll.
func NewFileCopyBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.copy: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		f := &FileCopy{}
		if _, err := fileCopySpec.Decode(id, config, f, opts); err != nil {
			return nil, fmt.Errorf("file.copy: %w", err)
		}
		f.id = id
		f.file = mctx.File
		f.reqs = state.ParseRequisites(config)
		return f, nil
	}
}

func (f *FileCopy) Name() string           { return "file.copy:" + f.id }
func (f *FileCopy) Reqs() state.Requisites { return f.reqs }

func (f *FileCopy) Check(ctx context.Context) (state.CheckResult, error) {
	// Canonical makedirs contract (§13, Check re-ruled 2026-07-14): a
	// missing parent with makedirs unset is a WOULD-CHANGE — an earlier
	// state in the run may create it, so dry runs of ordered trees stay
	// valid; Apply stays strict.
	if detail, err := checkParentDirs(ctx, f.file, "file.copy", f.Path, f.MakeDirs); err != nil {
		return state.CheckResult{}, err
	} else if detail != "" {
		return state.CheckResult{NeedsChange: true, Diff: "file.copy: " + detail}, nil
	}
	src, err := f.file.ReadFile(ctx, f.Source)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("file.copy: read source %s: %w", f.Source, err)
	}

	dst, err := f.file.ReadFile(ctx, f.Path)
	if err != nil {
		// Only a genuine not-exist means "destination absent"; any other
		// read error fails the check rather than risking a blind overwrite.
		if !errors.Is(err, fs.ErrNotExist) {
			return state.CheckResult{}, fmt.Errorf("file.copy: read %s: %w", f.Path, err)
		}
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("destination %s does not exist", f.Path),
		}, nil
	}

	// Destination exists: only replace it when force is set.
	if !f.Force {
		return state.CheckResult{NeedsChange: false}, nil
	}

	if hashBytes(src) != hashBytes(dst) {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("destination %s differs from source %s", f.Path, f.Source),
		}, nil
	}

	if f.Preserve {
		if diff, changed := f.modeDiffers(ctx); changed {
			return state.CheckResult{NeedsChange: true, Diff: diff}, nil
		}
	}

	return state.CheckResult{NeedsChange: false}, nil
}

// modeDiffers reports whether the destination's mode differs from the source's.
func (f *FileCopy) modeDiffers(ctx context.Context) (string, bool) {
	si, err := f.file.Stat(ctx, f.Source)
	if err != nil {
		return "", false
	}
	di, err := f.file.Stat(ctx, f.Path)
	if err != nil {
		return "", false
	}
	if si.Mode().Perm() != di.Mode().Perm() {
		return fmt.Sprintf("mode %o != %o for %s", di.Mode().Perm(), si.Mode().Perm(), f.Path), true
	}
	return "", false
}

func (f *FileCopy) Apply(ctx context.Context) (state.ApplyResult, error) {
	src, err := f.file.ReadFile(ctx, f.Source)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.copy: read source %s: %w", f.Source, err)
	}

	// Probe prior state for revert. Only a genuine not-exist means "will
	// create"; any other read error fails the apply rather than overwriting
	// a destination whose current content could not be captured.
	preExisted := false
	var priorContent []byte
	existing, err := f.file.ReadFile(ctx, f.Path)
	switch {
	case err == nil:
		if !f.Force {
			return state.ApplyResult{Changed: false}, nil
		}
		preExisted = true
		priorContent = existing
	case errors.Is(err, fs.ErrNotExist):
		// Will create.
	default:
		return state.ApplyResult{}, fmt.Errorf("file.copy: read %s: %w", f.Path, err)
	}

	if err := ensureParentDirs(ctx, f.file, "file.copy", f.Path, f.MakeDirs); err != nil {
		return state.ApplyResult{}, err
	}

	mode := fsModeDefault
	if f.Preserve {
		if info, err := f.file.Stat(ctx, f.Source); err == nil {
			mode = info.Mode().Perm()
		}
	}

	if err := f.file.WriteFile(ctx, f.Path, src, mode); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.copy: write %s: %w", f.Path, err)
	}

	// Record revert memos only after the write actually changed the system.
	// First capture wins: a re-Apply on the same instance (retry:, watch-
	// forced runs) must not clobber the original backup with the copy's own
	// content — and a destination this instance CREATED stays wasCreated, or
	// Revert would rewrite source content into a file that never pre-existed
	// instead of removing it.
	if preExisted {
		if !f.backupSet && !f.wasCreated {
			f.backup = priorContent
			f.backupSet = true
		}
	} else {
		f.wasCreated = true
	}

	if f.Preserve {
		if err := f.file.Chmod(ctx, f.Path, mode); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.copy: chmod %s: %w", f.Path, err)
		}
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("copied %s to %s (%d bytes)", f.Source, f.Path, len(src)),
		Details: map[string]string{
			"source": f.Source,
			"path":   f.Path,
			"bytes":  fmt.Sprintf("%d", len(src)),
		},
	}, nil
}

func (f *FileCopy) Revert(ctx context.Context) (state.ApplyResult, error) {
	switch {
	case f.backupSet:
		if err := f.file.WriteFile(ctx, f.Path, f.backup, fsModeDefault); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.copy: revert %s: %w", f.Path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("reverted %s to previous content", f.Path),
		}, nil

	case f.wasCreated:
		if err := f.file.Remove(ctx, f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return state.ApplyResult{}, fmt.Errorf("file.copy: revert remove %s: %w", f.Path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("removed %s (revert create)", f.Path),
		}, nil

	default:
		// No apply recorded on this instance: never destroy a destination
		// we did not write.
		return state.ApplyResult{
			Changed: false,
			Diff:    "nothing to revert (no apply recorded in this run)",
		}, nil
	}
}
