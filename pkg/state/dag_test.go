package state

import (
	"context"
	"testing"
)

// mockState is a minimal State implementation for testing the DAG.
type mockState struct {
	name string
	reqs Requisites
}

func (m *mockState) Name() string     { return m.name }
func (m *mockState) Reqs() Requisites { return m.reqs }

func (m *mockState) Check(ctx context.Context) (CheckResult, error) {
	return CheckResult{NeedsChange: true, Diff: "mock diff"}, nil
}

func (m *mockState) Apply(ctx context.Context) (ApplyResult, error) {
	return ApplyResult{Changed: true, Diff: "mock applied"}, nil
}

func (m *mockState) Revert(ctx context.Context) (ApplyResult, error) {
	return ApplyResult{Changed: true, Diff: "mock reverted"}, nil
}

func TestDAGLinearChain(t *testing.T) {
	// A -> B -> C (C requires B, B requires A)
	a := &mockState{name: "a"}
	b := &mockState{name: "b", reqs: Requisites{Require: []string{"a"}}}
	c := &mockState{name: "c", reqs: Requisites{Require: []string{"b"}}}

	dag, err := NewDAG([]State{c, a, b}) // intentionally unordered
	if err != nil {
		t.Fatalf("NewDAG: %v", err)
	}

	levels, err := dag.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(levels) != 3 {
		t.Fatalf("expected 3 levels, got %d", len(levels))
	}

	if levels[0].States[0].Name() != "a" {
		t.Errorf("level 0: expected 'a', got %q", levels[0].States[0].Name())
	}
	if levels[1].States[0].Name() != "b" {
		t.Errorf("level 1: expected 'b', got %q", levels[1].States[0].Name())
	}
	if levels[2].States[0].Name() != "c" {
		t.Errorf("level 2: expected 'c', got %q", levels[2].States[0].Name())
	}
}

func TestDAGParallelIndependent(t *testing.T) {
	// A, B, C are independent -> all in level 0
	a := &mockState{name: "a"}
	b := &mockState{name: "b"}
	c := &mockState{name: "c"}

	dag, err := NewDAG([]State{a, b, c})
	if err != nil {
		t.Fatalf("NewDAG: %v", err)
	}

	levels, err := dag.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(levels) != 1 {
		t.Fatalf("expected 1 level, got %d", len(levels))
	}
	if len(levels[0].States) != 3 {
		t.Fatalf("expected 3 states in level 0, got %d", len(levels[0].States))
	}
}

func TestDAGDiamond(t *testing.T) {
	// Diamond: A -> B, A -> C, B -> D, C -> D
	a := &mockState{name: "a"}
	b := &mockState{name: "b", reqs: Requisites{Require: []string{"a"}}}
	c := &mockState{name: "c", reqs: Requisites{Require: []string{"a"}}}
	d := &mockState{name: "d", reqs: Requisites{Require: []string{"b", "c"}}}

	dag, err := NewDAG([]State{d, c, b, a})
	if err != nil {
		t.Fatalf("NewDAG: %v", err)
	}

	levels, err := dag.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(levels) != 3 {
		t.Fatalf("expected 3 levels, got %d", len(levels))
	}

	// Level 0: a
	if len(levels[0].States) != 1 || levels[0].States[0].Name() != "a" {
		t.Errorf("level 0: expected [a], got %v", stateNames(levels[0].States))
	}

	// Level 1: b, c (parallel)
	if len(levels[1].States) != 2 {
		t.Errorf("level 1: expected 2 states, got %d", len(levels[1].States))
	}

	// Level 2: d
	if len(levels[2].States) != 1 || levels[2].States[0].Name() != "d" {
		t.Errorf("level 2: expected [d], got %v", stateNames(levels[2].States))
	}
}

func TestDAGCycleDetection(t *testing.T) {
	// A -> B -> A (cycle)
	a := &mockState{name: "a", reqs: Requisites{Require: []string{"b"}}}
	b := &mockState{name: "b", reqs: Requisites{Require: []string{"a"}}}

	dag, err := NewDAG([]State{a, b})
	if err != nil {
		t.Fatalf("NewDAG should succeed: %v", err)
	}

	_, err = dag.Resolve()
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
}

func TestDAGDuplicateName(t *testing.T) {
	a1 := &mockState{name: "a"}
	a2 := &mockState{name: "a"}

	_, err := NewDAG([]State{a1, a2})
	if err == nil {
		t.Fatal("expected duplicate name error")
	}
}

func TestDAGUnknownDependency(t *testing.T) {
	a := &mockState{name: "a", reqs: Requisites{Require: []string{"nonexistent"}}}

	_, err := NewDAG([]State{a})
	if err == nil {
		t.Fatal("expected unknown dependency error")
	}
}

func TestDAGOrder(t *testing.T) {
	a := &mockState{name: "a"}
	b := &mockState{name: "b", reqs: Requisites{Require: []string{"a"}}}
	c := &mockState{name: "c", reqs: Requisites{Require: []string{"a"}}}
	d := &mockState{name: "d", reqs: Requisites{Require: []string{"b", "c"}}}

	dag, err := NewDAG([]State{d, c, b, a})
	if err != nil {
		t.Fatalf("NewDAG: %v", err)
	}

	order, err := dag.Order()
	if err != nil {
		t.Fatalf("Order: %v", err)
	}

	if len(order) != 4 {
		t.Fatalf("expected 4 states, got %d", len(order))
	}

	// a must come before b and c; b and c must come before d
	pos := make(map[string]int)
	for i, s := range order {
		pos[s.Name()] = i
	}

	if pos["a"] >= pos["b"] || pos["a"] >= pos["c"] {
		t.Error("a must come before b and c")
	}
	if pos["b"] >= pos["d"] || pos["c"] >= pos["d"] {
		t.Error("b and c must come before d")
	}
}

func TestDAGGetAndLen(t *testing.T) {
	a := &mockState{name: "a"}
	b := &mockState{name: "b"}

	dag, err := NewDAG([]State{a, b})
	if err != nil {
		t.Fatalf("NewDAG: %v", err)
	}

	if dag.Len() != 2 {
		t.Errorf("Len: expected 2, got %d", dag.Len())
	}

	if dag.Get("a") == nil {
		t.Error("Get('a') returned nil")
	}
	if dag.Get("nonexistent") != nil {
		t.Error("Get('nonexistent') should return nil")
	}
}

func TestDAGEmpty(t *testing.T) {
	dag, err := NewDAG([]State{})
	if err != nil {
		t.Fatalf("NewDAG: %v", err)
	}

	levels, err := dag.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(levels) != 0 {
		t.Errorf("expected 0 levels, got %d", len(levels))
	}
}

func TestDAGAllRequisiteTypes(t *testing.T) {
	// Verify that all requisite types (require, watch, onchanges, onfail)
	// create proper ordering edges in the DAG.
	a := &mockState{name: "a"}
	b := &mockState{name: "b", reqs: Requisites{Require: []string{"a"}}}
	c := &mockState{name: "c", reqs: Requisites{Watch: []string{"a"}}}
	d := &mockState{name: "d", reqs: Requisites{OnChanges: []string{"b"}}}
	e := &mockState{name: "e", reqs: Requisites{OnFail: []string{"b"}}}
	// f has mixed requisites
	f := &mockState{name: "f", reqs: Requisites{
		Require: []string{"c"},
		Watch:   []string{"d"},
	}}

	dag, err := NewDAG([]State{f, e, d, c, b, a})
	if err != nil {
		t.Fatalf("NewDAG: %v", err)
	}

	order, err := dag.Order()
	if err != nil {
		t.Fatalf("Order: %v", err)
	}

	if len(order) != 6 {
		t.Fatalf("expected 6 states, got %d", len(order))
	}

	pos := make(map[string]int)
	for i, s := range order {
		pos[s.Name()] = i
	}

	// a must come before b (require) and c (watch)
	if pos["a"] >= pos["b"] {
		t.Error("a must come before b (require)")
	}
	if pos["a"] >= pos["c"] {
		t.Error("a must come before c (watch)")
	}
	// b must come before d (onchanges) and e (onfail)
	if pos["b"] >= pos["d"] {
		t.Error("b must come before d (onchanges)")
	}
	if pos["b"] >= pos["e"] {
		t.Error("b must come before e (onfail)")
	}
	// c and d must come before f (require + watch)
	if pos["c"] >= pos["f"] {
		t.Error("c must come before f (require)")
	}
	if pos["d"] >= pos["f"] {
		t.Error("d must come before f (watch)")
	}
}

func stateNames(states []State) []string {
	names := make([]string, len(states))
	for i, s := range states {
		names[i] = s.Name()
	}
	return names
}
