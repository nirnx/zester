package state

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// trackingState records execution order for parallel testing.
type trackingState struct {
	name      string
	reqs      Requisites
	order     *atomic.Int64
	execOrder int64
	checkErr  error
	applyErr  error
	delay     time.Duration
}

func (s *trackingState) Name() string     { return s.name }
func (s *trackingState) Reqs() Requisites { return s.reqs }

func (s *trackingState) Check(ctx context.Context) (CheckResult, error) {
	if s.checkErr != nil {
		return CheckResult{}, s.checkErr
	}
	return CheckResult{NeedsChange: true, Diff: "needs change"}, nil
}

func (s *trackingState) Apply(ctx context.Context) (ApplyResult, error) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	s.execOrder = s.order.Add(1)
	if s.applyErr != nil {
		return ApplyResult{}, s.applyErr
	}
	return ApplyResult{Changed: true, Diff: "applied"}, nil
}

func (s *trackingState) Revert(ctx context.Context) (ApplyResult, error) {
	s.execOrder = s.order.Add(1)
	return ApplyResult{Changed: true, Diff: "reverted"}, nil
}

// noChangeState is a state where Check returns NeedsChange: false.
// Useful for testing watch-no-trigger and onchanges-skip scenarios.
type noChangeState struct {
	name  string
	reqs  Requisites
	order *atomic.Int64
}

func (s *noChangeState) Name() string     { return s.name }
func (s *noChangeState) Reqs() Requisites { return s.reqs }

func (s *noChangeState) Check(_ context.Context) (CheckResult, error) {
	return CheckResult{NeedsChange: false, Diff: "already correct"}, nil
}

func (s *noChangeState) Apply(_ context.Context) (ApplyResult, error) {
	if s.order != nil {
		s.order.Add(1)
	}
	return ApplyResult{Changed: false}, nil
}

func (s *noChangeState) Revert(_ context.Context) (ApplyResult, error) {
	return ApplyResult{Changed: false}, nil
}

func TestRunnerApplyLinearChain(t *testing.T) {
	order := &atomic.Int64{}

	a := &trackingState{name: "a", order: order}
	b := &trackingState{name: "b", reqs: Requisites{Require: []string{"a"}}, order: order}
	c := &trackingState{name: "c", reqs: Requisites{Require: []string{"b"}}, order: order}

	runner := NewRunner(nil)
	result, err := runner.Run(context.Background(), []State{c, a, b}, ModeApply)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !result.Success() {
		t.Error("expected success")
	}
	if result.Changed != 3 {
		t.Errorf("expected 3 changed, got %d", result.Changed)
	}

	// Verify ordering: a before b before c.
	if a.execOrder >= b.execOrder || b.execOrder >= c.execOrder {
		t.Errorf("wrong order: a=%d b=%d c=%d", a.execOrder, b.execOrder, c.execOrder)
	}
}

func TestRunnerApplyParallel(t *testing.T) {
	order := &atomic.Int64{}

	// Three independent states should all run in the first level.
	a := &trackingState{name: "a", order: order, delay: 10 * time.Millisecond}
	b := &trackingState{name: "b", order: order, delay: 10 * time.Millisecond}
	c := &trackingState{name: "c", order: order, delay: 10 * time.Millisecond}

	runner := NewRunner(nil)
	start := time.Now()
	result, err := runner.Run(context.Background(), []State{a, b, c}, ModeApply)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !result.Success() {
		t.Error("expected success")
	}
	if result.Changed != 3 {
		t.Errorf("expected 3 changed, got %d", result.Changed)
	}

	// If run in parallel, total time should be much less than 30ms.
	if elapsed > 25*time.Millisecond {
		t.Errorf("states did not appear to run in parallel: %v", elapsed)
	}
}

func TestRunnerCheckMode(t *testing.T) {
	order := &atomic.Int64{}
	a := &trackingState{name: "a", order: order}

	runner := NewRunner(nil)
	result, err := runner.Run(context.Background(), []State{a}, ModeCheck)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Changed != 1 {
		t.Errorf("expected 1 changed (needs change), got %d", result.Changed)
	}
	// In check mode, Apply should NOT be called.
	if a.execOrder != 0 {
		t.Error("Apply should not have been called in check mode")
	}
}

func TestRunnerRevertMode(t *testing.T) {
	order := &atomic.Int64{}

	a := &trackingState{name: "a", order: order}
	b := &trackingState{name: "b", reqs: Requisites{Require: []string{"a"}}, order: order}

	runner := NewRunner(nil)
	result, err := runner.Run(context.Background(), []State{a, b}, ModeRevert)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Changed != 2 {
		t.Errorf("expected 2 changed, got %d", result.Changed)
	}

	// In revert mode, b should run before a (reverse order).
	if b.execOrder >= a.execOrder {
		t.Errorf("revert order wrong: b=%d a=%d", b.execOrder, a.execOrder)
	}
}

func TestRunnerFailedDependency(t *testing.T) {
	order := &atomic.Int64{}

	a := &trackingState{name: "a", order: order, applyErr: fmt.Errorf("disk full")}
	b := &trackingState{name: "b", reqs: Requisites{Require: []string{"a"}}, order: order}

	runner := NewRunner(nil)
	result, err := runner.Run(context.Background(), []State{a, b}, ModeApply)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Success() {
		t.Error("expected failure")
	}
	if result.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", result.Failed)
	}
	if result.Skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", result.Skipped)
	}

	sr := result.States["b"]
	if sr == nil {
		t.Fatal("state 'b' result missing")
	}
	if !sr.Skipped {
		t.Error("state 'b' should be skipped")
	}
	if sr.SkipReason != "require_failed" {
		t.Errorf("expected SkipReason 'require_failed', got %q", sr.SkipReason)
	}
}

func TestRunnerContextCancellation(t *testing.T) {
	order := &atomic.Int64{}

	a := &trackingState{name: "a", order: order, delay: 200 * time.Millisecond}
	b := &trackingState{name: "b", reqs: Requisites{Require: []string{"a"}}, order: order}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	runner := NewRunner(nil)
	result, err := runner.Run(ctx, []State{a, b}, ModeApply)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// b should be skipped due to context cancellation (a may or may not complete).
	sr := result.States["b"]
	if sr == nil {
		t.Fatal("state 'b' result missing")
	}
	if !sr.Skipped {
		t.Error("state 'b' should be skipped after context cancellation")
	}
}

func TestRunnerCheckError(t *testing.T) {
	order := &atomic.Int64{}
	a := &trackingState{name: "a", order: order, checkErr: fmt.Errorf("check failed")}

	runner := NewRunner(nil)
	result, err := runner.Run(context.Background(), []State{a}, ModeApply)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", result.Failed)
	}

	sr := result.States["a"]
	if sr == nil {
		t.Fatal("state 'a' result missing")
	}
	if sr.Error == "" {
		t.Error("expected error in state result")
	}
}

func TestRunResultSuccess(t *testing.T) {
	r := &RunResult{Failed: 0}
	if !r.Success() {
		t.Error("expected Success() true when Failed=0")
	}

	r.Failed = 1
	if r.Success() {
		t.Error("expected Success() false when Failed=1")
	}
}

// --- Requisite tests ---

func TestRunnerWatchForceApply(t *testing.T) {
	// When a watched state changes, the watcher should force Apply
	// (skipping the Check phase).
	order := &atomic.Int64{}

	// "a" changes (NeedsChange=true, Apply returns Changed=true)
	a := &trackingState{name: "a", order: order}
	// "b" watches "a" — should force Apply even if Check says no change needed
	b := &noChangeState{name: "b", reqs: Requisites{Watch: []string{"a"}}, order: order}

	runner := NewRunner(nil)
	result, err := runner.Run(context.Background(), []State{a, b}, ModeApply)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !result.Success() {
		t.Error("expected success")
	}

	srB := result.States["b"]
	if srB == nil {
		t.Fatal("state 'b' result missing")
	}
	// b's Check would return NeedsChange=false, but watch forces Apply.
	// Apply on noChangeState returns Changed=false, but it must not be skipped.
	if srB.Skipped {
		t.Error("state 'b' should not be skipped (watch triggered)")
	}
}

func TestRunnerWatchNoChangeNoForce(t *testing.T) {
	// When a watched state did NOT change, watch acts like require
	// (no force apply). The watcher's own Check determines behavior.
	order := &atomic.Int64{}

	// "a" does not change (Check returns NeedsChange=false)
	a := &noChangeState{name: "a", reqs: Requisites{}, order: order}
	// "b" watches "a" — since "a" didn't change, no force apply.
	// "b" itself also has NeedsChange=false, so it should not apply.
	b := &noChangeState{name: "b", reqs: Requisites{Watch: []string{"a"}}, order: order}

	runner := NewRunner(nil)
	result, err := runner.Run(context.Background(), []State{a, b}, ModeApply)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !result.Success() {
		t.Error("expected success")
	}

	srB := result.States["b"]
	if srB == nil {
		t.Fatal("state 'b' result missing")
	}
	// "a" didn't change, so "b" should not be force-applied.
	// "b" Check returns NeedsChange=false, so Changed should be false.
	if srB.Changed {
		t.Error("state 'b' should not report changed (no watch trigger, no change needed)")
	}
}

func TestRunnerWatchDepFailed(t *testing.T) {
	// Watch skips when dep failed (same as require).
	order := &atomic.Int64{}

	a := &trackingState{name: "a", order: order, applyErr: fmt.Errorf("disk full")}
	b := &trackingState{name: "b", reqs: Requisites{Watch: []string{"a"}}, order: order}

	runner := NewRunner(nil)
	result, err := runner.Run(context.Background(), []State{a, b}, ModeApply)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	srB := result.States["b"]
	if srB == nil {
		t.Fatal("state 'b' result missing")
	}
	if !srB.Skipped {
		t.Error("state 'b' should be skipped when watched dep failed")
	}
	if srB.SkipReason != "require_failed" {
		t.Errorf("expected SkipReason 'require_failed', got %q", srB.SkipReason)
	}
}

func TestRunnerOnChangesTriggered(t *testing.T) {
	// OnChanges runs when the listed dep changed.
	order := &atomic.Int64{}

	a := &trackingState{name: "a", order: order} // changes
	b := &trackingState{name: "b", reqs: Requisites{OnChanges: []string{"a"}}, order: order}

	runner := NewRunner(nil)
	result, err := runner.Run(context.Background(), []State{a, b}, ModeApply)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !result.Success() {
		t.Error("expected success")
	}

	srB := result.States["b"]
	if srB == nil {
		t.Fatal("state 'b' result missing")
	}
	if srB.Skipped {
		t.Error("state 'b' should not be skipped (onchanges dep changed)")
	}
	if !srB.Changed {
		t.Error("state 'b' should report changed")
	}
}

func TestRunnerOnChangesSkipped(t *testing.T) {
	// OnChanges skips when the listed dep did NOT change.
	order := &atomic.Int64{}

	a := &noChangeState{name: "a", reqs: Requisites{}, order: order} // no change
	b := &trackingState{name: "b", reqs: Requisites{OnChanges: []string{"a"}}, order: order}

	runner := NewRunner(nil)
	result, err := runner.Run(context.Background(), []State{a, b}, ModeApply)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	srB := result.States["b"]
	if srB == nil {
		t.Fatal("state 'b' result missing")
	}
	if !srB.Skipped {
		t.Error("state 'b' should be skipped (onchanges not met)")
	}
	if srB.SkipReason != "onchanges_not_met" {
		t.Errorf("expected SkipReason 'onchanges_not_met', got %q", srB.SkipReason)
	}
}

func TestRunnerOnFailTriggered(t *testing.T) {
	// OnFail runs when the listed dep failed.
	order := &atomic.Int64{}

	a := &trackingState{name: "a", order: order, applyErr: fmt.Errorf("boom")}
	b := &trackingState{name: "b", reqs: Requisites{OnFail: []string{"a"}}, order: order}

	runner := NewRunner(nil)
	result, err := runner.Run(context.Background(), []State{a, b}, ModeApply)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	srB := result.States["b"]
	if srB == nil {
		t.Fatal("state 'b' result missing")
	}
	if srB.Skipped {
		t.Error("state 'b' should not be skipped (onfail dep failed)")
	}
	if !srB.Changed {
		t.Error("state 'b' should report changed")
	}
}

func TestRunnerOnFailSkipped(t *testing.T) {
	// OnFail skips when the listed dep succeeded.
	order := &atomic.Int64{}

	a := &trackingState{name: "a", order: order} // succeeds
	b := &trackingState{name: "b", reqs: Requisites{OnFail: []string{"a"}}, order: order}

	runner := NewRunner(nil)
	result, err := runner.Run(context.Background(), []State{a, b}, ModeApply)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	srB := result.States["b"]
	if srB == nil {
		t.Fatal("state 'b' result missing")
	}
	if !srB.Skipped {
		t.Error("state 'b' should be skipped (onfail not met)")
	}
	if srB.SkipReason != "onfail_not_met" {
		t.Errorf("expected SkipReason 'onfail_not_met', got %q", srB.SkipReason)
	}
}

func TestRunnerMixedRequisites(t *testing.T) {
	// A state with both require and watch.
	// "a" succeeds and changes, "b" succeeds but doesn't change.
	// "c" requires "b" and watches "a". Since "a" changed, forceApply should be set.
	order := &atomic.Int64{}

	a := &trackingState{name: "a", order: order}                     // changes
	b := &noChangeState{name: "b", reqs: Requisites{}, order: order} // no change
	c := &noChangeState{name: "c", reqs: Requisites{
		Require: []string{"b"},
		Watch:   []string{"a"},
	}, order: order}

	runner := NewRunner(nil)
	result, err := runner.Run(context.Background(), []State{a, b, c}, ModeApply)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !result.Success() {
		t.Error("expected success")
	}

	srC := result.States["c"]
	if srC == nil {
		t.Fatal("state 'c' result missing")
	}
	// c's Check returns NeedsChange=false, but watch on "a" forces Apply.
	if srC.Skipped {
		t.Error("state 'c' should not be skipped")
	}
}
