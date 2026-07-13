package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	"github.com/nirnx/zester/pkg/state"
)

// UserPresent implements the user.present state.
// It ensures that a user account exists with the specified attributes.
//
// UserPresent is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). Two of
// them are named semantic types: GIDRef is a paramtypes.GroupRef (a gid given as
// a numeric GID or a group name — an all-digit string is a GID, BD-4) and Groups
// / OptionalGroups are paramtypes.StringList (a scalar list element is rendered
// to a string rather than silently dropped, BD-5). Password carries the
// `sensitive` option, so its value is redacted across the entire decode-error
// chain and never rendered into docs, defaults, or examples. The derived GID /
// PrimaryGroup runtime facets are exported (for test/contract projection) but
// UNTAGGED, so the schema compiler skips them — they are computed from GIDRef and
// PrimaryGroupParam by resolveGroupFacets, reproducing the legacy gid /
// primary_group precedence. The remaining unexported runtime fields are untagged.
type UserPresent struct {
	id   string
	reqs state.Requisites

	// Schema parameters (tagged): the module's declaration surface.
	UserName string `zester:"name,primary" usage:"username for the account; defaults to the state ID"`
	UID      int    `zester:"uid" usage:"numeric user ID; only compared and set when non-zero (0 auto-assigns)"`
	// GIDRef is the raw gid parameter: an integer or an all-digit string is a
	// numeric GID; any other string is a group name (which takes precedence over
	// primary_group). It is resolved into the GID / PrimaryGroup facets below.
	GIDRef paramtypes.GroupRef `zester:"gid" usage:"primary group as a numeric GID or a group name; an integer or all-digit string is a GID, any other string is a group name (taking precedence over primary_group)"`
	// PrimaryGroupParam is the primary_group parameter: the fallback group name
	// used only when gid is not itself a group name.
	PrimaryGroupParam string                `zester:"primary_group" usage:"primary group name; used only when gid is not itself a group name"`
	Groups            paramtypes.StringList `zester:"groups" usage:"supplementary groups the account should belong to"`
	OptionalGroups    paramtypes.StringList `zester:"optional_groups" usage:"supplementary groups added only if the account is already a member (absent groups are skipped, never created)"`
	Home              string                `zester:"home" usage:"home directory path; only compared and set when non-empty"`
	Shell             string                `zester:"shell" usage:"login shell path; only compared and set when non-empty"`
	CreateHome        bool                  `zester:"createhome" usage:"create the home directory when creating the account"`
	System            bool                  `zester:"system" usage:"create a system account (low UID range)"`
	Password          string                `zester:"password,sensitive" usage:"pre-hashed shadow password; converged by comparing the account's current shadow hash"`
	FullName          string                `zester:"fullname" usage:"GECOS full-name field; only compared and set when non-empty"`
	RemoveGroups      bool                  `zester:"remove_groups" usage:"remove supplementary groups not listed in groups (otherwise existing groups are preserved)"`

	// Derived runtime facets (untagged; computed by resolveGroupFacets from
	// GIDRef and PrimaryGroupParam). GID is the numeric primary GID; PrimaryGroup
	// is the name-based primary group. Check/Apply/Revert read these.
	GID          int
	PrimaryGroup string

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

// userPresentSpec is the compiled schema + documentation for user.present. It is
// compiled once at package init and executed by every decode path (the builder
// below, and Registry.Parse). The prose is drift-corrected against the live
// Check/Apply/Revert behavior.
var userPresentSpec = mustSpec("user.present", modschema.KindState, UserPresent{}, modschema.Doc{
	Summary: "Ensure a user account exists with the specified attributes.",
	Description: "`user.present` ensures the named account exists and converges its attributes " +
		"(uid, primary group, home, shell, full name, shadow password, and supplementary groups). " +
		"The username defaults to the state ID. The `gid` parameter is polymorphic: an integer — or " +
		"an all-digit string — is a numeric GID, while any other string is a group NAME, which takes " +
		"precedence over `primary_group` and is resolved through the group provider for drift " +
		"comparison. Only attributes that are actually specified are compared, so an unset field never " +
		"churns the account.",
	Effects: modschema.Effects{
		Check: "Reports a change when the account does not exist. For an existing account it compares, " +
			"in order: `uid` (only when non-zero); the primary group — a name-based primary group is " +
			"resolved to its GID and compared (a declared name that does not exist counts as drift), " +
			"taking precedence over a numeric `gid` (compared only when non-zero); `home`, `shell`, and " +
			"`fullname` (each only when non-empty); the shadow `password` (its hash is compared, never " +
			"shown — an unverifiable/empty provider hash under a declared password counts as drift); and " +
			"the supplementary `groups` (when declared, the desired list — `groups` plus any " +
			"`optional_groups` the account already belongs to, preserving existing groups unless " +
			"`remove_groups` is set — is compared against current membership).",
		Apply: "Creates the account with all specified attributes when it does not exist (recording the " +
			"creation for revert); a create that races another state at the same DAG level re-looks-up " +
			"and falls through to the modify path. For an existing account it builds a usermod set from " +
			"only the drifted attributes (a declared-but-missing primary group fails loudly before the " +
			"usermod); a fully converged account is a clean no-op that leaves the revert memo unarmed. " +
			"Reports the action (created/modified) and, on create/modify, the affected username.",
		Revert: "Undoes only what this run's Apply recorded. A user Apply created is deleted (with its " +
			"home directory); a user Apply modified is restored by diffing the current account against " +
			"the memoized original and reverting only the still-drifted attributes — including the " +
			"password when a prior hash was recorded (a password Apply set from an empty/unreadable " +
			"original hash is NOT restorable via usermod and is skipped with an explicit note rather " +
			"than a false \"restored\" claim). A fresh instance (a standalone revert) recorded nothing " +
			"and is an explicit clean no-op.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Create a user account",
			Kind:        "state",
			Explanation: "The username defaults to the state ID; createhome makes the home directory.",
			Code:        "appuser:\n  user.present:\n    - shell: /bin/bash\n    - home: /home/appuser\n    - createhome: true\n",
		},
		{
			Title:       "System account with a nologin shell",
			Kind:        "state",
			Explanation: "system: true allocates a low UID; the account cannot log in interactively.",
			Code:        "prometheus:\n  user.present:\n    - system: true\n    - shell: /usr/sbin/nologin\n    - home: /var/lib/prometheus\n    - createhome: true\n",
		},
		{
			Title: "Full specification with a numeric GID and groups",
			Kind:  "state",
			Explanation: "gid as an integer is a numeric primary GID; groups sets supplementary membership, " +
				"and optional_groups is applied only where the account already belongs.",
			Code: "deploy:\n  user.present:\n    - uid: 1500\n    - gid: 1500\n    - home: /opt/deploy\n" +
				"    - shell: /bin/bash\n    - createhome: true\n    - fullname: Deploy User\n" +
				"    - groups:\n      - docker\n      - wheel\n    - optional_groups:\n      - sudo\n" +
				"    - require:\n      - \"group.present:docker\"\n",
		},
		{
			Title:       "Create a user ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the username; key=value pairs set attributes.",
			Code:        "zester 'web*' user.present deploy uid=1500 home=/opt/deploy shell=/bin/bash createhome=true",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "gid is a GID or a group name",
			Body: "An integer or all-digit `gid` (for example `gid: 1000` or `gid: \"1000\"`) is a numeric " +
				"primary GID. Any other string is a group NAME, resolved through the group provider and " +
				"taking precedence over `primary_group`; `primary_group` is only the fallback used when " +
				"`gid` is not itself a name.",
		},
		{
			Level: "info",
			Title: "optional_groups and remove_groups",
			Body: "`optional_groups` adds a supplementary group only when the account is ALREADY a member " +
				"of it — a listed group the account does not belong to is skipped, never created. With " +
				"`remove_groups: false` (the default) existing supplementary groups are preserved; set it " +
				"true to prune any group not in `groups`.",
		},
		{
			Level: "info",
			Title: "password is a shadow hash and is sensitive",
			Body: "`password` is a pre-hashed shadow value, converged by comparing the account's current " +
				"shadow hash (the hash never appears in a diff). It is a sensitive parameter: its value is " +
				"redacted from every decode-error message and never rendered into docs, defaults, or " +
				"examples.",
		},
	},
	Divergences: []string{"BD-1", "BD-2", "BD-4", "BD-5", "BD-6", "BD-7"},
	SeeAlso:     []string{"user.absent", "group.present"},
})

// NewUserPresentBuilder returns a state.Builder that creates UserPresent states
// using the given ModuleContext's user (and, when a name-based primary group is
// declared, group) provider. Decode policy is threaded via opts; the peel
// supplies it through modules.RegisterAll.
func NewUserPresentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.User == nil {
			return nil, fmt.Errorf("user.present: no user provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected providers, id,
		// requisites, and the derived group facets MUST be assigned AFTER it.
		u := &UserPresent{}
		if _, err := userPresentSpec.Decode(id, config, u, opts); err != nil {
			return nil, fmt.Errorf("user.present: %w", err)
		}
		u.id = id
		u.user = mctx.User
		u.group = mctx.Group
		u.reqs = state.ParseRequisites(config)
		u.resolveGroupFacets()

		if u.PrimaryGroup != "" && mctx.Group == nil {
			return nil, fmt.Errorf("user.present: %s: no group provider available (required to verify primary group %q)", id, u.PrimaryGroup)
		}
		return u, nil
	}
}

// resolveGroupFacets derives the numeric GID and name-based PrimaryGroup runtime
// facets from the decoded gid (GroupRef) and primary_group parameters,
// reproducing the legacy precedence: a name-form gid wins as the primary group; a
// numeric gid sets GID; primary_group is the fallback used only when gid is not a
// name. (Under BD-4 an all-digit string gid resolves as a numeric GID, where the
// legacy string branch treated it as a group name.)
func (u *UserPresent) resolveGroupFacets() {
	if u.GIDRef.Declared() {
		if u.GIDRef.IsGID() {
			u.GID = u.GIDRef.GID()
		} else {
			u.PrimaryGroup = u.GIDRef.Name()
		}
	}
	if u.PrimaryGroup == "" {
		u.PrimaryGroup = u.PrimaryGroupParam
	}
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
