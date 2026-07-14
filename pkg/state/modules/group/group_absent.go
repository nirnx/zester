package groupmod

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// GroupAbsent implements the group.absent state.
// It ensures that a group does not exist.
//
// GroupAbsent is also its own schema proto: the single tagged GroupName (primary)
// field IS its parameter declaration. The unexported runtime fields are untagged.
type GroupAbsent struct {
	id   string
	reqs state.Requisites

	GroupName string `zester:"name,primary" usage:"group name to remove; defaults to the state ID"`

	group exec.GroupExec
}

// groupAbsentSpec is the compiled schema + documentation for group.absent.
var groupAbsentSpec = regdef.MustSpec("group.absent", modschema.KindState, GroupAbsent{}, modschema.Doc{
	Summary: "Ensure a group does not exist on the system.",
	Description: "`group.absent` ensures the named group is removed. The group name defaults to the " +
		"state ID.",
	Effects: modschema.Effects{
		Check: "Looks up the group and reports a change only when it exists; an already-absent group " +
			"needs no change.",
		Apply: "Re-verifies existence (a self-contained flow: a watch-forced Apply bypasses Check, and " +
			"deleting a nonexistent group would fail), then deletes the group through the group provider. " +
			"An already-absent group is a clean no-op. Reports the group in its details.",
		Revert: "Cannot restore a deleted group (its GID and membership are not recorded), so Revert is " +
			"an explicit no-op.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Remove a group",
			Kind:        "state",
			Explanation: "The group name defaults to the state ID.",
			Code:        "oldgroup:\n  group.absent: []\n",
		},
		{
			Title:       "Remove a group after removing its users",
			Kind:        "state",
			Explanation: "require orders the user removals ahead of the group deletion.",
			Code: "decommission-team:\n  group.absent:\n    - name: oldteam\n    - require:\n" +
				"      - \"user.absent:alice\"\n      - \"user.absent:bob\"\n",
		},
		{
			Title:       "Remove a group ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the group name.",
			Code:        "zester '*' group.absent tempgroup",
		},
	},
	Divergences: []string{"BD-6"},
	SeeAlso:     []string{"group.present"},
})

// NewGroupAbsentBuilder returns a state.Builder that creates GroupAbsent states
// using the given ModuleContext's group provider. Decode policy is threaded via
// opts; the peel supplies it through modules.RegisterAll.
func NewGroupAbsentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Group == nil {
			return nil, fmt.Errorf("group.absent: no group provider available")
		}
		g := &GroupAbsent{}
		if _, err := groupAbsentSpec.Decode(id, config, g, opts); err != nil {
			return nil, fmt.Errorf("group.absent: %w", err)
		}
		g.id = id
		g.group = mctx.Group
		g.reqs = state.ParseRequisites(config)
		return g, nil
	}
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
	// Self-contained full flow: a watch-forced Apply bypasses Check, and
	// groupdel on a nonexistent group exits non-zero — re-verify existence so
	// an already-absent group is a clean no-op, not a failure.
	info, err := g.group.Lookup(ctx, g.GroupName)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("group.absent: lookup %s: %w", g.GroupName, err)
	}
	if info == nil {
		return state.ApplyResult{
			Changed: false,
			Diff:    fmt.Sprintf("group %s already absent", g.GroupName),
		}, nil
	}

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
