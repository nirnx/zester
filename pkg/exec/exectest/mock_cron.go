package exectest

import (
	"context"
	"sync"

	"github.com/nirnx/zester/pkg/exec"
)

// FakeCronExec is an in-memory fake for exec.CronExec.
// It stores entries per user and records operations for test assertions.
type FakeCronExec struct {
	mu      sync.Mutex
	entries map[string][]exec.CronEntry // key: user

	// Error injection — when set, the corresponding method returns this error.
	ListErr   error
	SetErr    error
	RemoveErr error
}

// NewFakeCronExec creates a FakeCronExec with an empty entry store.
func NewFakeCronExec() *FakeCronExec {
	return &FakeCronExec{
		entries: make(map[string][]exec.CronEntry),
	}
}

func (f *FakeCronExec) List(_ context.Context, user string) ([]exec.CronEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	src := f.entries[user]
	out := make([]exec.CronEntry, len(src))
	copy(out, src)
	return out, nil
}

func (f *FakeCronExec) Set(_ context.Context, user string, entry exec.CronEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SetErr != nil {
		return f.SetErr
	}
	entries := f.entries[user]
	// Same identity semantics as CrontabProvider.Set: comment (label) when
	// present, command fallback for comment-less entries.
	if i := exec.FindCronEntry(entries, entry); i >= 0 {
		entries[i] = entry
		f.entries[user] = entries
		return nil
	}
	f.entries[user] = append(entries, entry)
	return nil
}

func (f *FakeCronExec) Remove(_ context.Context, user string, command string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	entries := f.entries[user]
	filtered := entries[:0]
	for _, e := range entries {
		if e.Command != command {
			filtered = append(filtered, e)
		}
	}
	f.entries[user] = filtered
	return nil
}

// PreAdd places an entry in the fake store (for test setup).
func (f *FakeCronExec) PreAdd(user string, entry exec.CronEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[user] = append(f.entries[user], entry)
}

// ListSync returns entries for user without going through the interface (for assertions).
func (f *FakeCronExec) ListSync(user string) []exec.CronEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	src := f.entries[user]
	out := make([]exec.CronEntry, len(src))
	copy(out, src)
	return out
}
