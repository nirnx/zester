package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// FileAppend implements the file.append state.
// It ensures that specific lines are present in a file.
type FileAppend struct {
	id   string
	reqs state.Requisites

	Path string
	Text []string

	file exec.FileExec

	// backup stores original content for revert.
	backup    []byte
	backupSet bool
}

// NewFileAppendBuilder returns a state.Builder that creates FileAppend states.
func NewFileAppendBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		return newFileAppend(id, config, mctx.File)
	}
}

func newFileAppend(id string, config map[string]any, file exec.FileExec) (state.State, error) {
	f := &FileAppend{id: id, file: file}

	f.Path, _ = config["name"].(string)
	if f.Path == "" {
		f.Path = id
	}

	f.Text = parseAnyStringList(config, "text")

	f.reqs = state.ParseRequisites(config)

	return f, nil
}

func (f *FileAppend) Name() string           { return "file.append:" + f.id }
func (f *FileAppend) Reqs() state.Requisites { return f.reqs }

func (f *FileAppend) Check(ctx context.Context) (state.CheckResult, error) {
	data, err := f.file.ReadFile(ctx, f.Path)
	if err != nil {
		// File doesn't exist — need to create and append.
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("file %s does not exist", f.Path),
		}, nil
	}

	content := string(data)
	var missing []string
	for _, line := range f.Text {
		if !strings.Contains(content, line) {
			missing = append(missing, line)
		}
	}

	if len(missing) > 0 {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("%d lines missing from %s", len(missing), f.Path),
		}, nil
	}

	return state.CheckResult{NeedsChange: false}, nil
}

func (f *FileAppend) Apply(ctx context.Context) (state.ApplyResult, error) {
	// Read current content (may not exist).
	existing, err := f.file.ReadFile(ctx, f.Path)
	if err == nil {
		f.backup = existing
		f.backupSet = true
	}

	content := string(existing)
	var appended int

	for _, line := range f.Text {
		if !strings.Contains(content, line) {
			if len(content) > 0 && !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			content += line + "\n"
			appended++
		}
	}

	if appended == 0 {
		return state.ApplyResult{Changed: false}, nil
	}

	if err := f.file.WriteFile(ctx, f.Path, []byte(content), 0644); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.append: write %s: %w", f.Path, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("appended %d lines to %s", appended, f.Path),
		Details: map[string]string{
			"path":  f.Path,
			"lines": fmt.Sprintf("%d", appended),
		},
	}, nil
}

func (f *FileAppend) Revert(ctx context.Context) (state.ApplyResult, error) {
	if !f.backupSet {
		// File didn't exist before — remove it.
		if err := f.file.Remove(ctx, f.Path); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.append: revert remove %s: %w", f.Path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("removed %s (revert append to new file)", f.Path),
		}, nil
	}

	if err := f.file.WriteFile(ctx, f.Path, f.backup, 0644); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.append: revert %s: %w", f.Path, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("reverted %s to previous content", f.Path),
	}, nil
}
