package exectest

import (
	"context"
	"sync"

	"github.com/nirnx/zester/pkg/exec"
)

// FakeMountExec is an in-memory fake for exec.MountExec.
// It tracks mounted filesystems and fstab entries separately.
type FakeMountExec struct {
	mu     sync.Mutex
	mounts map[string]exec.MountEntry // key: mountPoint (currently mounted)
	fstab  map[string]exec.MountEntry // key: mountPoint (fstab entries)

	// Error injection — when set, the corresponding method returns this error.
	IsMountedErr   error
	MountErr       error
	UnmountErr     error
	GetFstabErr    error
	SetFstabErr    error
	RemoveFstabErr error
}

// NewFakeMountExec creates a FakeMountExec with empty mount and fstab stores.
func NewFakeMountExec() *FakeMountExec {
	return &FakeMountExec{
		mounts: make(map[string]exec.MountEntry),
		fstab:  make(map[string]exec.MountEntry),
	}
}

func (f *FakeMountExec) IsMounted(_ context.Context, mountPoint string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.IsMountedErr != nil {
		return false, f.IsMountedErr
	}
	_, ok := f.mounts[mountPoint]
	return ok, nil
}

func (f *FakeMountExec) Mount(_ context.Context, entry exec.MountEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MountErr != nil {
		return f.MountErr
	}
	f.mounts[entry.MountPoint] = entry
	return nil
}

func (f *FakeMountExec) Unmount(_ context.Context, mountPoint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.UnmountErr != nil {
		return f.UnmountErr
	}
	delete(f.mounts, mountPoint)
	return nil
}

func (f *FakeMountExec) GetFstab(_ context.Context, mountPoint string) (*exec.MountEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GetFstabErr != nil {
		return nil, f.GetFstabErr
	}
	e, ok := f.fstab[mountPoint]
	if !ok {
		return nil, nil
	}
	cp := e
	return &cp, nil
}

func (f *FakeMountExec) SetFstab(_ context.Context, entry exec.MountEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SetFstabErr != nil {
		return f.SetFstabErr
	}
	f.fstab[entry.MountPoint] = entry
	return nil
}

func (f *FakeMountExec) RemoveFstab(_ context.Context, mountPoint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RemoveFstabErr != nil {
		return f.RemoveFstabErr
	}
	delete(f.fstab, mountPoint)
	return nil
}

// PreMount places a mount entry in the active mounts map (for test setup).
func (f *FakeMountExec) PreMount(entry exec.MountEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mounts[entry.MountPoint] = entry
}

// PreFstab places an entry in the fstab map (for test setup).
func (f *FakeMountExec) PreFstab(entry exec.MountEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fstab[entry.MountPoint] = entry
}

// IsMountedSync returns whether mountPoint is currently mounted (for assertions).
func (f *FakeMountExec) IsMountedSync(mountPoint string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.mounts[mountPoint]
	return ok
}

// GetFstabSync returns the fstab entry for mountPoint (for assertions).
func (f *FakeMountExec) GetFstabSync(mountPoint string) (exec.MountEntry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.fstab[mountPoint]
	return e, ok
}
