package state

import (
	"context"
	"fmt"
	"time"
)

// Ordered is an optional interface a State may implement to influence
// execution order within its DAG level. Lower values run first; states
// that do not implement it sort after explicit orders (treated as 0 by
// default at parse time). Mirrors Salt's `- order:` declaration.
type Ordered interface {
	Order() int
}

// Retryable is an optional interface a State may implement to request
// automatic retries on failure. Mirrors Salt's `- retry:` declaration.
type Retryable interface {
	// RetrySpec returns the number of additional attempts and the delay
	// between them. attempts <= 0 disables retries.
	RetrySpec() (attempts int, interval time.Duration)
}

// FailHarder is an optional interface a State may implement to abort the
// whole run when it fails. Mirrors Salt's `- failhard: True`.
type FailHarder interface {
	FailHard() bool
}

// Prereqer is an optional interface a State may implement to declare prereq
// gate targets: this state runs (forced) only if one of the targets would
// change. Mirrors Salt's `- prereq:`. Ordering (this state before the
// targets) is established separately via an injected require on each target.
type Prereqer interface {
	PrereqTargets() []string
}

// GuardRunner executes a shell guard command (onlyif/unless) and reports its
// exit code. Provided by the peel from its CommandExec.
type GuardRunner interface {
	RunGuard(ctx context.Context, cmd string) (exitCode int, err error)
}

// GuardRunnerFunc adapts a function to GuardRunner.
type GuardRunnerFunc func(ctx context.Context, cmd string) (int, error)

// RunGuard implements GuardRunner.
func (f GuardRunnerFunc) RunGuard(ctx context.Context, cmd string) (int, error) {
	return f(ctx, cmd)
}

// StateAttributes holds the generic, module-independent declarations that
// Zester supports on any state (Salt parity): execution guards, ordering,
// retries, and failhard. They are parsed from the state config map by the
// compiler and applied via a wrapper so individual modules need no changes.
type StateAttributes struct {
	Onlyif        []string
	Unless        []string
	Order         int
	hasOrder      bool
	RetryAttempts int
	RetryInterval time.Duration
	FailHard      bool
	Prereq        []string
}

// IsZero reports whether no generic attributes are set (so the state needs
// no wrapper).
func (a StateAttributes) IsZero() bool {
	return len(a.Onlyif) == 0 && len(a.Unless) == 0 && !a.hasOrder &&
		a.RetryAttempts == 0 && !a.FailHard && len(a.Prereq) == 0
}

// ParseStateAttributes extracts the generic state attributes from a config
// map. The consumed key set IS attributeKeys (see reserved.go): onlyif,
// unless, order, retry, failhard, prereq. The keys are consumed (unknown to
// the module builders, which ignore them). Because several keys need bespoke
// coercion (order's first/last, retry's map form, failhard's truthiness), the
// parser names them structurally rather than looping the slice; drift between
// this parser and attributeKeys is caught by
// TestParseStateAttributes_ConsumesAttributeKeys.
func ParseStateAttributes(config map[string]any) StateAttributes {
	var a StateAttributes
	a.Onlyif = toStringList(config["onlyif"])
	a.Unless = toStringList(config["unless"])
	a.Prereq = ParseReqEntries(config["prereq"])

	if v, ok := config["order"]; ok {
		a.hasOrder = true
		switch t := v.(type) {
		case int:
			a.Order = t
		case int64:
			a.Order = int(t)
		case float64:
			a.Order = int(t)
		case string:
			switch t {
			case "first":
				a.Order = -1_000_000
			case "last":
				a.Order = 1_000_000
			default:
				a.hasOrder = false
			}
		default:
			a.hasOrder = false
		}
	}

	if v, ok := config["failhard"]; ok {
		a.FailHard = truthy(v)
	}

	if v, ok := config["retry"]; ok {
		a.RetryAttempts, a.RetryInterval = parseRetry(v)
	}

	return a
}

// parseRetry supports both a bare count (`retry: 3`) and Salt's map form
// (`retry: {attempts: 3, interval: 10}` — interval in seconds).
func parseRetry(v any) (attempts int, interval time.Duration) {
	interval = 10 * time.Second // Salt default
	switch t := v.(type) {
	case int:
		attempts = t
	case int64:
		attempts = int(t)
	case float64:
		attempts = int(t)
	case map[string]any:
		if a, ok := t["attempts"]; ok {
			attempts = toInt(a)
		}
		if iv, ok := t["interval"]; ok {
			interval = time.Duration(toInt(iv)) * time.Second
		}
	}
	if attempts < 0 {
		attempts = 0
	}
	return attempts, interval
}

// wrappedState decorates an inner State with generic attributes: onlyif/unless
// guards (evaluated before Check/Apply), ordering, retry, failhard, and prereq
// gating. It embeds the inner State so Name() and Reqs() pass through
// unchanged (preserving DAG identity and dependency edges).
type wrappedState struct {
	State
	attrs  StateAttributes
	guards GuardRunner
}

// WrapAttributes returns a State decorated with the given generic attributes.
// If the attributes are zero, the inner state is returned unchanged.
func WrapAttributes(inner State, attrs StateAttributes, guards GuardRunner) State {
	if attrs.IsZero() {
		return inner
	}
	return &wrappedState{State: inner, attrs: attrs, guards: guards}
}

// Unwrap returns the inner state (used by tooling/tests).
func (w *wrappedState) Unwrap() State { return w.State }

func (w *wrappedState) Order() int { return w.attrs.Order }

func (w *wrappedState) FailHard() bool { return w.attrs.FailHard }

func (w *wrappedState) RetrySpec() (int, time.Duration) {
	return w.attrs.RetryAttempts, w.attrs.RetryInterval
}

func (w *wrappedState) PrereqTargets() []string { return w.attrs.Prereq }

// guardsAllow reports whether the onlyif/unless guards permit the state to
// run. When no guard runner is configured, guards are treated as satisfied.
func (w *wrappedState) guardsAllow(ctx context.Context) (bool, error) {
	if w.guards == nil || (len(w.attrs.Onlyif) == 0 && len(w.attrs.Unless) == 0) {
		return true, nil
	}
	// onlyif: every command must exit 0, else skip.
	for _, cmd := range w.attrs.Onlyif {
		code, err := w.guards.RunGuard(ctx, cmd)
		if err != nil {
			return false, fmt.Errorf("onlyif %q: %w", cmd, err)
		}
		if code != 0 {
			return false, nil
		}
	}
	// unless: if any command exits 0, skip.
	for _, cmd := range w.attrs.Unless {
		code, err := w.guards.RunGuard(ctx, cmd)
		if err != nil {
			return false, fmt.Errorf("unless %q: %w", cmd, err)
		}
		if code == 0 {
			return false, nil
		}
	}
	return true, nil
}

// Check evaluates guards first; a guard that is not met makes the state a
// no-op (reports no change needed).
func (w *wrappedState) Check(ctx context.Context) (CheckResult, error) {
	allow, err := w.guardsAllow(ctx)
	if err != nil {
		return CheckResult{}, err
	}
	if !allow {
		return CheckResult{NeedsChange: false, Diff: "skipped: guard condition not met"}, nil
	}
	return w.State.Check(ctx)
}

// Apply evaluates guards first (needed because watch-forced applies skip
// Check); a guard that is not met makes Apply a no-op.
func (w *wrappedState) Apply(ctx context.Context) (ApplyResult, error) {
	allow, err := w.guardsAllow(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	if !allow {
		return ApplyResult{Changed: false, Diff: "skipped: guard condition not met"}, nil
	}
	return w.State.Apply(ctx)
}

// --- small parsing helpers (shared with requisite handling) ---

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch t {
		case "true", "True", "yes", "Yes", "1", "on", "On":
			return true
		}
	case int:
		return t != 0
	case float64:
		return t != 0
	}
	return false
}

func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	}
	return 0
}

// ParseNameList extracts a Salt-style `names:` list into a string slice.
// Each entry becomes a separate state instance during compilation.
func ParseNameList(v any) []string {
	return toStringList(v)
}

// toStringList accepts a string or a list of strings/ints and returns a
// string slice. Used for onlyif/unless which accept either form.
func toStringList(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			} else {
				out = append(out, fmt.Sprintf("%v", item))
			}
		}
		return out
	}
	return nil
}
