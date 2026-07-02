package state

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// RunMode controls how the runner applies states.
type RunMode int

const (
	// ModeApply checks and applies states (default).
	ModeApply RunMode = iota

	// ModeCheck only checks whether changes are needed, without applying.
	// This is the engine behind Salt-style test=True dry runs: it reports
	// which states would change without modifying the system.
	ModeCheck

	// ModeRevert reverts previously applied states in reverse order.
	ModeRevert
)

// ModeTest is an alias for ModeCheck: a read-only run that reports which
// states would change (Salt test=True).
const ModeTest = ModeCheck

// RunResult is the aggregated result of running all states.
type RunResult struct {
	// States contains per-state results keyed by state name.
	States map[string]*StateResult

	// TotalDuration is the wall-clock duration of the entire run.
	TotalDuration time.Duration

	// Changed is the count of states that made changes.
	Changed int

	// Failed is the count of states that errored.
	Failed int

	// Skipped is the count of states skipped due to failed dependencies.
	Skipped int

	// Canceled is true when the run context was canceled before completion.
	Canceled bool
}

// Success returns true if no states failed.
func (r *RunResult) Success() bool {
	return r.Failed == 0 && !r.Canceled
}

// Runner executes a set of states respecting their dependency order.
// Independent states within the same DAG level run in parallel.
type Runner struct {
	logger *slog.Logger
}

// NewRunner creates a state runner.
func NewRunner(logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{logger: logger}
}

// Run executes the given states according to the specified mode.
// States are resolved into a DAG and executed level by level.
// Within each level, independent states run concurrently.
func (r *Runner) Run(ctx context.Context, states []State, mode RunMode) (*RunResult, error) {
	start := time.Now()

	dag, err := NewDAG(states)
	if err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}

	levels, err := dag.Resolve()
	if err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}

	// For revert, reverse the level order.
	if mode == ModeRevert {
		for i, j := 0, len(levels)-1; i < j; i, j = i+1, j-1 {
			levels[i], levels[j] = levels[j], levels[i]
		}
	}

	result := &RunResult{
		States: make(map[string]*StateResult, dag.Len()),
	}

	failed := make(map[string]bool)
	changed := make(map[string]bool)
	aborted := false // set when a failhard state fails

	for _, level := range levels {
		// Within a level, honor explicit `order:` (lower first), then name
		// for determinism. States without an order sort as 0.
		ordered := make([]State, len(level.States))
		copy(ordered, level.States)
		sort.SliceStable(ordered, func(a, b int) bool {
			oa, ob := orderOf(ordered[a]), orderOf(ordered[b])
			if oa != ob {
				return oa < ob
			}
			return ordered[a].Name() < ordered[b].Name()
		})

		if aborted || ctx.Err() != nil {
			if ctx.Err() != nil {
				result.Canceled = true
			}
			for _, s := range ordered {
				sr := &StateResult{Name: s.Name(), Skipped: true}
				if aborted {
					sr.SkipReason = "failhard_abort"
				}
				result.States[s.Name()] = sr
				result.Skipped++
			}
			continue
		}

		var wg sync.WaitGroup
		var mu sync.Mutex
		results := make([]*StateResult, len(ordered))

		for i, s := range ordered {
			reqs := s.Reqs()

			if mode == ModeRevert {
				// Revert mode: only require-failure propagation.
				depFailed := false
				for _, dep := range reqs.Require {
					if failed[dep] {
						depFailed = true
						break
					}
				}
				if depFailed {
					results[i] = &StateResult{
						Name:       s.Name(),
						Skipped:    true,
						SkipReason: "require_failed",
					}
					continue
				}
			} else {
				// Apply and Check modes: full requisite evaluation.
				skip, skipReason, forceApply := resolveRequisites(reqs, failed, changed)
				if skip {
					results[i] = &StateResult{
						Name:       s.Name(),
						Skipped:    true,
						SkipReason: skipReason,
					}
					continue
				}

				// Prereq gate: run only if a prereq target would change.
				if pr, ok := s.(Prereqer); ok {
					gated, prSkip := r.evalPrereq(ctx, pr, dag)
					if prSkip {
						results[i] = &StateResult{
							Name:       s.Name(),
							Skipped:    true,
							SkipReason: "prereq_not_met",
						}
						continue
					}
					if gated {
						forceApply = true
					}
				}

				wg.Add(1)
				go func(idx int, st State, fa bool) {
					defer wg.Done()
					sr := r.execState(ctx, st, mode, fa)
					mu.Lock()
					results[idx] = sr
					mu.Unlock()
				}(i, s, forceApply)
				continue
			}

			wg.Add(1)
			go func(idx int, st State) {
				defer wg.Done()
				sr := r.execState(ctx, st, mode, false)
				mu.Lock()
				results[idx] = sr
				mu.Unlock()
			}(i, s)
		}

		wg.Wait()

		for idx, sr := range results {
			if sr == nil {
				continue
			}
			result.States[sr.Name] = sr
			if sr.Error != "" {
				result.Failed++
				failed[sr.Name] = true
				if fh, ok := ordered[idx].(FailHarder); ok && fh.FailHard() {
					aborted = true
				}
			}
			if sr.Changed {
				result.Changed++
				changed[sr.Name] = true
			}
			if sr.Skipped {
				result.Skipped++
			}
		}
	}

	result.TotalDuration = time.Since(start)
	return result, nil
}

// orderOf returns a state's declared execution order (0 when unspecified).
func orderOf(s State) int {
	if o, ok := s.(Ordered); ok {
		return o.Order()
	}
	return 0
}

// evalPrereq evaluates a state's prereq targets. gated is true when at least
// one target would change (so this state must run, forced). skip is true when
// no target would change (so this state is skipped).
func (r *Runner) evalPrereq(ctx context.Context, pr Prereqer, dag *DAG) (gated, skip bool) {
	targets := pr.PrereqTargets()
	if len(targets) == 0 {
		return false, false
	}
	for _, name := range targets {
		target := dag.Get(name)
		if target == nil {
			r.logger.Warn("prereq target not found", "target", name)
			continue
		}
		cr, err := target.Check(ctx)
		if err != nil {
			r.logger.Warn("prereq check failed", "target", name, "error", err)
			continue
		}
		if cr.NeedsChange {
			return true, false
		}
	}
	return false, true
}

// resolveRequisites evaluates all requisite types and determines whether
// the state should be skipped, and whether Apply should be forced.
func resolveRequisites(reqs Requisites, failed, changed map[string]bool) (skip bool, skipReason string, forceApply bool) {
	// 1. Require: any failed → skip.
	for _, dep := range reqs.Require {
		if failed[dep] {
			return true, "require_failed", false
		}
	}

	// 2. Watch: any failed → skip (like require); any changed → forceApply.
	for _, dep := range reqs.Watch {
		if failed[dep] {
			return true, "require_failed", false
		}
	}
	for _, dep := range reqs.Watch {
		if changed[dep] {
			forceApply = true
			break
		}
	}

	// 3. OnChanges: any dep failed → skip; none changed → skip "onchanges_not_met".
	if len(reqs.OnChanges) > 0 {
		for _, dep := range reqs.OnChanges {
			if failed[dep] {
				return true, "require_failed", false
			}
		}
		anyChanged := false
		for _, dep := range reqs.OnChanges {
			if changed[dep] {
				anyChanged = true
				break
			}
		}
		if !anyChanged {
			return true, "onchanges_not_met", false
		}
	}

	// 4. OnFail: none failed → skip "onfail_not_met".
	if len(reqs.OnFail) > 0 {
		anyFailed := false
		for _, dep := range reqs.OnFail {
			if failed[dep] {
				anyFailed = true
				break
			}
		}
		if !anyFailed {
			return true, "onfail_not_met", false
		}
	}

	return false, "", forceApply
}

// execState runs a state, retrying on failure when the state declares a
// retry policy (Salt `- retry:`). Retries only apply to apply/revert modes.
func (r *Runner) execState(ctx context.Context, s State, mode RunMode, forceApply bool) *StateResult {
	attempts, interval := 0, time.Duration(0)
	if rt, ok := s.(Retryable); ok && mode != ModeCheck {
		attempts, interval = rt.RetrySpec()
	}

	start := time.Now()
	sr := r.execStateOnce(ctx, s, mode, forceApply)
	for try := 1; try <= attempts && sr.Error != ""; try++ {
		if ctx.Err() != nil {
			break
		}
		r.logger.Warn("state failed, retrying", "state", s.Name(), "attempt", try, "max", attempts, "interval", interval)
		select {
		case <-ctx.Done():
		case <-time.After(interval):
		}
		sr = r.execStateOnce(ctx, s, mode, forceApply)
	}
	sr.Duration = time.Since(start)
	return sr
}

func (r *Runner) execStateOnce(ctx context.Context, s State, mode RunMode, forceApply bool) *StateResult {
	sr := &StateResult{Name: s.Name()}

	switch mode {
	case ModeCheck:
		cr, err := s.Check(ctx)
		if err != nil {
			sr.Error = err.Error()
			r.logger.Error("state check failed", "state", s.Name(), "error", err)
			return sr
		}
		sr.Changed = cr.NeedsChange
		sr.Diff = cr.Diff

	case ModeApply:
		if forceApply {
			// Watch triggered: skip Check, go straight to Apply.
			ar, err := s.Apply(ctx)
			if err != nil {
				sr.Error = err.Error()
				r.logger.Error("state apply failed (watch-forced)", "state", s.Name(), "error", err)
				return sr
			}
			sr.Changed = ar.Changed
			sr.Diff = ar.Diff
			sr.Details = ar.Details
			r.logger.Info("state applied (watch-forced)", "state", s.Name(), "changed", ar.Changed)
			return sr
		}

		// Check first.
		cr, err := s.Check(ctx)
		if err != nil {
			sr.Error = err.Error()
			r.logger.Error("state check failed", "state", s.Name(), "error", err)
			return sr
		}

		if !cr.NeedsChange {
			r.logger.Debug("state already correct", "state", s.Name())
			return sr
		}

		ar, err := s.Apply(ctx)
		if err != nil {
			sr.Error = err.Error()
			r.logger.Error("state apply failed", "state", s.Name(), "error", err)
			return sr
		}
		sr.Changed = ar.Changed
		sr.Diff = ar.Diff
		sr.Details = ar.Details
		r.logger.Info("state applied", "state", s.Name(), "changed", ar.Changed)

	case ModeRevert:
		ar, err := s.Revert(ctx)
		if err != nil {
			sr.Error = err.Error()
			r.logger.Error("state revert failed", "state", s.Name(), "error", err)
			return sr
		}
		sr.Changed = ar.Changed
		sr.Diff = ar.Diff
		sr.Details = ar.Details
		r.logger.Info("state reverted", "state", s.Name(), "changed", ar.Changed)
	}

	return sr
}
