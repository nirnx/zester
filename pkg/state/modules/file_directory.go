package modules

import (
	"context"
	"fmt"
	"io/fs"
	"os/user"
	"strconv"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
)

// FileDirectory implements the file.directory state.
// It ensures a directory exists with the specified permissions and ownership.
type FileDirectory struct {
	id   string
	reqs state.Requisites

	Path     string
	Mode     string
	DirMode  string
	User     string
	Group    string
	MakeDirs bool

	file exec.FileExec

	wasCreated bool
}

// NewFileDirectoryBuilder returns a state.Builder that creates FileDirectory states.
func NewFileDirectoryBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		return newFileDirectory(id, config, mctx.File)
	}
}

func newFileDirectory(id string, config map[string]any, file exec.FileExec) (state.State, error) {
	d := &FileDirectory{id: id, file: file}

	d.Path, _ = config["name"].(string)
	if d.Path == "" {
		d.Path = id
	}

	d.Mode = modeConfigToString(config["mode"])
	d.DirMode = modeConfigToString(config["dir_mode"])
	d.User, _ = config["user"].(string)
	d.Group, _ = config["group"].(string)
	d.MakeDirs, _ = config["makedirs"].(bool)

	d.reqs = state.ParseRequisites(config)

	return d, nil
}

func (d *FileDirectory) Name() string           { return "file.directory:" + d.id }
func (d *FileDirectory) Reqs() state.Requisites { return d.reqs }

func (d *FileDirectory) desiredMode() (fs.FileMode, error) {
	modeStr := d.Mode
	if modeStr == "" {
		modeStr = d.DirMode
	}
	if modeStr == "" {
		return 0755, nil
	}
	m, err := strconv.ParseUint(modeStr, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("file.directory: invalid mode %q: %w", modeStr, err)
	}
	return fs.FileMode(m), nil
}

func (d *FileDirectory) Check(ctx context.Context) (state.CheckResult, error) {
	info, err := d.file.Stat(ctx, d.Path)
	if err != nil {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("directory %s does not exist", d.Path),
		}, nil
	}

	if !info.IsDir() {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("%s exists but is not a directory", d.Path),
		}, nil
	}

	mode, err := d.desiredMode()
	if err != nil {
		return state.CheckResult{}, err
	}

	if info.Mode().Perm() != mode {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("mode %04o != %04o for %s", info.Mode().Perm(), mode, d.Path),
		}, nil
	}

	return state.CheckResult{NeedsChange: false}, nil
}

func (d *FileDirectory) Apply(ctx context.Context) (state.ApplyResult, error) {
	mode, err := d.desiredMode()
	if err != nil {
		return state.ApplyResult{}, err
	}

	// Check if directory already exists.
	_, statErr := d.file.Stat(ctx, d.Path)
	if statErr != nil {
		d.wasCreated = true
	}

	if err := d.file.MkdirAll(ctx, d.Path, mode); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.directory: mkdir %s: %w", d.Path, err)
	}

	if err := d.file.Chmod(ctx, d.Path, mode); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.directory: chmod %s: %w", d.Path, err)
	}

	if err := d.setOwnership(ctx); err != nil {
		return state.ApplyResult{}, err
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("ensured directory %s (mode %04o)", d.Path, mode),
		Details: map[string]string{
			"path": d.Path,
			"mode": fmt.Sprintf("%04o", mode),
		},
	}, nil
}

func (d *FileDirectory) Revert(ctx context.Context) (state.ApplyResult, error) {
	if d.wasCreated {
		if err := d.file.RemoveAll(ctx, d.Path); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.directory: revert remove %s: %w", d.Path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("removed directory %s (revert create)", d.Path),
		}, nil
	}

	return state.ApplyResult{Changed: false}, nil
}

func (d *FileDirectory) setOwnership(ctx context.Context) error {
	if d.User == "" && d.Group == "" {
		return nil
	}

	uid := -1
	gid := -1

	if d.User != "" {
		u, err := user.Lookup(d.User)
		if err != nil {
			return fmt.Errorf("file.directory: lookup user %q: %w", d.User, err)
		}
		uid, _ = strconv.Atoi(u.Uid)
	}

	if d.Group != "" {
		g, err := user.LookupGroup(d.Group)
		if err != nil {
			return fmt.Errorf("file.directory: lookup group %q: %w", d.Group, err)
		}
		gid, _ = strconv.Atoi(g.Gid)
	}

	if err := d.file.Chown(ctx, d.Path, uid, gid); err != nil {
		return fmt.Errorf("file.directory: chown %s: %w", d.Path, err)
	}
	return nil
}
