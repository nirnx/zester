package testmod

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// TestFailWithoutChanges implements the test.fail_without_changes state:
// it always fails and reports no changes.
type TestFailWithoutChanges struct {
	id   string
	reqs state.Requisites

	// Comment is the message included in the error and result details; it
	// defaults to "failure without changes" at use time (an empty Comment means
	// "use the built-in message").
	Comment string `zester:"comment" usage:"message included in the error and result details; defaults to \"failure without changes\""`
}

// testFailWithoutChangesSpec is the compiled schema + documentation for
// test.fail_without_changes.
var testFailWithoutChangesSpec = regdef.MustSpec("test.fail_without_changes", modschema.KindState, TestFailWithoutChanges{}, modschema.Doc{
	Summary: "Always fails, reporting no changes.",
	Description: "`test.fail_without_changes` always fails and reports no changes — useful for testing " +
		"`onfail` chains and failure propagation. The `comment` parameter customizes the error " +
		"message; when omitted the built-in message `failure without changes` is used.",
	Effects: modschema.Effects{
		Check: "Always reports that a change is needed so Apply runs and the failure is surfaced.",
		Apply: "Always fails: returns an error carrying `comment` (or `failure without changes` when " +
			"unset) together with Details `{\"result\": \"false\", \"comment\": <message>}`, and " +
			"Changed: false.",
		Revert: "No-op (Changed: false).",
	},
	Examples: []modschema.Example{
		{
			Title:       "Simulate a failure",
			Kind:        "state",
			Explanation: "The comment becomes the error message.",
			Code:        "simulated-failure:\n  test.fail_without_changes:\n    - comment: \"primary service unavailable\"\n",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Use in onfail chains",
			Body: "Hang an `onfail` requisite on another state so it runs only when this one fails:\n\n" +
				"```yaml\nfallback:\n  cmd.run:\n    - command: /usr/local/bin/failover.sh\n    - onfail:\n" +
				"      - \"test.fail_without_changes:simulated-failure\"\n```",
		},
	},
	Divergences: []string{"BD-6"},
	SeeAlso:     []string{"test.succeed_with_changes", "test.configurable_test_state"},
})

// NewTestFailWithoutChangesBuilder returns a state.Builder for
// test.fail_without_changes.
func NewTestFailWithoutChangesBuilder(opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		t := &TestFailWithoutChanges{}
		if _, err := testFailWithoutChangesSpec.Decode(id, config, t, opts); err != nil {
			return nil, fmt.Errorf("test.fail_without_changes: %w", err)
		}
		t.id = id
		t.reqs = state.ParseRequisites(config)
		return t, nil
	}
}

func (t *TestFailWithoutChanges) Name() string           { return "test.fail_without_changes:" + t.id }
func (t *TestFailWithoutChanges) Reqs() state.Requisites { return t.reqs }

func (t *TestFailWithoutChanges) Check(_ context.Context) (state.CheckResult, error) {
	// Force Apply so the failure is reported.
	return state.CheckResult{NeedsChange: true}, nil
}

func (t *TestFailWithoutChanges) Apply(_ context.Context) (state.ApplyResult, error) {
	msg := t.Comment
	if msg == "" {
		msg = "failure without changes"
	}
	return state.ApplyResult{
			Changed: false,
			Details: map[string]string{"result": "false", "comment": msg},
		},
		fmt.Errorf("test.fail_without_changes: %s", msg)
}

func (t *TestFailWithoutChanges) Revert(_ context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{Changed: false}, nil
}
