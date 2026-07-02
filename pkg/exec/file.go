package exec

import (
	"context"
	"io/fs"
	"os"
)

// OSFileExec implements FileExec using the os package.
type OSFileExec struct{}

func (e *OSFileExec) ReadFile(_ context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (e *OSFileExec) WriteFile(_ context.Context, path string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (e *OSFileExec) Stat(_ context.Context, path string) (fs.FileInfo, error) {
	return os.Stat(path)
}

func (e *OSFileExec) Remove(_ context.Context, path string) error {
	return os.Remove(path)
}

func (e *OSFileExec) MkdirAll(_ context.Context, path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (e *OSFileExec) Chown(_ context.Context, path string, uid, gid int) error {
	return os.Chown(path, uid, gid)
}

func (e *OSFileExec) Chmod(_ context.Context, path string, mode fs.FileMode) error {
	return os.Chmod(path, mode)
}

func (e *OSFileExec) RemoveAll(_ context.Context, path string) error {
	return os.RemoveAll(path)
}

func (e *OSFileExec) Symlink(_ context.Context, target, linkPath string) error {
	return os.Symlink(target, linkPath)
}

func (e *OSFileExec) Readlink(_ context.Context, path string) (string, error) {
	return os.Readlink(path)
}
