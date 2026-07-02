package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
)

// GroupPresent implements the group.present state.
// It ensures that a group exists with the specified attributes.
type GroupPresent struct {
	id   string
	reqs state.Requisites

	GroupName string
	GID       int
	System    bool
	Members   []string
	AddUsers  []string
	DelUsers  []string

	group exec.GroupExec

	// backup for revert.
	original   *exec.GroupInfo
	wasCreated bool
}

// NewGroupPresentBuilder returns a state.Builder that creates GroupPresent states.
func NewGroupPresentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Group == nil {
			return nil, fmt.Errorf("group.present: no group provider available")
		}
		return newGroupPresent(id, config, mctx.Group)
	}
}

func newGroupPresent(id string, config map[string]any, group exec.GroupExec) (state.State, error) {
	g := &GroupPresent{id: id, group: group}

	g.GroupName, _ = config["name"].(string)
	if g.GroupName == "" {
		g.GroupName = id
	}

	if v, ok := config["gid"].(int); ok {
		g.GID = v
	}
	g.System, _ = config["system"].(bool)
	g.Members = parseAnyStringList(config, "members")
	g.AddUsers = parseAnyStringList(config, "addusers")
	g.DelUsers = parseAnyStringList(config, "delusers")

	g.reqs = state.ParseRequisites(config)

	return g, nil
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
		// Restore original GID and membership.
		opts := exec.GroupModifyOpts{}
		if g.GID != 0 {
			opts.GID = &g.original.GID
		}
		if err := g.group.Modify(ctx, g.GroupName, opts); err != nil {
			return state.ApplyResult{}, fmt.Errorf("group.present: revert modify %s: %w", g.GroupName, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("reverted group %s to original state", g.GroupName),
		}, nil
	}

	return state.ApplyResult{Changed: false}, nil
}

// GroupAbsent implements the group.absent state.
// It ensures that a group does not exist.
type GroupAbsent struct {
	id   string
	reqs state.Requisites

	GroupName string

	group exec.GroupExec
}

// NewGroupAbsentBuilder returns a state.Builder that creates GroupAbsent states.
func NewGroupAbsentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Group == nil {
			return nil, fmt.Errorf("group.absent: no group provider available")
		}
		return newGroupAbsent(id, config, mctx.Group)
	}
}

func newGroupAbsent(id string, config map[string]any, group exec.GroupExec) (state.State, error) {
	g := &GroupAbsent{id: id, group: group}

	g.GroupName, _ = config["name"].(string)
	if g.GroupName == "" {
		g.GroupName = id
	}

	g.reqs = state.ParseRequisites(config)

	return g, nil
}

func (g *GroupAbsent) Name() string           { return "group.absent:" + g.id }
func (g *GroupAbsent) Reqs() state.Requisites { return g.reqs }

func (g *GroupAbsent) Check(ctx context.Context) (state.CheckResult, error) {
	info, err := g.group.Lookup(ctx, g.GroupName)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("group.absent: lookup %s: %w", g.GroupName, err)
	}
	if info == nil {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("group %s exists and should be absent", g.GroupName),
	}, nil
}

func (g *GroupAbsent) Apply(ctx context.Context) (state.ApplyResult, error) {
	if err := g.group.Delete(ctx, g.GroupName); err != nil {
		return state.ApplyResult{}, fmt.Errorf("group.absent: delete %s: %w", g.GroupName, err)
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("deleted group %s", g.GroupName),
		Details: map[string]string{"group": g.GroupName},
	}, nil
}

func (g *GroupAbsent) Revert(_ context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{
		Changed: false,
		Diff:    "cannot revert group deletion",
	}, nil
}
