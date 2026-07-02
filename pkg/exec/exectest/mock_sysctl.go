package exectest

import (
	"context"
	"sync"
)

// FakeSysctlExec is an in-memory fake for exec.SysctlExec.
// It stores key→value pairs in memory and records Persist calls.
type FakeSysctlExec struct {
	mu        sync.Mutex
	values    map[string]string
	persisted map[string]string

	// Error injection — when set, the corresponding method returns this error.
	GetErr     error
	SetErr     error
	PersistErr error
}

// NewFakeSysctlExec creates a FakeSysctlExec with an empty value store.
func NewFakeSysctlExec() *FakeSysctlExec {
	return &FakeSysctlExec{
		values:    make(map[string]string),
		persisted: make(map[string]string),
	}
}

func (f *FakeSysctlExec) Get(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GetErr != nil {
		return "", f.GetErr
	}
	return f.values[key], nil
}

func (f *FakeSysctlExec) Set(_ context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SetErr != nil {
		return f.SetErr
	}
	f.values[key] = value
	return nil
}

func (f *FakeSysctlExec) Persist(_ context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.PersistErr != nil {
		return f.PersistErr
	}
	f.values[key] = value
	f.persisted[key] = value
	return nil
}

// PreSet places a key=value in the fake store (for test setup).
func (f *FakeSysctlExec) PreSet(key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = value
}

// GetSync returns the current value for key without going through the interface (for assertions).
func (f *FakeSysctlExec) GetSync(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values[key]
}

// IsPersisted returns whether key has been persisted.
func (f *FakeSysctlExec) IsPersisted(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.persisted[key]
	return ok
}
