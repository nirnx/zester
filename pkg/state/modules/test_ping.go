package modules

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// TestPing implements the test.ping state — a no-op "are you alive?" check that
// always succeeds.
//
// TestPing is also its own schema proto: it declares NO parameters of its own
// (only untagged runtime fields), so the compiled schema records zero fields.
// The builder still decodes so that reserved keys are honored and the fleet-wide
// unknown-key policy applies uniformly.
type TestPing struct {
	id   string
	reqs state.Requisites
}

// testPingSpec is the compiled schema + documentation for test.ping. Its Doc is
// verified against the live Check/Apply/Revert behavior.
var testPingSpec = mustSpec("test.ping", modschema.KindState, TestPing{}, modschema.Doc{
	Summary: "A simple liveness check that always succeeds.",
	Description: "`test.ping` is a no-op \"are you alive?\" check — the Zester equivalent of Salt's " +
		"`test.ping`. It takes no parameters of its own and always succeeds, returning " +
		"`{\"result\": \"true\"}` in its result details.",
	Effects: modschema.Effects{
		Check: "Always reports that a change is needed, so the runner proceeds to Apply and the " +
			"result is returned.",
		Apply:  "Performs no work: returns Changed: false with Details `{\"result\": \"true\"}`.",
		Revert: "No-op (Changed: false) — there is nothing to undo.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Ping all peels",
			Kind:        "cli",
			Explanation: "The bare invocation checks liveness across every matched peel.",
			Code:        "zester '*' test.ping",
		},
		{
			Title:       "Ping a specific peel",
			Kind:        "cli",
			Explanation: "Target a single peel by id.",
			Code:        "zester 'web-01' test.ping",
		},
	},
	SeeAlso: []string{"test.nop"},
})

// NewTestPingBuilder returns a state.Builder for test.ping. It needs no exec
// providers; the decode policy (reserved keys, unknown-key handling) is threaded
// via opts from modules.RegisterAll.
func NewTestPingBuilder(opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		// Decode the (empty) parameter set first. Decode is transactional and
		// commits by replacing the whole struct, so id/reqs MUST be assigned
		// AFTER it — assigning them before would be overwritten by the committed
		// scratch value.
		t := &TestPing{}
		if _, err := testPingSpec.Decode(id, config, t, opts); err != nil {
			return nil, fmt.Errorf("test.ping: %w", err)
		}
		t.id = id
		t.reqs = state.ParseRequisites(config)
		return t, nil
	}
}

func (t *TestPing) Name() string           { return "test.ping:" + t.id }
func (t *TestPing) Reqs() state.Requisites { return t.reqs }

func (t *TestPing) Check(_ context.Context) (state.CheckResult, error) {
	// Always needs "change" so the runner calls Apply() and returns the result.
	return state.CheckResult{NeedsChange: true}, nil
}

func (t *TestPing) Apply(_ context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{
		Changed: false,
		Details: map[string]string{"result": "true"},
	}, nil
}

func (t *TestPing) Revert(_ context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{Changed: false}, nil
}
