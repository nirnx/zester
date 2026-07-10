package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// UserPresent implements the user.present state.
// It ensures that a user account exists with the specified attributes.
type UserPresent struct {
	id   string
	reqs state.Requisites

	UserName       string
	UID            int
	GID            int
	PrimaryGroup   string
	Groups         []string
	OptionalGroups []string
	Home           string
	Shell          string
	CreateHome     bool
	System         bool
	Password       string
	FullName       string
	RemoveGroups   bool

	user exec.UserExec
	// group resolves a name-based primary group to its GID for drift
	// comparison; required only when PrimaryGroup is declared.
	group exec.GroupExec

	// backup stores original user info for revert. original is armed ONLY
	// when Apply actually modified the user (a converged Apply must leave
	// Revert a clean no-op); originalHash/passwordChanged memo the password
	// facet, which exec.UserInfo does not carry.
	original        *exec.UserInfo
	originalHash    string
	passwordChanged bool
	wasCreated      bool
}

// NewUserPresentBuilder returns a state.Builder that creates UserPresent states.
func NewUserPresentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.User == nil {
			return nil, fmt.Errorf("user.present: no user provider available")
		}
		return newUserPresent(id, config, mctx.User, mctx.Group)
	}
}

func newUserPresent(id string, config map[string]any, user exec.UserExec, group exec.GroupExec) (state.State, error) {
	u := &UserPresent{id: id, user: user, group: group}

	u.UserName, _ = config["name"].(string)
	if u.UserName == "" {
		u.UserName = id
	}

	if v, ok := config["uid"].(int); ok {
		u.UID = v
	}
	if v, ok := config["gid"].(int); ok {
		u.GID = v
	} else if v, ok := config["gid"].(string); ok {
		// String gid is treated as a group name (Salt compatibility).
		u.PrimaryGroup = v
	}
	if u.PrimaryGroup == "" {
		u.PrimaryGroup, _ = config["primary_group"].(string)
	}
	u.Groups = parseAnyStringList(config, "groups")
	u.OptionalGroups = parseAnyStringList(config, "optional_groups")
	u.Home, _ = config["home"].(string)
	u.Shell, _ = config["shell"].(string)
	u.CreateHome, _ = config["createhome"].(bool)
	u.System, _ = config["system"].(bool)
	u.Password, _ = config["password"].(string)
	u.FullName, _ = config["fullname"].(string)
	u.RemoveGroups, _ = config["remove_groups"].(bool)

	if u.PrimaryGroup != "" && group == nil {
		return nil, fmt.Errorf("user.present: %s: no group provider available (required to verify primary group %q)", id, u.PrimaryGroup)
	}

	u.reqs = state.ParseRequisites(config)

	return u, nil
}

// primaryGroupDrift reports whether the user's primary group differs from the
// declared name-based PrimaryGroup, by resolving the group name to its GID.
// A declared group that does not exist counts as drift (Check reports it;
// Apply fails loudly before attempting a usermod that cannot succeed).
func (u *UserPresent) primaryGroupDrift(ctx context.Context, info *exec.UserInfo) (drift bool, groupMissing bool, err error) {
	ginfo, err := u.group.Lookup(ctx, u.PrimaryGroup)
	if err != nil {
		return false, false, fmt.Errorf("user.present: lookup group %s: %w", u.PrimaryGroup, err)
	}
	if ginfo == nil {
		return true, true, nil
	}
	return info.GID != ginfo.GID, false, nil
}

func (u *UserPresent) Name() string           { return "user.present:" + u.id }
func (u *UserPresent) Reqs() state.Requisites { return u.reqs }

func (u *UserPresent) Check(ctx context.Context) (state.CheckResult, error) {
	info, err := u.user.Lookup(ctx, u.UserName)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("user.present: lookup %s: %w", u.UserName, err)
	}

	if info == nil {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("user %s does not exist", u.UserName),
		}, nil
	}

	var diffs []string

	if u.UID != 0 && info.UID != u.UID {
		diffs = append(diffs, fmt.Sprintf("uid %d != %d", info.UID, u.UID))
	}
	// A declared name-based primary group takes precedence over a numeric
	// gid (mirroring UserCreateOpts, where PrimaryGroup overrides GID).
	if u.PrimaryGroup != "" {
		drift, missing, err := u.primaryGroupDrift(ctx, info)
		if err != nil {
			return state.CheckResult{}, err
		}
		switch {
		case missing:
			diffs = append(diffs, fmt.Sprintf("primary group %q does not exist", u.PrimaryGroup))
		case drift:
			diffs = append(diffs, fmt.Sprintf("primary group is not %q", u.PrimaryGroup))
		}
	} else if u.GID != 0 && info.GID != u.GID {
		diffs = append(diffs, fmt.Sprintf("gid %d != %d", info.GID, u.GID))
	}
	if u.Home != "" && info.Home != u.Home {
		diffs = append(diffs, fmt.Sprintf("home %s != %s", info.Home, u.Home))
	}
	if u.Shell != "" && info.Shell != u.Shell {
		diffs = append(diffs, fmt.Sprintf("shell %s != %s", info.Shell, u.Shell))
	}
	if u.FullName != "" && info.FullName != u.FullName {
		diffs = append(diffs, fmt.Sprintf("fullname %q != %q", info.FullName, u.FullName))
	}
	if u.Password != "" {
		hash, err := u.user.PasswordHash(ctx, u.UserName)
		if err != nil {
			return state.CheckResult{}, fmt.Errorf("user.present: password hash %s: %w", u.UserName, err)
		}
		// "" from the provider means none/cannot-verify — with a password
		// declared that is drift, and Apply's usermod -p is idempotent.
		// Hash values never appear in the diff.
		if hash != u.Password {
			diffs = append(diffs, "password hash differs from declared")
		}
	}
	if len(u.Groups) > 0 {
		desiredGroups := u.desiredGroups(info)
		if !stringSliceEqual(info.Groups, desiredGroups) {
			diffs = append(diffs, fmt.Sprintf("groups %v != %v", info.Groups, desiredGroups))
		}
	}

	if len(diffs) > 0 {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        strings.Join(diffs, "; "),
		}, nil
	}

	return state.CheckResult{NeedsChange: false}, nil
}

func (u *UserPresent) Apply(ctx context.Context) (state.ApplyResult, error) {
	info, err := u.user.Lookup(ctx, u.UserName)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("user.present: lookup %s: %w", u.UserName, err)
	}

	if info == nil {
		// Create user.
		createErr := u.user.Create(ctx, exec.UserCreateOpts{
			Name:         u.UserName,
			UID:          u.UID,
			GID:          u.GID,
			PrimaryGroup: u.PrimaryGroup,
			Groups:       u.Groups,
			Home:         u.Home,
			Shell:        u.Shell,
			CreateHome:   u.CreateHome,
			System:       u.System,
			Password:     u.Password,
			FullName:     u.FullName,
		})
		if createErr != nil {
			// Create failed — the user may have been created concurrently by
			// another state at the same DAG level. Re-lookup and fall through
			// to the modify path if the user now exists.
			info, err = u.user.Lookup(ctx, u.UserName)
			if err != nil {
				return state.ApplyResult{}, fmt.Errorf("user.present: lookup %s: %w", u.UserName, err)
			}
			if info == nil {
				return state.ApplyResult{}, fmt.Errorf("user.present: create %s: %w", u.UserName, createErr)
			}
			// Fall through to modify path.
		} else {
			u.wasCreated = true
			return state.ApplyResult{
				Changed: true,
				Diff:    fmt.Sprintf("created user %s", u.UserName),
				Details: map[string]string{"user": u.UserName, "action": "created"},
			}, nil
		}
	}

	// Modify existing user.
	opts := exec.UserModifyOpts{}
	changed := false
	passwordDrift := false
	var currentHash string

	if u.UID != 0 && info.UID != u.UID {
		opts.UID = &u.UID
		changed = true
	}
	// A declared name-based primary group takes precedence over a numeric
	// gid (mirroring UserCreateOpts, where PrimaryGroup overrides GID).
	if u.PrimaryGroup != "" {
		drift, missing, err := u.primaryGroupDrift(ctx, info)
		if err != nil {
			return state.ApplyResult{}, err
		}
		if missing {
			return state.ApplyResult{}, fmt.Errorf("user.present: primary group %q does not exist", u.PrimaryGroup)
		}
		if drift {
			opts.PrimaryGroup = &u.PrimaryGroup
			changed = true
		}
	} else if u.GID != 0 && info.GID != u.GID {
		opts.GID = &u.GID
		changed = true
	}
	if u.Home != "" && info.Home != u.Home {
		opts.Home = &u.Home
		changed = true
	}
	if u.Shell != "" && info.Shell != u.Shell {
		opts.Shell = &u.Shell
		changed = true
	}
	if u.Password != "" {
		// Compare the shadow hash so an in-sync password is a no-op — a
		// watch-forced Apply must not report a change it did not make.
		hash, err := u.user.PasswordHash(ctx, u.UserName)
		if err != nil {
			return state.ApplyResult{}, fmt.Errorf("user.present: password hash %s: %w", u.UserName, err)
		}
		if hash != u.Password {
			opts.Password = &u.Password
			currentHash = hash
			passwordDrift = true
			changed = true
		}
	}
	if u.FullName != "" && info.FullName != u.FullName {
		opts.FullName = &u.FullName
		changed = true
	}
	if len(u.Groups) > 0 {
		desired := u.desiredGroups(info)
		if !stringSliceEqual(info.Groups, desired) {
			opts.Groups = &desired
			changed = true
		}
	}

	if !changed {
		// Converged: no usermod, and the revert memo stays UNARMED — a later
		// Revert on this instance is a clean no-op, not a gratuitous usermod.
		return state.ApplyResult{Changed: false}, nil
	}

	if err := u.user.Modify(ctx, u.UserName, opts); err != nil {
		return state.ApplyResult{}, fmt.Errorf("user.present: modify %s: %w", u.UserName, err)
	}

	// Arm the revert memo only now that drift was actually applied.
	u.original = info
	if passwordDrift {
		u.originalHash = currentHash
		u.passwordChanged = true
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("modified user %s", u.UserName),
		Details: map[string]string{"user": u.UserName, "action": "modified"},
	}, nil
}

func (u *UserPresent) Revert(ctx context.Context) (state.ApplyResult, error) {
	if u.wasCreated {
		if err := u.user.Delete(ctx, u.UserName, true); err != nil {
			return state.ApplyResult{}, fmt.Errorf("user.present: revert delete %s: %w", u.UserName, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("deleted user %s (revert create)", u.UserName),
		}, nil
	}

	if u.original != nil {
		// Restore original attributes by diffing the CURRENT user against the
		// memoized original — only actual drift is reverted, and the reported
		// Diff reflects what was really done (mirrors group.present).
		info, err := u.user.Lookup(ctx, u.UserName)
		if err != nil {
			return state.ApplyResult{}, fmt.Errorf("user.present: revert lookup %s: %w", u.UserName, err)
		}
		if info == nil {
			return state.ApplyResult{
				Changed: false,
				Diff:    fmt.Sprintf("user %s no longer exists; nothing to restore", u.UserName),
			}, nil
		}

		opts := exec.UserModifyOpts{}
		changed := false
		if info.UID != u.original.UID {
			opts.UID = &u.original.UID
			changed = true
		}
		if info.GID != u.original.GID {
			opts.GID = &u.original.GID
			changed = true
		}
		if info.Home != u.original.Home {
			opts.Home = &u.original.Home
			changed = true
		}
		if info.Shell != u.original.Shell {
			opts.Shell = &u.original.Shell
			changed = true
		}
		if info.FullName != u.original.FullName {
			opts.FullName = &u.original.FullName
			changed = true
		}
		if !stringSliceEqual(info.Groups, u.original.Groups) {
			opts.Groups = &u.original.Groups
			changed = true
		}

		// The password facet: exec.UserInfo carries no hash, so it is memoized
		// separately when Apply changed it. An empty original hash (account had
		// none / shadow unreadable) is not safely restorable via usermod -p —
		// skip it and say so instead of silently leaving the Apply-set hash
		// behind a "reverted to original state" claim.
		passwordSkipped := false
		if u.passwordChanged {
			hash, err := u.user.PasswordHash(ctx, u.UserName)
			if err != nil {
				return state.ApplyResult{}, fmt.Errorf("user.present: revert password hash %s: %w", u.UserName, err)
			}
			if hash != u.originalHash {
				if u.originalHash != "" {
					opts.Password = &u.originalHash
					changed = true
				} else {
					passwordSkipped = true
				}
			}
		}

		if !changed {
			diff := fmt.Sprintf("user %s already matches original state", u.UserName)
			if passwordSkipped {
				diff += " (password not restored: no original hash recorded)"
			}
			return state.ApplyResult{
				Changed: false,
				Diff:    diff,
			}, nil
		}

		if err := u.user.Modify(ctx, u.UserName, opts); err != nil {
			return state.ApplyResult{}, fmt.Errorf("user.present: revert modify %s: %w", u.UserName, err)
		}
		diff := fmt.Sprintf("reverted user %s to original state", u.UserName)
		if passwordSkipped {
			diff += " (password not restored: no original hash recorded)"
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    diff,
		}, nil
	}

	return state.ApplyResult{
		Changed: false,
		Diff:    "nothing to revert (no apply recorded in this run)",
	}, nil
}

// desiredGroups computes the desired supplementary group list.
// It merges Groups and OptionalGroups (if they exist on the system).
// If RemoveGroups is false, existing groups are preserved.
func (u *UserPresent) desiredGroups(current *exec.UserInfo) []string {
	desired := make([]string, len(u.Groups))
	copy(desired, u.Groups)

	// Add optional groups only if they exist in the current groups
	// (meaning they exist on the system).
	for _, og := range u.OptionalGroups {
		if containsString(current.Groups, og) && !containsString(desired, og) {
			desired = append(desired, og)
		}
	}

	// If not removing groups, preserve existing groups that aren't in the desired list.
	if !u.RemoveGroups {
		for _, g := range current.Groups {
			if !containsString(desired, g) {
				desired = append(desired, g)
			}
		}
	}

	return desired
}

// UserAbsent implements the user.absent state.
// It ensures that a user account does not exist.
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

// parseAnyStringList extracts a string slice from a config map key that
// holds []any values (as produced by YAML parsing).
func parseAnyStringList(config map[string]any, key string) []string {
	raw, ok := config[key].([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, v := range raw {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
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
