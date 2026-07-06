package modules

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/state"
)

// These are Salt-compatible test helper states. Like test.ping, they need no
// exec providers, so their constructors are plain state.Builder functions
// rather than closure factories.

// TestNop implements the test.nop state: a no-op that always succeeds and
// reports no changes.
type TestNop struct {
	id   string
	reqs state.Requisites
}

// NewTestNop is a state.Builder for test.nop.
func NewTestNop(id string, config map[string]any) (state.State, error) {
	return &TestNop{id: id, reqs: state.ParseRequisites(config)}, nil
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

// TestFailWithoutChanges implements the test.fail_without_changes state:
// it always fails and reports no changes.
type TestFailWithoutChanges struct {
	id      string
	reqs    state.Requisites
	comment string
}

// NewTestFailWithoutChanges is a state.Builder for test.fail_without_changes.
func NewTestFailWithoutChanges(id string, config map[string]any) (state.State, error) {
	comment, _ := config["comment"].(string)
	return &TestFailWithoutChanges{
		id:      id,
		reqs:    state.ParseRequisites(config),
		comment: comment,
	}, nil
}

func (t *TestFailWithoutChanges) Name() string           { return "test.fail_without_changes:" + t.id }
func (t *TestFailWithoutChanges) Reqs() state.Requisites { return t.reqs }

func (t *TestFailWithoutChanges) Check(_ context.Context) (state.CheckResult, error) {
	// Force Apply so the failure is reported.
	return state.CheckResult{NeedsChange: true}, nil
}

func (t *TestFailWithoutChanges) Apply(_ context.Context) (state.ApplyResult, error) {
	msg := t.comment
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

// TestSucceedWithChanges implements the test.succeed_with_changes state:
// it always succeeds and reports that changes were made.
type TestSucceedWithChanges struct {
	id      string
	reqs    state.Requisites
	comment string
}

// NewTestSucceedWithChanges is a state.Builder for test.succeed_with_changes.
func NewTestSucceedWithChanges(id string, config map[string]any) (state.State, error) {
	comment, _ := config["comment"].(string)
	return &TestSucceedWithChanges{
		id:      id,
		reqs:    state.ParseRequisites(config),
		comment: comment,
	}, nil
}

func (t *TestSucceedWithChanges) Name() string           { return "test.succeed_with_changes:" + t.id }
func (t *TestSucceedWithChanges) Reqs() state.Requisites { return t.reqs }

func (t *TestSucceedWithChanges) Check(_ context.Context) (state.CheckResult, error) {
	return state.CheckResult{NeedsChange: true}, nil
}

func (t *TestSucceedWithChanges) Apply(_ context.Context) (state.ApplyResult, error) {
	details := map[string]string{"result": "true"}
	if t.comment != "" {
		details["comment"] = t.comment
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

// TestConfigurableTestState implements the test.configurable_test_state state:
// its result (success/failure) and change reporting are driven by config.
type TestConfigurableTestState struct {
	id      string
	reqs    state.Requisites
	result  bool
	changes bool
	comment string
}

// NewTestConfigurableTestState is a state.Builder for test.configurable_test_state.
// Config keys: result (bool, default true), changes (bool, default true),
// comment (string).
func NewTestConfigurableTestState(id string, config map[string]any) (state.State, error) {
	t := &TestConfigurableTestState{
		id:      id,
		reqs:    state.ParseRequisites(config),
		result:  true,
		changes: true,
	}
	if v, ok := config["result"].(bool); ok {
		t.result = v
	}
	if v, ok := config["changes"].(bool); ok {
		t.changes = v
	}
	t.comment, _ = config["comment"].(string)
	return t, nil
}

func (t *TestConfigurableTestState) Name() string           { return "test.configurable_test_state:" + t.id }
func (t *TestConfigurableTestState) Reqs() state.Requisites { return t.reqs }

func (t *TestConfigurableTestState) Check(_ context.Context) (state.CheckResult, error) {
	// Always run Apply so it fully controls the reported outcome.
	return state.CheckResult{NeedsChange: true}, nil
}

func (t *TestConfigurableTestState) Apply(_ context.Context) (state.ApplyResult, error) {
	details := map[string]string{
		"result": fmt.Sprintf("%t", t.result),
	}
	if t.comment != "" {
		details["comment"] = t.comment
	}
	if !t.result {
		msg := t.comment
		if msg == "" {
			msg = "configured failure"
		}
		return state.ApplyResult{Changed: t.changes, Details: details},
			fmt.Errorf("test.configurable_test_state: %s", msg)
	}
	return state.ApplyResult{
		Changed: t.changes,
		Diff:    "test.configurable_test_state: applied",
		Details: details,
	}, nil
}

func (t *TestConfigurableTestState) Revert(_ context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{Changed: false}, nil
}
