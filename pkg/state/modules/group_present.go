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

// GroupPresent implements the group.present state.
// It ensures that a group exists with the specified attributes.
//
// GroupPresent is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). `gid` is a
// PLAIN int (no group-name overload — group.present never resolves names, unlike
// user.present's polymorphic gid), so it stays a primitive rather than a
// paramtypes.GroupRef. `members`/`addusers`/`delusers` are paramtypes.StringList
// (a scalar list element is rendered to a string rather than silently dropped, a
// bare-string value becomes a one-element list, and a nested element is rejected
// — BD-5). The unexported runtime fields (id, reqs, group, revert memos) are
// untagged, so the schema compiler skips them.
type GroupPresent struct {
	id   string
	reqs state.Requisites

	GroupName string                `zester:"name,primary" usage:"group name; defaults to the state ID"`
	GID       int                   `zester:"gid" usage:"numeric group ID; only compared and set when non-zero (0 auto-assigns)"`
	System    bool                  `zester:"system" usage:"create a system group (low GID range); used only during creation; a boolean that also accepts the integers 1 (true) and 0 (false)"`
	Members   paramtypes.StringList `zester:"members" usage:"exact membership list — extra members are removed and missing members are added"`
	AddUsers  paramtypes.StringList `zester:"addusers" usage:"users to add without affecting other existing members"`
	DelUsers  paramtypes.StringList `zester:"delusers" usage:"users to remove without affecting other existing members"`

	group exec.GroupExec

	// backup for revert.
	original   *exec.GroupInfo
	wasCreated bool
}

// groupPresentSpec is the compiled schema + documentation for group.present. Its
// Doc is drift-corrected against the live Check/Apply/Revert behavior — notably
// the Revert section, which DOES restore membership (the hand page wrongly
// claimed membership changes are not reverted).
var groupPresentSpec = mustSpec("group.present", modschema.KindState, GroupPresent{}, modschema.Doc{
	Summary: "Ensure a group exists with the specified attributes and membership.",
	Description: "`group.present` ensures the named group exists and converges its numeric GID and " +
		"membership. The group name defaults to the state ID. `gid` is a numeric group ID, compared and " +
		"set only when non-zero. `members` declares an EXACT membership (extra members are removed, " +
		"missing members added), while `addusers` and `delusers` adjust membership additively without " +
		"disturbing other members. `system` selects a low-GID system group and is honored only at " +
		"creation time.",
	Effects: modschema.Effects{
		Check: "Looks up the group. Reports a change when it does not exist; otherwise it compares, in " +
			"order: `gid` (only when non-zero); the exact `members` list (when declared); every " +
			"`addusers` entry (a listed user that is not a member is drift); and every `delusers` entry " +
			"(a listed user that IS a member is drift).",
		Apply: "Creates the group with the declared GID and system flag when it does not exist (adding " +
			"`members`/`addusers` afterward and recording the creation for revert); a create that races " +
			"another state at the same DAG level re-looks-up and falls through to the modify path. For an " +
			"existing group it builds a modify set from only the drifted GID and membership (arming the " +
			"revert memo), and a fully converged group is a clean no-op that leaves the memo unarmed. " +
			"Reports the group and the action (created/modified) in its details.",
		Revert: "Undoes only what this run's Apply recorded. A group Apply created is deleted; a group " +
			"Apply modified is restored by diffing the current group against the memoized original and " +
			"reverting only the still-drifted GID AND membership (so an added member is removed and a " +
			"removed member restored). A fresh instance (a standalone revert) recorded nothing and is an " +
			"explicit clean no-op.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Create a system group",
			Kind:        "state",
			Explanation: "The group name defaults to the state ID; system allocates a low GID.",
			Code:        "docker:\n  group.present:\n    - system: true\n",
		},
		{
			Title:       "Group with an exact membership list",
			Kind:        "state",
			Explanation: "members declares exact membership — users not listed are removed.",
			Code: "webadmins:\n  group.present:\n    - gid: 3000\n    - members:\n      - alice\n" +
				"      - bob\n      - charlie\n",
		},
		{
			Title:       "Add and remove members without disturbing others",
			Kind:        "state",
			Explanation: "addusers/delusers adjust membership additively; require orders user creation first.",
			Code: "developers:\n  group.present:\n    - addusers:\n      - newdev\n    - delusers:\n" +
				"      - formerdev\n    - require:\n      - \"user.present:newdev\"\n",
		},
		{
			Title:       "Create a group ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the group name; key=value pairs set attributes.",
			Code:        "zester 'db*' group.present dbadmin gid=2000",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "members is exact; addusers/delusers are additive",
			Body: "`members` enforces an EXACT membership — a user present on the system but absent from " +
				"the list is removed. Use `addusers`/`delusers` to adjust membership without touching " +
				"other members.",
		},
		{
			Level: "info",
			Title: "A bare-string member list is one member",
			Body: "A scalar `members: alice` decodes as the single-element list `[alice]` (and enables " +
				"exact-membership management), a mixed scalar list renders each element to a string, and " +
				"a nested list/map element is rejected rather than silently dropped (BD-5).",
		},
	},
	Divergences: []string{"BD-1", "BD-2", "BD-5", "BD-6", "BD-7"},
	SeeAlso:     []string{"group.absent", "user.present"},
})

// NewGroupPresentBuilder returns a state.Builder that creates GroupPresent states
// using the given ModuleContext's group provider. Decode policy is threaded via
// opts; the peel supplies it through modules.RegisterAll.
func NewGroupPresentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Group == nil {
			return nil, fmt.Errorf("group.present: no group provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it.
		g := &GroupPresent{}
		if _, err := groupPresentSpec.Decode(id, config, g, opts); err != nil {
			return nil, fmt.Errorf("group.present: %w", err)
		}
		g.id = id
		g.group = mctx.Group
		g.reqs = state.ParseRequisites(config)
		return g, nil
	}
}

func (g *GroupPresent) Name() string           { return "group.present:" + g.id }
func (g *GroupPresent) Reqs() state.Requisites { return g.reqs }

func (g *GroupPresent) Check(ctx context.Context) (state.CheckResult, error) {
	info, err := g.group.Lookup(ctx, g.GroupName)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("group.present: lookup %s: %w", g.GroupName, err)
	}

	if info == nil {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("group %s does not exist", g.GroupName),
		}, nil
	}

	var diffs []string

	if g.GID != 0 && info.GID != g.GID {
		diffs = append(diffs, fmt.Sprintf("gid %d != %d", info.GID, g.GID))
	}

	if len(g.Members) > 0 && !stringSliceEqual(info.Members, g.Members) {
		diffs = append(diffs, fmt.Sprintf("members %v != %v", info.Members, g.Members))
	}

	for _, u := range g.AddUsers {
		if !containsString(info.Members, u) {
			diffs = append(diffs, fmt.Sprintf("missing member %s", u))
		}
	}

	for _, u := range g.DelUsers {
		if containsString(info.Members, u) {
			diffs = append(diffs, fmt.Sprintf("unwanted member %s", u))
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

func (g *GroupPresent) Apply(ctx context.Context) (state.ApplyResult, error) {
	info, err := g.group.Lookup(ctx, g.GroupName)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("group.present: lookup %s: %w", g.GroupName, err)
	}

	if info == nil {
		createErr := g.group.Create(ctx, exec.GroupCreateOpts{
			Name:   g.GroupName,
			GID:    g.GID,
			System: g.System,
		})
		if createErr != nil {
			// Create failed — the group may have been created concurrently by
			// another state at the same DAG level. Re-lookup and fall through
			// to the modify path if the group now exists.
			info, err = g.group.Lookup(ctx, g.GroupName)
			if err != nil {
				return state.ApplyResult{}, fmt.Errorf("group.present: lookup %s: %w", g.GroupName, err)
			}
			if info == nil {
				return state.ApplyResult{}, fmt.Errorf("group.present: create %s: %w", g.GroupName, createErr)
			}
			// Fall through to modify path.
		} else {
			g.wasCreated = true

			// If members were specified, add them after creation.
			if len(g.Members) > 0 || len(g.AddUsers) > 0 {
				addList := append([]string{}, g.Members...)
				for _, u := range g.AddUsers {
					if !containsString(addList, u) {
						addList = append(addList, u)
					}
				}
				if len(addList) > 0 {
					if err := g.group.Modify(ctx, g.GroupName, exec.GroupModifyOpts{
						AddMembers: addList,
					}); err != nil {
						return state.ApplyResult{}, fmt.Errorf("group.present: add members to %s: %w", g.GroupName, err)
					}
				}
			}

			return state.ApplyResult{
				Changed: true,
				Diff:    fmt.Sprintf("created group %s", g.GroupName),
				Details: map[string]string{"group": g.GroupName, "action": "created"},
			}, nil
		}
	}

	// Modify existing group.
	g.original = info
	opts := exec.GroupModifyOpts{}
	changed := false

	if g.GID != 0 && info.GID != g.GID {
		opts.GID = &g.GID
		changed = true
	}

	// Compute members to add/remove.
	if len(g.Members) > 0 {
		// Explicit member list: ensure exact membership.
		for _, m := range g.Members {
			if !containsString(info.Members, m) {
				opts.AddMembers = append(opts.AddMembers, m)
				changed = true
			}
		}
		for _, m := range info.Members {
			if !containsString(g.Members, m) {
				opts.DelMembers = append(opts.DelMembers, m)
				changed = true
			}
		}
	}

	for _, u := range g.AddUsers {
		if !containsString(info.Members, u) && !containsString(opts.AddMembers, u) {
			opts.AddMembers = append(opts.AddMembers, u)
			changed = true
		}
	}
	for _, u := range g.DelUsers {
		if containsString(info.Members, u) && !containsString(opts.DelMembers, u) {
			opts.DelMembers = append(opts.DelMembers, u)
			changed = true
		}
	}

	if !changed {
		return state.ApplyResult{Changed: false}, nil
	}

	if err := g.group.Modify(ctx, g.GroupName, opts); err != nil {
		return state.ApplyResult{}, fmt.Errorf("group.present: modify %s: %w", g.GroupName, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("modified group %s", g.GroupName),
		Details: map[string]string{"group": g.GroupName, "action": "modified"},
	}, nil
}

func (g *GroupPresent) Revert(ctx context.Context) (state.ApplyResult, error) {
	if g.wasCreated {
		if err := g.group.Delete(ctx, g.GroupName); err != nil {
			return state.ApplyResult{}, fmt.Errorf("group.present: revert delete %s: %w", g.GroupName, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("deleted group %s (revert create)", g.GroupName),
		}, nil
	}

	if g.original != nil {
		// Restore original GID and membership by diffing the CURRENT group
		// against the memoized original — only actual drift is reverted, and
		// the reported Diff reflects what was really done.
		info, err := g.group.Lookup(ctx, g.GroupName)
		if err != nil {
			return state.ApplyResult{}, fmt.Errorf("group.present: revert lookup %s: %w", g.GroupName, err)
		}
		if info == nil {
			return state.ApplyResult{
				Changed: false,
				Diff:    fmt.Sprintf("group %s no longer exists; nothing to restore", g.GroupName),
			}, nil
		}

		opts := exec.GroupModifyOpts{}
		changed := false
		if info.GID != g.original.GID {
			opts.GID = &g.original.GID
			changed = true
		}
		for _, m := range g.original.Members {
			if !containsString(info.Members, m) {
				opts.AddMembers = append(opts.AddMembers, m)
				changed = true
			}
		}
		for _, m := range info.Members {
			if !containsString(g.original.Members, m) {
				opts.DelMembers = append(opts.DelMembers, m)
				changed = true
			}
		}

		if !changed {
			return state.ApplyResult{
				Changed: false,
				Diff:    fmt.Sprintf("group %s already matches original state", g.GroupName),
			}, nil
		}

		if err := g.group.Modify(ctx, g.GroupName, opts); err != nil {
			return state.ApplyResult{}, fmt.Errorf("group.present: revert modify %s: %w", g.GroupName, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("reverted group %s to original state", g.GroupName),
		}, nil
	}

	return state.ApplyResult{
		Changed: false,
		Diff:    "nothing to revert (no apply recorded in this run)",
	}, nil
}
