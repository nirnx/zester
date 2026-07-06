package exectest

import (
	"context"
	"sync"

	"github.com/nirnx/zester/pkg/exec"
)

// FakeUserExec is an in-memory fake for exec.UserExec.
// It stores users in a map and records operations for test assertions.
type FakeUserExec struct {
	mu    sync.Mutex
	users map[string]*exec.UserInfo

	// Error injection fields — when set, the corresponding method returns the error.
	LookupErr error
	CreateErr error
	ModifyErr error
	DeleteErr error
}

// NewFakeUserExec creates a FakeUserExec with an empty user store.
func NewFakeUserExec() *FakeUserExec {
	return &FakeUserExec{
		users: make(map[string]*exec.UserInfo),
	}
}

func (f *FakeUserExec) Lookup(_ context.Context, name string) (*exec.UserInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.LookupErr != nil {
		return nil, f.LookupErr
	}
	u, ok := f.users[name]
	if !ok {
		return nil, nil
	}
	cp := *u
	cp.Groups = append([]string(nil), u.Groups...)
	return &cp, nil
}

func (f *FakeUserExec) Create(_ context.Context, opts exec.UserCreateOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return f.CreateErr
	}
	f.users[opts.Name] = &exec.UserInfo{
		Name:     opts.Name,
		UID:      opts.UID,
		GID:      opts.GID,
		Groups:   append([]string(nil), opts.Groups...),
		Home:     opts.Home,
		Shell:    opts.Shell,
		FullName: opts.FullName,
	}
	return nil
}

func (f *FakeUserExec) Modify(_ context.Context, name string, opts exec.UserModifyOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ModifyErr != nil {
		return f.ModifyErr
	}
	u, ok := f.users[name]
	if !ok {
		return nil
	}
	if opts.UID != nil {
		u.UID = *opts.UID
	}
	if opts.GID != nil {
		u.GID = *opts.GID
	}
	if opts.Groups != nil {
		u.Groups = append([]string(nil), *opts.Groups...)
	}
	if opts.Home != nil {
		u.Home = *opts.Home
	}
	if opts.Shell != nil {
		u.Shell = *opts.Shell
	}
	if opts.FullName != nil {
		u.FullName = *opts.FullName
	}
	if opts.Password != nil {
		// Password is not stored in UserInfo, but we accept it without error.
	}
	return nil
}

func (f *FakeUserExec) Delete(_ context.Context, name string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DeleteErr != nil {
		return f.DeleteErr
	}
	delete(f.users, name)
	return nil
}

// PreCreate places a user in the fake store (for test setup).
func (f *FakeUserExec) PreCreate(info *exec.UserInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *info
	cp.Groups = append([]string(nil), info.Groups...)
	f.users[info.Name] = &cp
}

// GetUser returns the user info (for test assertions).
func (f *FakeUserExec) GetUser(name string) (*exec.UserInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[name]
	if !ok {
		return nil, false
	}
	cp := *u
	cp.Groups = append([]string(nil), u.Groups...)
	return &cp, true
}
