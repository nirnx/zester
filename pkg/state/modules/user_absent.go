package modules

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// UserAbsent implements the user.absent state.
// It ensures that a user account does not exist.
//
// UserAbsent is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). `purge`
// and `force` are plain bools — under the uniform decoder an integer (`1`/`0`)
// or a truthy/falsy string now coerces to the flag (BD-2/BD-7) where the legacy
// `config[...].(bool)` assertion silently dropped it. Nothing here is sensitive
// (a username and two flags are public config). `force` is decoded for Salt
// compatibility but the execution layer does not yet consume it. The unexported
// runtime fields (id, reqs, user) are untagged, so the schema compiler skips
// them. Its sibling user.present lives in user_present.go; the shared slice
// helpers live in user.go.
type UserAbsent struct {
	id   string
	reqs state.Requisites

	// UserName is the account to remove; it defaults to the state ID.
	UserName string `zester:"name,primary" usage:"username of the account to remove; defaults to the state ID"`
	// Purge also removes the home directory and mail spool (userdel -r).
	Purge bool `zester:"purge" usage:"also remove the user's home directory and mail spool (userdel -r); a boolean that also accepts the integers 1 (true) and 0 (false)"`
	// Force is parsed for Salt compatibility but not yet wired into the
	// execution layer, so it currently has no effect.
	Force bool `zester:"force" usage:"parsed for Salt compatibility but not yet implemented in the execution layer — currently has no effect; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	user exec.UserExec
}

// userAbsentSpec is the compiled schema + documentation for user.absent. It is
// compiled once at package init and executed by every decode path (the builder
// below, and Registry.Parse). The prose is drift-corrected against the live
// Check/Apply/Revert behavior (notably: Apply RE-VERIFIES existence so an
// already-absent user is a clean no-op, not a userdel failure).
var userAbsentSpec = mustSpec("user.absent", modschema.KindState, UserAbsent{}, modschema.Doc{
	Summary: "Ensure a user account does not exist on the system.",
	Description: "`user.absent` ensures the named user account is removed. The username defaults to the " +
		"state ID. With `purge` the account's home directory and mail spool are removed as well " +
		"(`userdel -r`); without it only the account is deleted. `force` is accepted for Salt " +
		"compatibility but is not yet wired into the execution layer, so it currently has no effect.",
	Effects: modschema.Effects{
		Check: "Looks up the user by name and reports a change only when the account exists; an " +
			"already-absent user needs no change.",
		Apply: "Re-verifies existence (a self-contained flow: a watch-forced Apply bypasses Check, and " +
			"`userdel` on a nonexistent user exits non-zero), then deletes the account through the user " +
			"provider — removing the home directory and mail spool as well when `purge` is set " +
			"(`userdel -r`). An already-absent user is a clean no-op. Reports the username and the purge " +
			"flag in its details.",
		Revert: "Cannot restore a deleted user: the original account data (password hash, groups, uid) is " +
			"not recorded and cannot be re-derived, so Revert is an explicit no-op.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Remove a user",
			Kind:        "state",
			Explanation: "The username defaults to the state ID; the home directory is left in place.",
			Code:        "olduser:\n  user.absent: []\n",
		},
		{
			Title:       "Remove a user and purge the home directory",
			Kind:        "state",
			Explanation: "purge runs userdel -r, removing the home directory and mail spool too.",
			Code:        "remove-temp-user:\n  user.absent:\n    - name: tempuser\n    - purge: true\n",
		},
		{
			Title:       "Remove a user after stopping their service",
			Kind:        "state",
			Explanation: "require orders the service stop ahead of the account removal.",
			Code: "decommission-app:\n  user.absent:\n    - name: appuser\n    - purge: true\n" +
				"    - require:\n      - \"cmd.run:stop-app-service\"\n",
		},
		{
			Title:       "Remove a user ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the username; purge=true also removes the home directory.",
			Code:        "zester 'web*' user.absent olduser purge=true",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "purge removes the home directory",
			Body: "`purge` runs `userdel -r`, removing the account's home directory and mail spool as well " +
				"as the account; without it only the account is deleted and the home directory is left in " +
				"place.",
		},
		{
			Level: "info",
			Title: "force is not yet implemented",
			Body: "`force` is accepted for Salt compatibility but is not yet wired into the execution layer " +
				"(it does not forward `userdel -f`), so it currently has no effect.",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7"},
	SeeAlso:     []string{"user.present"},
})

// NewUserAbsentBuilder returns a state.Builder that creates UserAbsent states
// using the given ModuleContext's user provider. Decode policy (unknown-key
// handling, reserved keys) is threaded via opts; the peel supplies it through
// modules.RegisterAll.
func NewUserAbsentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.User == nil {
			return nil, fmt.Errorf("user.absent: no user provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		u := &UserAbsent{}
		if _, err := userAbsentSpec.Decode(id, config, u, opts); err != nil {
			return nil, fmt.Errorf("user.absent: %w", err)
		}
		u.id = id
		u.user = mctx.User
		u.reqs = state.ParseRequisites(config)
		return u, nil
	}
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
