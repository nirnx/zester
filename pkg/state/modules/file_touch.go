package modules

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// FileTouch implements the file.touch state.
// It creates an empty file when missing, or updates the modification time of
// an existing file. Modeled after Salt's file.touch module.
type FileTouch struct {
	id   string
	reqs state.Requisites

	// Path is the file to touch.
	Path string

	// MakeDirs creates parent directories if needed.
	MakeDirs bool

	file exec.FileExec
	cmd  exec.CommandExec

	createdNew bool
}

// NewFileTouchBuilder returns a state.Builder that creates FileTouch states.
func NewFileTouchBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.touch: no file provider available")
		}
		return newFileTouch(id, config, mctx.File, mctx.Command)
	}
}

func newFileTouch(id string, config map[string]any, file exec.FileExec, cmd exec.CommandExec) (state.State, error) {
	f := &FileTouch{id: id, file: file, cmd: cmd}

	f.Path = fsxResolvePath(config, id)
	f.MakeDirs, _ = config["makedirs"].(bool)

	f.reqs = state.ParseRequisites(config)

	return f, nil
}

func (f *FileTouch) Name() string           { return "file.touch:" + f.id }
func (f *FileTouch) Reqs() state.Requisites { return f.reqs }

func (f *FileTouch) exists(ctx context.Context) bool {
	_, err := f.file.Stat(ctx, f.Path)
	return err == nil
}

func (f *FileTouch) Check(ctx context.Context) (state.CheckResult, error) {
	if f.exists(ctx) {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("file %s does not exist", f.Path),
	}, nil
}

func (f *FileTouch) Apply(ctx context.Context) (state.ApplyResult, error) {
	if f.MakeDirs {
		if err := fsxMakeParent(ctx, f.file, f.Path); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.touch: mkdir for %s: %w", f.Path, err)
		}
	}

	if !f.exists(ctx) {
		if err := f.file.WriteFile(ctx, f.Path, []byte{}, fsModeDefault); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.touch: create %s: %w", f.Path, err)
		}
		f.createdNew = true
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("created %s", f.Path),
			Details: map[string]string{"path": f.Path, "action": "created"},
		}, nil
	}

	// File exists: update its modification time via `touch` when possible.
	if f.cmd != nil {
		if _, err := f.cmd.Run(ctx, exec.CommandOpts{
			Command: "touch",
			Args:    []string{f.Path},
		}); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.touch: touch %s: %w", f.Path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("updated mtime of %s", f.Path),
			Details: map[string]string{"path": f.Path, "action": "mtime"},
		}, nil
	}

	return state.ApplyResult{Changed: false}, nil
}

func (f *FileTouch) Revert(ctx context.Context) (state.ApplyResult, error) {
	if !f.createdNew {
		return state.ApplyResult{
			Changed: false,
			Diff:    "file.touch on existing file cannot be reverted",
		}, nil
	}
	if err := f.file.Remove(ctx, f.Path); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.touch: revert remove %s: %w", f.Path, err)
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed %s (revert touch)", f.Path),
	}, nil
}
