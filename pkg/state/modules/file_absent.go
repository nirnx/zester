package modules

import (
	"context"
	"fmt"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
)

// FileAbsent implements the file.absent state.
// It ensures a file or directory does not exist.
type FileAbsent struct {
	id   string
	reqs state.Requisites

	Path string

	file exec.FileExec
}

// NewFileAbsentBuilder returns a state.Builder that creates FileAbsent states.
func NewFileAbsentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		return newFileAbsent(id, config, mctx.File)
	}
}

func newFileAbsent(id string, config map[string]any, file exec.FileExec) (state.State, error) {
	f := &FileAbsent{id: id, file: file}

	f.Path, _ = config["name"].(string)
	if f.Path == "" {
		f.Path = id
	}

	f.reqs = state.ParseRequisites(config)

	return f, nil
}

func (f *FileAbsent) Name() string           { return "file.absent:" + f.id }
func (f *FileAbsent) Reqs() state.Requisites { return f.reqs }

func (f *FileAbsent) Check(ctx context.Context) (state.CheckResult, error) {
	_, err := f.file.Stat(ctx, f.Path)
	if err != nil {
		// File doesn't exist — no change needed.
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%s exists and should be absent", f.Path),
	}, nil
}

func (f *FileAbsent) Apply(ctx context.Context) (state.ApplyResult, error) {
	if err := f.file.RemoveAll(ctx, f.Path); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.absent: remove %s: %w", f.Path, err)
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed %s", f.Path),
		Details: map[string]string{"path": f.Path},
	}, nil
}

func (f *FileAbsent) Revert(_ context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{
		Changed: false,
		Diff:    "cannot revert file removal",
	}, nil
}
