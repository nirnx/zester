package testmod

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// These are Salt-compatible test helper states (test.nop, test.fail_without_changes,
// test.succeed_with_changes, test.configurable_test_state). Like test.ping, they need
// no exec providers, so their builders are opts-only factories (they thread the decode
// policy into spec.Decode but capture no ModuleContext). Each module IS its own schema
// proto: the tagged exported fields ARE its parameter declaration; the untagged runtime
// fields (id, reqs) are skipped by the schema compiler.

// TestNop implements the test.nop state: a no-op that always succeeds and
// reports no changes.
//
// TestNop declares no parameters of its own.
type TestNop struct {
	id   string
	reqs state.Requisites
}

// testNopSpec is the compiled schema + documentation for test.nop.
var testNopSpec = regdef.MustSpec("test.nop", modschema.KindState, TestNop{}, modschema.Doc{
	Summary: "A no-op that always succeeds and reports no changes.",
	Description: "`test.nop` is a placeholder state that always succeeds and reports no changes. " +
		"Check reports the state as already satisfied, so under normal ordering Apply does not " +
		"even run — useful as a requisite anchor or an inert placeholder.",
	Effects: modschema.Effects{
		Check: "Reports NeedsChange: false — the state is always already satisfied, so the runner " +
			"normally skips Apply entirely.",
		Apply: "If reached (for example via a watch-forced apply), performs no work: returns " +
			"Changed: false with Details `{\"result\": \"true\"}`.",
		Revert: "No-op (Changed: false).",
	},
	Examples: []modschema.Example{
		{
			Title:       "An inert placeholder",
			Kind:        "state",
			Explanation: "test.nop is satisfied immediately and never touches the system.",
			Code:        "placeholder:\n  test.nop: []\n",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Unimplemented Salt test states",
			Body: "Salt's `test.show_notification` and `test.mod_watch` behaviors are not implemented; " +
				"the Zester test helpers are `test.ping`, `test.nop`, `test.fail_without_changes`, " +
				"`test.succeed_with_changes`, and `test.configurable_test_state`.",
		},
	},
	SeeAlso: []string{"test.ping", "test.succeed_with_changes", "test.fail_without_changes", "test.configurable_test_state"},
})

// NewTestNopBuilder returns a state.Builder for test.nop.
func NewTestNopBuilder(opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		t := &TestNop{}
		if _, err := testNopSpec.Decode(id, config, t, opts); err != nil {
			return nil, fmt.Errorf("test.nop: %w", err)
		}
		t.id = id
		t.reqs = state.ParseRequisites(config)
		return t, nil
	}
}

func (t *TestNop) Name() string           { return "test.nop:" + t.id }
func (t *TestNop) Reqs() state.Requisites { return t.reqs }

func (t *TestNop) Check(_ context.Context) (state.CheckResult, error) {
	return state.CheckResult{NeedsChange: false}, nil
}

func (t *TestNop) Apply(_ context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{Changed: false, Details: map[string]string{"result": "true"}}, nil
}

func (t *TestNop) Revert(_ context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{Changed: false}, nil
}
