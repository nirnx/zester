package exectest

import (
	"context"
	"sync"

	"github.com/ptorbus/zester/pkg/exec"
)

// FakeGroupExec is an in-memory fake for exec.GroupExec.
// It stores groups in a map and records operations for test assertions.
type FakeGroupExec struct {
	mu     sync.Mutex
	groups map[string]*exec.GroupInfo

	// Error injection fields — when set, the corresponding method returns the error.
	LookupErr error
	CreateErr error
	ModifyErr error
	DeleteErr error
}

// NewFakeGroupExec creates a FakeGroupExec with an empty group store.
func NewFakeGroupExec() *FakeGroupExec {
	return &FakeGroupExec{
		groups: make(map[string]*exec.GroupInfo),
	}
}

func (f *FakeGroupExec) Lookup(_ context.Context, name string) (*exec.GroupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.LookupErr != nil {
		return nil, f.LookupErr
	}
	g, ok := f.groups[name]
	if !ok {
		return nil, nil
	}
	cp := *g
	cp.Members = append([]string(nil), g.Members...)
	return &cp, nil
}

func (f *FakeGroupExec) Create(_ context.Context, opts exec.GroupCreateOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return f.CreateErr
	}
	f.groups[opts.Name] = &exec.GroupInfo{
		Name: opts.Name,
		GID:  opts.GID,
	}
	return nil
}

func (f *FakeGroupExec) Modify(_ context.Context, name string, opts exec.GroupModifyOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ModifyErr != nil {
		return f.ModifyErr
	}
	g, ok := f.groups[name]
	if !ok {
		return nil
	}
	if opts.GID != nil {
		g.GID = *opts.GID
	}
	for _, member := range opts.AddMembers {
		if !containsStr(g.Members, member) {
			g.Members = append(g.Members, member)
		}
	}
	for _, member := range opts.DelMembers {
		g.Members = removeStr(g.Members, member)
	}
	return nil
}

func (f *FakeGroupExec) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DeleteErr != nil {
		return f.DeleteErr
	}
	delete(f.groups, name)
	return nil
}

// PreCreate places a group in the fake store (for test setup).
func (f *FakeGroupExec) PreCreate(info *exec.GroupInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *info
	cp.Members = append([]string(nil), info.Members...)
	f.groups[info.Name] = &cp
}

// GetGroup returns the group info (for test assertions).
func (f *FakeGroupExec) GetGroup(name string) (*exec.GroupInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.groups[name]
	if !ok {
		return nil, false
	}
	cp := *g
	cp.Members = append([]string(nil), g.Members...)
	return &cp, true
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func removeStr(ss []string, s string) []string {
	result := make([]string, 0, len(ss))
	for _, v := range ss {
		if v != s {
			result = append(result, v)
		}
	}
	return result
}
