package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// fsModeDefault is the permission mode used for new files when no mode is
// preserved or specified.
const fsModeDefault fs.FileMode = 0644

// FileCopy implements the file.copy state.
// It copies a source file to a destination path. Modeled after Salt's
// file.copy module.
type FileCopy struct {
	id   string
	reqs state.Requisites

	// Path is the destination path.
	Path string

	// Source is the file to copy from.
	Source string

	// Force overwrites the destination when it already exists.
	Force bool

	// Preserve copies the source file's permission mode to the destination.
	Preserve bool

	// MakeDirs creates parent directories of the destination if needed.
	MakeDirs bool

	file exec.FileExec

	backup    []byte
	backupSet bool

	// wasCreated records that Apply created the destination (it did not
	// pre-exist), so a same-instance Revert removes it. With neither memo
	// set, Revert is a clean no-op.
	wasCreated bool
}

// NewFileCopyBuilder returns a state.Builder that creates FileCopy states.
func NewFileCopyBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.copy: no file provider available")
		}
		return newFileCopy(id, config, mctx.File)
	}
}

func newFileCopy(id string, config map[string]any, file exec.FileExec) (state.State, error) {
	f := &FileCopy{id: id, file: file}

	f.Path = fsxResolvePath(config, id)
	f.Source, _ = config["source"].(string)
	f.Force, _ = config["force"].(bool)
	f.Preserve, _ = config["preserve"].(bool)
	f.MakeDirs, _ = config["makedirs"].(bool)

	if f.Source == "" {
		return nil, fmt.Errorf("file.copy: source is required")
	}

	f.reqs = state.ParseRequisites(config)

	return f, nil
}

func (f *FileCopy) Name() string           { return "file.copy:" + f.id }
func (f *FileCopy) Reqs() state.Requisites { return f.reqs }

func (f *FileCopy) Check(ctx context.Context) (state.CheckResult, error) {
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

	if f.MakeDirs {
		if err := fsxMakeParent(ctx, f.file, f.Path); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.copy: mkdir for %s: %w", f.Path, err)
		}
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
