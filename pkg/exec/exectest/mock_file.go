package exectest

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"
)

// FakeFileExec is an in-memory fake for exec.FileExec.
// It stores file contents, permissions, and ownership in memory.
type FakeFileExec struct {
	mu       sync.Mutex
	files    map[string]*fakeFile
	symlinks map[string]string

	// RemoveAllErr, if set, is returned by RemoveAll.
	RemoveAllErr error
}

type fakeFile struct {
	data []byte
	mode fs.FileMode
	uid  int
	gid  int
}

func NewFakeFileExec() *FakeFileExec {
	return &FakeFileExec{
		files:    make(map[string]*fakeFile),
		symlinks: make(map[string]string),
	}
}

func (f *FakeFileExec) ReadFile(_ context.Context, path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, ok := f.files[path]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", path)
	}
	cp := make([]byte, len(ff.data))
	copy(cp, ff.data)
	return cp, nil
}

func (f *FakeFileExec) WriteFile(_ context.Context, path string, data []byte, perm fs.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	f.files[path] = &fakeFile{data: cp, mode: perm}
	return nil
}

func (f *FakeFileExec) Stat(_ context.Context, path string) (fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, ok := f.files[path]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", path)
	}
	return &fakeFileInfo{name: path, size: int64(len(ff.data)), mode: ff.mode}, nil
}

func (f *FakeFileExec) Remove(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, path)
	return nil
}

func (f *FakeFileExec) RemoveAll(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RemoveAllErr != nil {
		return f.RemoveAllErr
	}
	for k := range f.files {
		if k == path || strings.HasPrefix(k, path+"/") {
			delete(f.files, k)
		}
	}
	return nil
}

func (f *FakeFileExec) MkdirAll(_ context.Context, path string, perm fs.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Track directories as empty files with directory mode.
	if _, ok := f.files[path]; !ok {
		f.files[path] = &fakeFile{mode: perm | fs.ModeDir}
	}
	return nil
}

func (f *FakeFileExec) Chown(_ context.Context, path string, uid, gid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, ok := f.files[path]
	if !ok {
		return fmt.Errorf("file not found: %s", path)
	}
	ff.uid = uid
	ff.gid = gid
	return nil
}

func (f *FakeFileExec) Chmod(_ context.Context, path string, mode fs.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, ok := f.files[path]
	if !ok {
		return fmt.Errorf("file not found: %s", path)
	}
	ff.mode = mode
	return nil
}

func (f *FakeFileExec) Symlink(_ context.Context, target, linkPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.symlinks[linkPath] = target
	return nil
}

func (f *FakeFileExec) Readlink(_ context.Context, path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	target, ok := f.symlinks[path]
	if !ok {
		return "", fmt.Errorf("readlink %s: %w", path, fmt.Errorf("no such file or directory"))
	}
	return target, nil
}

// PreCreate places a file in the fake filesystem (for test setup).
func (f *FakeFileExec) PreCreate(path string, data []byte, perm fs.FileMode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	f.files[path] = &fakeFile{data: cp, mode: perm}
}

// GetFile returns the file data (for test assertions).
func (f *FakeFileExec) GetFile(path string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, ok := f.files[path]
	if !ok {
		return nil, false
	}
	cp := make([]byte, len(ff.data))
	copy(cp, ff.data)
	return cp, true
}

// Exists returns whether a file exists.
func (f *FakeFileExec) Exists(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.files[path]
	return ok
}

// fakeFileInfo implements fs.FileInfo for the fake filesystem.
type fakeFileInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (fi *fakeFileInfo) Name() string       { return fi.name }
func (fi *fakeFileInfo) Size() int64        { return fi.size }
func (fi *fakeFileInfo) Mode() fs.FileMode  { return fi.mode }
func (fi *fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fi *fakeFileInfo) IsDir() bool        { return fi.mode.IsDir() }
func (fi *fakeFileInfo) Sys() any           { return nil }
