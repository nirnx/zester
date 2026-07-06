package modules

import (
	"context"

	"github.com/nirnx/zester/pkg/state"
)

// TestPing implements the test.ping state.
// It is a no-op "are you alive?" check that always succeeds.
type TestPing struct {
	id   string
	reqs state.Requisites
}

func NewTestPing(id string, config map[string]any) (state.State, error) {
	t := &TestPing{id: id}
	t.reqs = state.ParseRequisites(config)
	return t, nil
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
