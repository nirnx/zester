package modules

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
)

// FileSymlink implements the file.symlink state.
// It ensures a symbolic link exists at Path pointing to Target.
type FileSymlink struct {
	id   string
	reqs state.Requisites

	// Path is the absolute path for the symbolic link.
	Path string

	// Target is the destination the symlink should point to.
	Target string

	// Force removes an existing file/symlink at Path if it points elsewhere.
	Force bool

	// MakeDirs creates parent directories if true.
	MakeDirs bool

	file exec.FileExec
}

// NewFileSymlinkBuilder returns a state.Builder that creates FileSymlink states.
func NewFileSymlinkBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		return newFileSymlink(id, config, mctx.File)
	}
}

func newFileSymlink(id string, config map[string]any, file exec.FileExec) (state.State, error) {
	s := &FileSymlink{id: id, file: file}

	s.Path, _ = config["name"].(string)
	if s.Path == "" {
		s.Path = id
	}

	s.Target, _ = config["target"].(string)
	s.Force, _ = config["force"].(bool)
	s.MakeDirs, _ = config["makedirs"].(bool)

	s.reqs = state.ParseRequisites(config)

	return s, nil
}

func (s *FileSymlink) Name() string           { return "file.symlink:" + s.id }
func (s *FileSymlink) Reqs() state.Requisites { return s.reqs }

func (s *FileSymlink) Check(ctx context.Context) (state.CheckResult, error) {
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
	if s.MakeDirs {
		dir := filepath.Dir(s.Path)
		if dir != "" && dir != "." {
			if err := s.file.MkdirAll(ctx, dir, 0755); err != nil {
				return state.ApplyResult{}, fmt.Errorf("file.symlink: mkdir %s: %w", dir, err)
			}
		}
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
