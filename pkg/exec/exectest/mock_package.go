// Package exectest provides test fakes for the exec layer interfaces.
// It follows the same pattern as pkg/bus/bustest.
package exectest

import (
	"context"
	"fmt"
	"sync"
)

// FakePackageExec is an in-memory fake for exec.PackageExec.
// It tracks installed packages and records all operations for assertions.
type FakePackageExec struct {
	mu        sync.Mutex
	name      string
	installed map[string]string // pkg -> version ("" if no version)
	refreshed int

	// InstallErr, if set, is returned by Install.
	InstallErr error
	// RemoveErr, if set, is returned by Remove.
	RemoveErr error
	// RefreshErr, if set, is returned by Refresh.
	RefreshErr error
}

func NewFakePackageExec(name string) *FakePackageExec {
	return &FakePackageExec{
		name:      name,
		installed: make(map[string]string),
	}
}

func (f *FakePackageExec) Name() string { return f.name }

func (f *FakePackageExec) IsInstalled(_ context.Context, pkg string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.installed[pkg]
	return ok, nil
}

func (f *FakePackageExec) Install(_ context.Context, pkg string, version string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.InstallErr != nil {
		return f.InstallErr
	}
	f.installed[pkg] = version
	return nil
}

func (f *FakePackageExec) Remove(_ context.Context, pkg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	if _, ok := f.installed[pkg]; !ok {
		return fmt.Errorf("package %s not installed", pkg)
	}
	delete(f.installed, pkg)
	return nil
}

func (f *FakePackageExec) Refresh(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RefreshErr != nil {
		return f.RefreshErr
	}
	f.refreshed++
	return nil
}

// PreInstall marks a package as already installed (for test setup).
func (f *FakePackageExec) PreInstall(pkg string, version string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed[pkg] = version
}

// IsInstalledSync returns the installed state (for test assertions).
func (f *FakePackageExec) IsInstalledSync(pkg string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.installed[pkg]
	return ok
}

// RefreshCount returns how many times Refresh was called.
func (f *FakePackageExec) RefreshCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshed
}
