package exectest

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
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
	readErrs map[string]error

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
		readErrs: make(map[string]error),
	}
}

func (f *FakeFileExec) ReadFile(_ context.Context, path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.readErrs[path]; ok {
		return nil, err
	}
	ff, ok := f.files[path]
	if !ok {
		return nil, fmt.Errorf("file not found: %s: %w", path, fs.ErrNotExist)
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
	// os.WriteFile semantics: perm applies only at CREATION; overwriting an
	// existing file keeps its mode (and ownership). The fake previously set
	// the mode on every write, which masked a real never-converges bug
	// (file.managed mode drift on pre-existing files).
	if existing, ok := f.files[path]; ok {
		existing.data = cp
		return nil
	}
	f.files[path] = &fakeFile{data: cp, mode: perm}
	return nil
}

func (f *FakeFileExec) Stat(_ context.Context, path string) (fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, ok := f.files[path]
	if !ok {
		// IMPLICIT directories: an entry cannot exist without its ancestor
		// directories, so any strict prefix-dir of an existing entry stats as
		// a directory (mirrors a real filesystem; keeps parent-existence
		// checks like the file.* makedirs contract working against fixtures
		// that pre-create only the file).
		prefix := strings.TrimSuffix(path, "/") + "/"
		for k := range f.files {
			if strings.HasPrefix(k, prefix) {
				return &fakeFileInfo{name: path, mode: 0o755 | fs.ModeDir}, nil
			}
		}
		for k := range f.symlinks {
			if strings.HasPrefix(k, prefix) {
				return &fakeFileInfo{name: path, mode: 0o755 | fs.ModeDir}, nil
			}
		}
		return nil, fmt.Errorf("file not found: %s: %w", path, fs.ErrNotExist)
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
		return fmt.Errorf("file not found: %s: %w", path, fs.ErrNotExist)
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
		return fmt.Errorf("file not found: %s: %w", path, fs.ErrNotExist)
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
		return "", fmt.Errorf("readlink %s: %w", path, fs.ErrNotExist)
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

// SetReadError injects an error for ReadFile on the given path (e.g. a
// permission failure) — distinct from the file simply not existing, which
// wraps fs.ErrNotExist. Pass nil to clear.
func (f *FakeFileExec) SetReadError(path string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.readErrs, path)
		return
	}
	f.readErrs[path] = err
}

// SetOwner presets a file's ownership (test setup).
func (f *FakeFileExec) SetOwner(path string, uid, gid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ff, ok := f.files[path]; ok {
		ff.uid = uid
		ff.gid = gid
	}
}

// Owner returns the tracked ownership of a file.
func (f *FakeFileExec) Owner(_ context.Context, path string) (int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, ok := f.files[path]
	if !ok {
		return 0, 0, fmt.Errorf("file not found: %s: %w", path, fs.ErrNotExist)
	}
	return ff.uid, ff.gid, nil
}

// Walk walks the fake tree rooted at root in sorted path order (fs.WalkDir
// semantics: fn is called for root first if present, then each descendant).
func (f *FakeFileExec) Walk(ctx context.Context, root string, fn fs.WalkDirFunc) error {
	f.mu.Lock()
	var paths []string
	for p := range f.files {
		if p == root || strings.HasPrefix(p, root+"/") {
			paths = append(paths, p)
		}
	}
	infos := make(map[string]*fakeFileInfo, len(paths))
	for _, p := range paths {
		ff := f.files[p]
		infos[p] = &fakeFileInfo{name: p, size: int64(len(ff.data)), mode: ff.mode}
	}
	f.mu.Unlock()

	if len(paths) == 0 {
		return fn(root, nil, fmt.Errorf("walk %s: %w", root, fs.ErrNotExist))
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(p, fs.FileInfoToDirEntry(infos[p]), nil); err != nil {
			if err == fs.SkipDir || err == fs.SkipAll { //nolint:errorlint // sentinel identity per fs docs
				return nil
			}
			return err
		}
	}
	return nil
}
