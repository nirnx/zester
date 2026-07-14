package testmod

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// TestConfigurableTestState implements the test.configurable_test_state state:
// its result (success/failure) and change reporting are driven by config.
type TestConfigurableTestState struct {
	id   string
	reqs state.Requisites

	// Result selects success (true, the default) or failure (false).
	Result bool `zester:"result,default=true" usage:"whether the state succeeds; false makes it fail (with the comment message, or \"configured failure\"); defaults to true; a boolean that also accepts the integers 1 (true) and 0 (false)"`
	// Changes selects whether the state reports Changed: true.
	Changes bool `zester:"changes,default=true" usage:"whether the state reports Changed: true (honored on both success and failure); defaults to true; a boolean that also accepts the integers 1 (true) and 0 (false)"`
	// Comment is the message included in the result details / error.
	Comment string `zester:"comment" usage:"message included in the result details and, on failure, the error"`
}

// testConfigurableTestStateSpec is the compiled schema + documentation for
// test.configurable_test_state. Its Doc is drift-corrected against the live
// Check/Apply/Revert behavior.
var testConfigurableTestStateSpec = regdef.MustSpec("test.configurable_test_state", modschema.KindState, TestConfigurableTestState{}, modschema.Doc{
	Summary: "Result and change reporting fully driven by config.",
	Description: "`test.configurable_test_state` lets a state file dial in an exact outcome: `result` " +
		"selects success or failure and `changes` selects whether a change is reported — each " +
		"defaulting to true. Check always forces Apply, so the configuration alone controls the " +
		"reported result. `comment` customizes the message (and, on failure, the error).",
	Effects: modschema.Effects{
		Check: "Always reports that a change is needed so Apply runs and fully controls the reported " +
			"outcome.",
		Apply: "When `result` is true, succeeds with Changed set to `changes` and Details " +
			"`{\"result\": \"true\"}` (plus `comment` when set). When `result` is false, fails: returns " +
			"an error carrying `comment` (or `configured failure`) together with Changed set to " +
			"`changes` and Details `{\"result\": \"false\"}` (plus `comment` when set) — so a change and " +
			"the comment can be reported on failure too.",
		Revert: "No-op (Changed: false).",
	},
	Examples: []modschema.Example{
		{
			Title:       "Succeed without reporting a change",
			Kind:        "state",
			Explanation: "result true + changes false models a healthy, unchanged resource.",
			Code:        "scenario:\n  test.configurable_test_state:\n    - result: true\n    - changes: false\n    - comment: \"healthy, nothing changed\"\n",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Boolean parameters accept 1/0 and truthy strings",
			Body: "Under the uniform decoder `result` and `changes` accept a real boolean, the integers " +
				"`1` (true) and `0` (false), and the case-insensitive truthy/falsy strings " +
				"(`true`/`yes`/`on` and `false`/`no`/`off`) — so a reactor-dispatched or CLI value is " +
				"honored where the legacy `.(bool)` assertion silently kept the default. Any other value " +
				"(for example the integer `2`, or Salt's `Sometimes`/`Random`) is a decode error, not a " +
				"silent fallback.",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7"},
	SeeAlso:     []string{"test.succeed_with_changes", "test.fail_without_changes"},
})

// NewTestConfigurableTestStateBuilder returns a state.Builder for
// test.configurable_test_state.
func NewTestConfigurableTestStateBuilder(opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		t := &TestConfigurableTestState{}
		if _, err := testConfigurableTestStateSpec.Decode(id, config, t, opts); err != nil {
			return nil, fmt.Errorf("test.configurable_test_state: %w", err)
		}
		t.id = id
		t.reqs = state.ParseRequisites(config)
		return t, nil
	}
}

func (t *TestConfigurableTestState) Name() string           { return "test.configurable_test_state:" + t.id }
func (t *TestConfigurableTestState) Reqs() state.Requisites { return t.reqs }

func (t *TestConfigurableTestState) Check(_ context.Context) (state.CheckResult, error) {
	// Always run Apply so it fully controls the reported outcome.
	return state.CheckResult{NeedsChange: true}, nil
}

func (t *TestConfigurableTestState) Apply(_ context.Context) (state.ApplyResult, error) {
	details := map[string]string{
		"result": fmt.Sprintf("%t", t.Result),
	}
	if t.Comment != "" {
		details["comment"] = t.Comment
	}
	if !t.Result {
		msg := t.Comment
		if msg == "" {
			msg = "configured failure"
		}
		return state.ApplyResult{Changed: t.Changes, Details: details},
			fmt.Errorf("test.configurable_test_state: %s", msg)
	}
	return state.ApplyResult{
		Changed: t.Changes,
		Diff:    "test.configurable_test_state: applied",
		Details: details,
	}, nil
}

func (t *TestConfigurableTestState) Revert(_ context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{Changed: false}, nil
}
