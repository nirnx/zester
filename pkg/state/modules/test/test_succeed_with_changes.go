package testmod

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// TestSucceedWithChanges implements the test.succeed_with_changes state:
// it always succeeds and reports that changes were made.
type TestSucceedWithChanges struct {
	id   string
	reqs state.Requisites

	// Comment is an optional message included in the result details.
	Comment string `zester:"comment" usage:"optional message included in the result details"`
}

// testSucceedWithChangesSpec is the compiled schema + documentation for
// test.succeed_with_changes.
var testSucceedWithChangesSpec = regdef.MustSpec("test.succeed_with_changes", modschema.KindState, TestSucceedWithChanges{}, modschema.Doc{
	Summary: "Always succeeds and reports that changes were made.",
	Description: "`test.succeed_with_changes` always succeeds and reports `Changed: true` — useful for " +
		"testing `watch` and `onchanges` chains. The optional `comment` parameter is included in the " +
		"result details when set.",
	Effects: modschema.Effects{
		Check: "Always reports that a change is needed so Apply runs.",
		Apply: "Always succeeds with Changed: true and Details `{\"result\": \"true\"}` (plus " +
			"`comment` when set).",
		Revert: "No-op (Changed: false).",
	},
	Examples: []modschema.Example{
		{
			Title:       "Simulate a change",
			Kind:        "state",
			Explanation: "The state always reports Changed: true.",
			Code:        "simulated-change:\n  test.succeed_with_changes: []\n",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Use in watch / onchanges chains",
			Body: "Hang a `watch` or `onchanges` requisite on another state so it reacts to this one's " +
				"reported change:\n\n" +
				"```yaml\nreactor:\n  cmd.run:\n    - command: echo \"reacting to change\"\n    - onchanges:\n" +
				"      - \"test.succeed_with_changes:simulated-change\"\n```",
		},
	},
	Divergences: []string{"BD-6"},
	SeeAlso:     []string{"test.fail_without_changes", "test.configurable_test_state"},
})

// NewTestSucceedWithChangesBuilder returns a state.Builder for
// test.succeed_with_changes.
func NewTestSucceedWithChangesBuilder(opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		t := &TestSucceedWithChanges{}
		if _, err := testSucceedWithChangesSpec.Decode(id, config, t, opts); err != nil {
			return nil, fmt.Errorf("test.succeed_with_changes: %w", err)
		}
		t.id = id
		t.reqs = state.ParseRequisites(config)
		return t, nil
	}
}

func (t *TestSucceedWithChanges) Name() string           { return "test.succeed_with_changes:" + t.id }
func (t *TestSucceedWithChanges) Reqs() state.Requisites { return t.reqs }

func (t *TestSucceedWithChanges) Check(_ context.Context) (state.CheckResult, error) {
	return state.CheckResult{NeedsChange: true}, nil
}

func (t *TestSucceedWithChanges) Apply(_ context.Context) (state.ApplyResult, error) {
	details := map[string]string{"result": "true"}
	if t.Comment != "" {
		details["comment"] = t.Comment
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    "test.succeed_with_changes: reported changes",
		Details: details,
	}, nil
}

func (t *TestSucceedWithChanges) Revert(_ context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{Changed: false}, nil
}
