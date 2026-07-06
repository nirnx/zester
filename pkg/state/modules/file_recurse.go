package modules

import (
	"context"
	"fmt"
	"io/fs"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// FileRecurse implements the file.recurse state.
// It recursively copies a source directory to a destination directory.
type FileRecurse struct {
	id   string
	reqs state.Requisites

	// Dest is the destination directory path.
	Dest string

	// Source is the source directory path.
	Source string

	// DirMode is the mode for created directories (e.g., "0755").
	DirMode string

	// FileMode is the mode for copied files (e.g., "0644").
	FileMode string

	// User is the owner username for copied files.
	User string

	// Group is the group name for copied files.
	Group string

	// Clean removes files in Dest that are not in Source.
	Clean bool

	// MakeDirs creates the destination parent directories if true.
	MakeDirs bool

	file exec.FileExec

	// createdFiles tracks files created by Apply for Revert.
	createdFiles []string
}

// NewFileRecurseBuilder returns a state.Builder that creates FileRecurse states.
func NewFileRecurseBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		return newFileRecurse(id, config, mctx.File)
	}
}

func newFileRecurse(id string, config map[string]any, file exec.FileExec) (state.State, error) {
	r := &FileRecurse{id: id, file: file}

	r.Dest, _ = config["name"].(string)
	if r.Dest == "" {
		r.Dest = id
	}

	r.Source, _ = config["source"].(string)
	r.DirMode = modeConfigToString(config["dir_mode"])
	r.FileMode = modeConfigToString(config["file_mode"])
	r.User, _ = config["user"].(string)
	r.Group, _ = config["group"].(string)
	r.Clean, _ = config["clean"].(bool)
	r.MakeDirs, _ = config["makedirs"].(bool)

	r.reqs = state.ParseRequisites(config)

	return r, nil
}

func (r *FileRecurse) Name() string           { return "file.recurse:" + r.id }
func (r *FileRecurse) Reqs() state.Requisites { return r.reqs }

func (r *FileRecurse) desiredFileMode() (fs.FileMode, error) {
	if r.FileMode == "" {
		return 0644, nil
	}
	m, err := strconv.ParseUint(r.FileMode, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("file.recurse: invalid file_mode %q: %w", r.FileMode, err)
	}
	return fs.FileMode(m), nil
}

func (r *FileRecurse) desiredDirMode() (fs.FileMode, error) {
	if r.DirMode == "" {
		return 0755, nil
	}
	m, err := strconv.ParseUint(r.DirMode, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("file.recurse: invalid dir_mode %q: %w", r.DirMode, err)
	}
	return fs.FileMode(m), nil
}

func (r *FileRecurse) Check(ctx context.Context) (state.CheckResult, error) {
	if r.Source == "" {
		return state.CheckResult{}, fmt.Errorf("file.recurse: source is required")
	}

	fileMode, err := r.desiredFileMode()
	if err != nil {
		return state.CheckResult{}, err
	}

	var diffCount int
	err = filepath.WalkDir(r.Source, func(srcPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(r.Source, srcPath)
		if err != nil {
			return err
		}
		destPath := filepath.Join(r.Dest, rel)

		if d.IsDir() {
			info, statErr := r.file.Stat(ctx, destPath)
			if statErr != nil || !info.IsDir() {
				diffCount++
			}
			return nil
		}

		// Compare file.
		destData, readErr := r.file.ReadFile(ctx, destPath)
		if readErr != nil {
			diffCount++
			return nil
		}

		srcData, srcReadErr := r.file.ReadFile(ctx, srcPath)
		if srcReadErr != nil {
			return srcReadErr
		}

		if hashBytes(srcData) != hashBytes(destData) {
			diffCount++
			return nil
		}

		info, statErr := r.file.Stat(ctx, destPath)
		if statErr != nil {
			diffCount++
			return nil
		}
		if info.Mode().Perm() != fileMode {
			diffCount++
		}

		return nil
	})
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("file.recurse: walk source %s: %w", r.Source, err)
	}

	if diffCount > 0 {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("%d files differ between %s and %s", diffCount, r.Source, r.Dest),
		}, nil
	}

	return state.CheckResult{NeedsChange: false}, nil
}

func (r *FileRecurse) Apply(ctx context.Context) (state.ApplyResult, error) {
	if r.Source == "" {
		return state.ApplyResult{}, fmt.Errorf("file.recurse: source is required")
	}

	fileMode, err := r.desiredFileMode()
	if err != nil {
		return state.ApplyResult{}, err
	}

	dirMode, err := r.desiredDirMode()
	if err != nil {
		return state.ApplyResult{}, err
	}

	uid, gid, err := r.resolveOwnership()
	if err != nil {
		return state.ApplyResult{}, err
	}

	if r.MakeDirs {
		parent := filepath.Dir(r.Dest)
		if parent != "" && parent != "." {
			if err := r.file.MkdirAll(ctx, parent, dirMode); err != nil {
				return state.ApplyResult{}, fmt.Errorf("file.recurse: mkdir parent %s: %w", parent, err)
			}
		}
	}

	// Build set of source-relative paths for clean mode.
	sourceFiles := make(map[string]struct{})

	var copiedFiles, copiedDirs int
	r.createdFiles = r.createdFiles[:0]

	err = filepath.WalkDir(r.Source, func(srcPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(r.Source, srcPath)
		if relErr != nil {
			return relErr
		}
		destPath := filepath.Join(r.Dest, rel)
		sourceFiles[destPath] = struct{}{}

		if d.IsDir() {
			if err := r.file.MkdirAll(ctx, destPath, dirMode); err != nil {
				return fmt.Errorf("mkdir %s: %w", destPath, err)
			}
			if err := r.file.Chmod(ctx, destPath, dirMode); err != nil {
				return fmt.Errorf("chmod %s: %w", destPath, err)
			}
			copiedDirs++
			return nil
		}

		data, readErr := r.file.ReadFile(ctx, srcPath)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", srcPath, readErr)
		}

		// Track whether this is a new file for revert.
		if _, statErr := r.file.Stat(ctx, destPath); statErr != nil {
			r.createdFiles = append(r.createdFiles, destPath)
		}

		if err := r.file.WriteFile(ctx, destPath, data, fileMode); err != nil {
			return fmt.Errorf("write %s: %w", destPath, err)
		}

		if err := r.file.Chmod(ctx, destPath, fileMode); err != nil {
			return fmt.Errorf("chmod %s: %w", destPath, err)
		}

		if uid != -1 || gid != -1 {
			if err := r.file.Chown(ctx, destPath, uid, gid); err != nil {
				return fmt.Errorf("chown %s: %w", destPath, err)
			}
		}

		copiedFiles++
		return nil
	})
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.recurse: apply: %w", err)
	}

	// Clean: remove dest files not in source.
	var cleaned int
	if r.Clean {
		cleaned, err = r.cleanDestination(ctx, sourceFiles)
		if err != nil {
			return state.ApplyResult{}, err
		}
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("copied %d files, %d dirs from %s to %s (cleaned %d)", copiedFiles, copiedDirs, r.Source, r.Dest, cleaned),
		Details: map[string]string{
			"source":  r.Source,
			"dest":    r.Dest,
			"files":   strconv.Itoa(copiedFiles),
			"dirs":    strconv.Itoa(copiedDirs),
			"cleaned": strconv.Itoa(cleaned),
		},
	}, nil
}

// cleanDestination removes files in dest not present in the source file set.
func (r *FileRecurse) cleanDestination(ctx context.Context, sourceFiles map[string]struct{}) (int, error) {
	var removed int
	err := filepath.WalkDir(r.Dest, func(destPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if destPath == r.Dest {
			return nil
		}
		if _, ok := sourceFiles[destPath]; !ok {
			if d.IsDir() {
				// Only remove if empty after children are removed.
				return nil
			}
			if err := r.file.Remove(ctx, destPath); err != nil {
				return fmt.Errorf("clean remove %s: %w", destPath, err)
			}
			removed++
		}
		return nil
	})
	if err != nil {
		return removed, fmt.Errorf("file.recurse: clean walk: %w", err)
	}
	return removed, nil
}

func (r *FileRecurse) Revert(ctx context.Context) (state.ApplyResult, error) {
	var removed int
	for _, f := range r.createdFiles {
		if err := r.file.Remove(ctx, f); err != nil {
			// Best-effort removal.
			continue
		}
		removed++
	}

	if removed == 0 && len(r.createdFiles) == 0 {
		return state.ApplyResult{Changed: false}, nil
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed %d files created by file.recurse (revert)", removed),
	}, nil
}

func (r *FileRecurse) resolveOwnership() (uid, gid int, err error) {
	uid = -1
	gid = -1

	if r.User != "" {
		u, lookupErr := user.Lookup(r.User)
		if lookupErr != nil {
			return -1, -1, fmt.Errorf("file.recurse: lookup user %q: %w", r.User, lookupErr)
		}
		uid, _ = strconv.Atoi(u.Uid)
	}

	if r.Group != "" {
		g, lookupErr := user.LookupGroup(r.Group)
		if lookupErr != nil {
			return -1, -1, fmt.Errorf("file.recurse: lookup group %q: %w", r.Group, lookupErr)
		}
		gid, _ = strconv.Atoi(g.Gid)
	}

	return uid, gid, nil
}
