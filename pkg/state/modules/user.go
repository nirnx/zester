package modules

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// UserAbsent implements the user.absent state.
// It ensures that a user account does not exist.
//
// user.absent is not yet migrated to the self-documenting module-schema
// framework (a later wave); it keeps its legacy config-map constructor. Its
// sibling user.present lives in user_present.go.
type UserAbsent struct {
	id   string
	reqs state.Requisites

	UserName string
	Purge    bool // remove home directory
	Force    bool // force removal even if user is logged in

	user exec.UserExec
}

// NewUserAbsentBuilder returns a state.Builder that creates UserAbsent states.
func NewUserAbsentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.User == nil {
			return nil, fmt.Errorf("user.absent: no user provider available")
		}
		return newUserAbsent(id, config, mctx.User)
	}
}

func newUserAbsent(id string, config map[string]any, user exec.UserExec) (state.State, error) {
	u := &UserAbsent{id: id, user: user}

	u.UserName, _ = config["name"].(string)
	if u.UserName == "" {
		u.UserName = id
	}

	u.Purge, _ = config["purge"].(bool)
	u.Force, _ = config["force"].(bool)

	u.reqs = state.ParseRequisites(config)

	return u, nil
}

func (u *UserAbsent) Name() string           { return "user.absent:" + u.id }
func (u *UserAbsent) Reqs() state.Requisites { return u.reqs }

func (u *UserAbsent) Check(ctx context.Context) (state.CheckResult, error) {
	info, err := u.user.Lookup(ctx, u.UserName)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("user.absent: lookup %s: %w", u.UserName, err)
	}
	if info == nil {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("user %s exists and should be absent", u.UserName),
	}, nil
}

func (u *UserAbsent) Apply(ctx context.Context) (state.ApplyResult, error) {
	// Self-contained full flow: a watch-forced Apply bypasses Check, and
	// userdel on a nonexistent user exits non-zero — re-verify existence so
	// an already-absent user is a clean no-op, not a failure.
	info, err := u.user.Lookup(ctx, u.UserName)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("user.absent: lookup %s: %w", u.UserName, err)
	}
	if info == nil {
		return state.ApplyResult{
			Changed: false,
			Diff:    fmt.Sprintf("user %s already absent", u.UserName),
		}, nil
	}

	if err := u.user.Delete(ctx, u.UserName, u.Purge); err != nil {
		return state.ApplyResult{}, fmt.Errorf("user.absent: delete %s: %w", u.UserName, err)
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("deleted user %s", u.UserName),
		Details: map[string]string{"user": u.UserName, "purge": fmt.Sprintf("%v", u.Purge)},
	}, nil
}

func (u *UserAbsent) Revert(_ context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{
		Changed: false,
		Diff:    "cannot revert user deletion",
	}, nil
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int)
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
		if m[s] < 0 {
			return false
		}
	}
	return true
}
