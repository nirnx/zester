package state

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseStateAttributes(t *testing.T) {
	cfg := map[string]any{
		"onlyif":   "test -f /x",
		"unless":   []any{"test -f /y", "test -f /z"},
		"order":    5,
		"failhard": true,
		"retry":    map[string]any{"attempts": 3, "interval": 2},
		"prereq":   []any{map[string]any{"pkg": "nginx"}},
	}
	a := ParseStateAttributes(cfg)

	if len(a.Onlyif) != 1 || a.Onlyif[0] != "test -f /x" {
		t.Errorf("Onlyif = %v", a.Onlyif)
	}
	if len(a.Unless) != 2 {
		t.Errorf("Unless = %v", a.Unless)
	}
	if !a.hasOrder || a.Order != 5 {
		t.Errorf("Order = %d (hasOrder=%v)", a.Order, a.hasOrder)
	}
	if !a.FailHard {
		t.Error("FailHard should be true")
	}
	if a.RetryAttempts != 3 || a.RetryInterval != 2*time.Second {
		t.Errorf("retry = %d/%v", a.RetryAttempts, a.RetryInterval)
	}
	if len(a.Prereq) != 1 || a.Prereq[0] != "pkg.installed:nginx" {
		t.Errorf("Prereq = %v", a.Prereq)
	}
	if a.IsZero() {
		t.Error("IsZero should be false")
	}
}

func TestParseStateAttributes_OrderKeywords(t *testing.T) {
	if a := ParseStateAttributes(map[string]any{"order": "first"}); a.Order != -1_000_000 {
		t.Errorf("order first = %d", a.Order)
	}
	if a := ParseStateAttributes(map[string]any{"order": "last"}); a.Order != 1_000_000 {
		t.Errorf("order last = %d", a.Order)
	}
	if a := ParseStateAttributes(map[string]any{"retry": 4}); a.RetryAttempts != 4 || a.RetryInterval != 10*time.Second {
		t.Errorf("bare retry = %d/%v", a.RetryAttempts, a.RetryInterval)
	}
	if !ParseStateAttributes(map[string]any{}).IsZero() {
		t.Error("empty config should be zero attributes")
	}
}

// stubGuard records commands and returns configured exit codes.
type stubGuard struct {
	codes map[string]int
	calls []string
	err   error
}

func (g *stubGuard) RunGuard(_ context.Context, cmd string) (int, error) {
	g.calls = append(g.calls, cmd)
	if g.err != nil {
		return 1, g.err
	}
	return g.codes[cmd], nil
}

func TestWrapAttributes_Guards(t *testing.T) {
	order := &atomic.Int64{}
	inner := func() State { return &trackingState{name: "cmd.run:x", order: order} }

	tests := []struct {
		name    string
		attrs   StateAttributes
		codes   map[string]int
		wantRun bool // whether Check delegates (NeedsChange true)
	}{
		{"onlyif-pass", StateAttributes{Onlyif: []string{"c"}}, map[string]int{"c": 0}, true},
		{"onlyif-fail", StateAttributes{Onlyif: []string{"c"}}, map[string]int{"c": 1}, false},
		{"unless-skip", StateAttributes{Unless: []string{"c"}}, map[string]int{"c": 0}, false},
		{"unless-run", StateAttributes{Unless: []string{"c"}}, map[string]int{"c": 3}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &stubGuard{codes: tt.codes}
			w := WrapAttributes(inner(), tt.attrs, g)
			cr, err := w.Check(context.Background())
			if err != nil {
				t.Fatalf("Check error: %v", err)
			}
			if cr.NeedsChange != tt.wantRun {
				t.Errorf("NeedsChange = %v, want %v", cr.NeedsChange, tt.wantRun)
			}
			// Apply must also honor the guard (watch-forced path).
			ar, err := w.Apply(context.Background())
			if err != nil {
				t.Fatalf("Apply error: %v", err)
			}
			if tt.wantRun && !ar.Changed {
				t.Error("guard passed but Apply reported no change")
			}
			if !tt.wantRun && ar.Changed {
				t.Error("guard failed but Apply changed the system")
			}
		})
	}
}

func TestWrapAttributes_ZeroReturnsInner(t *testing.T) {
	order := &atomic.Int64{}
	inner := &trackingState{name: "cmd.run:x", order: order}
	got := WrapAttributes(inner, StateAttributes{}, nil)
	if got != State(inner) {
		t.Error("zero attributes should return the inner state unchanged")
	}
}

// failCountState fails a configured number of times, then succeeds.
type failCountState struct {
	name       string
	reqs       Requisites
	failFirst  int
	attempts   int32
	order      int
	failhard   bool
	retryN     int
	retryEvery time.Duration
}

func (s *failCountState) Name() string     { return s.name }
func (s *failCountState) Reqs() Requisites { return s.reqs }
func (s *failCountState) Check(context.Context) (CheckResult, error) {
	return CheckResult{NeedsChange: true}, nil
}
func (s *failCountState) Apply(context.Context) (ApplyResult, error) {
	n := atomic.AddInt32(&s.attempts, 1)
	if int(n) <= s.failFirst {
		return ApplyResult{}, errors.New("boom")
	}
	return ApplyResult{Changed: true}, nil
}
func (s *failCountState) Revert(context.Context) (ApplyResult, error) { return ApplyResult{}, nil }
func (s *failCountState) Order() int                                  { return s.order }
func (s *failCountState) FailHard() bool                              { return s.failhard }
func (s *failCountState) RetrySpec() (int, time.Duration)             { return s.retryN, s.retryEvery }

func TestRunner_Retry(t *testing.T) {
	s := &failCountState{name: "cmd.run:r", failFirst: 2, retryN: 3, retryEvery: time.Millisecond}
	r := NewRunner(nil)
	res, err := r.Run(context.Background(), []State{s}, ModeApply)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 {
		t.Fatalf("expected success after retries, failed=%d", res.Failed)
	}
	if got := atomic.LoadInt32(&s.attempts); got != 3 {
		t.Errorf("attempts = %d, want 3 (2 fail + 1 success)", got)
	}
}

func TestRunner_RetryExhausted(t *testing.T) {
	s := &failCountState{name: "cmd.run:r", failFirst: 5, retryN: 2, retryEvery: time.Millisecond}
	res, _ := NewRunner(nil).Run(context.Background(), []State{s}, ModeApply)
	if res.Failed != 1 {
		t.Errorf("expected failure, failed=%d", res.Failed)
	}
	if got := atomic.LoadInt32(&s.attempts); got != 3 {
		t.Errorf("attempts = %d, want 3 (1 + 2 retries)", got)
	}
}

func TestRunner_FailHardAborts(t *testing.T) {
	// Level 0: hard (failhard, fails) + b (succeeds). Level 1: later requires b.
	// failhard must abort the whole run, so `later` is skipped with
	// failhard_abort even though its own dependency (b) succeeded.
	order := &atomic.Int64{}
	hard := &failCountState{name: "cmd.run:a", failFirst: 99, failhard: true}
	b := &trackingState{name: "cmd.run:b", order: order}
	later := &trackingState{name: "cmd.run:later", order: order, reqs: Requisites{Require: []string{"cmd.run:b"}}}

	res, _ := NewRunner(nil).Run(context.Background(), []State{hard, b, later}, ModeApply)
	if res.Failed != 1 {
		t.Errorf("failed = %d, want 1", res.Failed)
	}
	sr := res.States["cmd.run:later"]
	if sr == nil || !sr.Skipped || sr.SkipReason != "failhard_abort" {
		t.Errorf("later should be failhard_abort skipped, got %+v", sr)
	}
	if later.execOrder != 0 {
		t.Error("later must not have executed after failhard abort")
	}
}

func TestRunner_TestModeDoesNotApply(t *testing.T) {
	order := &atomic.Int64{}
	s := &trackingState{name: "cmd.run:x", order: order}
	res, _ := NewRunner(nil).Run(context.Background(), []State{s}, ModeTest)
	if res.Changed != 1 {
		t.Errorf("test mode should report 1 would-change, got %d", res.Changed)
	}
	if s.execOrder != 0 {
		t.Error("test mode must not call Apply")
	}
}
