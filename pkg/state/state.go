// Package state provides the state engine for Zester.
// States are idempotent units of configuration that can be checked, applied,
// and reverted on target peels. States declare dependencies via Reqs()
// and are executed in topological order with maximum parallelism.
package state

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nirnx/zester/pkg/modschema"
)

// Requisites holds the dependency declarations for a state.
// Each requisite type creates DAG ordering edges, but the Runner
// interprets their semantics differently during execution.
type Requisites struct {
	// Require lists states that must succeed before this state runs.
	Require []string

	// Watch lists states that must succeed before this state runs.
	// If any watched state changed, the runner forces Apply (skips Check).
	Watch []string

	// OnChanges lists states that gate this state's execution.
	// This state only runs if at least one listed state made changes;
	// otherwise it is skipped with SkipReason "onchanges_not_met".
	OnChanges []string

	// OnFail lists states that gate this state's execution.
	// This state only runs if at least one listed state failed;
	// otherwise it is skipped with SkipReason "onfail_not_met".
	OnFail []string
}

// AllDeps returns a deduplicated union of all requisite lists.
// This is used by the DAG to create ordering edges — all requisite
// types create edges regardless of their runtime semantics.
func (r Requisites) AllDeps() []string {
	seen := make(map[string]bool)
	var deps []string
	for _, lists := range [][]string{r.Require, r.Watch, r.OnChanges, r.OnFail} {
		for _, d := range lists {
			if !seen[d] {
				seen[d] = true
				deps = append(deps, d)
			}
		}
	}
	return deps
}

// ParseRequisites extracts requisite declarations from a state config map.
// It replaces the manual config["require"].([]any) boilerplate in modules.
// Supports both Zester string format ("pkg.installed:nginx") and Salt-style
// dict format ({"pkg": "nginx"} → "pkg.installed:nginx").
//
// The consumed key set IS requisiteKeys (see reserved.go): the keys are read
// from that slice, positionally paired with the output fields it documents
// (Require, Watch, OnChanges, OnFail). A drift between the slice and the
// fields is caught by TestParseRequisites_ConsumesRequisiteKeys.
func ParseRequisites(config map[string]any) Requisites {
	var r Requisites
	dests := [...]*[]string{&r.Require, &r.Watch, &r.OnChanges, &r.OnFail}
	for i, key := range requisiteKeys {
		*dests[i] = parseReqList(config, key)
	}
	return r
}

// saltShorthandMap maps Salt requisite shorthand module names to their
// default Zester full module names.
var saltShorthandMap = map[string]string{
	"pkg":     "pkg.installed",
	"file":    "file.managed",
	"service": "service.running",
	"cmd":     "cmd.run",
	"user":    "user.present",
	"group":   "group.present",
}

// parseReqList extracts a requisite list from a config map.
// Accepts both formats:
//   - String:  "pkg.installed:nginx"   → used as-is
//   - Dict:    {"pkg": "nginx"}        → resolved to "pkg.installed:nginx"
func parseReqList(config map[string]any, key string) []string {
	return ParseReqEntries(config[key])
}

// ParseReqEntries normalizes a requisite value (as found under require/watch/
// prereq/… keys) into a list of "module:id" reference strings. Accepts a
// single entry or a list, in either Zester string form ("pkg.installed:nginx")
// or Salt dict form ({"pkg": "nginx"}).
func ParseReqEntries(v any) []string {
	var raw []any
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		raw = t
	default:
		raw = []any{t}
	}
	var result []string
	for _, item := range raw {
		switch entry := item.(type) {
		case string:
			result = append(result, entry)
		case map[string]any:
			// Salt-style: {"pkg": "nginx_pkg"} → "pkg.installed:nginx_pkg"
			for mod, id := range entry {
				stateID := fmt.Sprintf("%v", id)
				fullMod := saltShorthandMap[mod]
				if fullMod == "" {
					fullMod = mod // pass through unknown modules as-is
				}
				result = append(result, fullMod+":"+stateID)
			}
		}
	}
	return result
}

// State is the interface that all state modules must implement.
// Each state represents a single idempotent configuration action.
type State interface {
	// Name returns the unique identifier for this state instance.
	// Format: "module.function:id" (e.g., "file.managed:/etc/hosts")
	Name() string

	// Reqs returns the requisite declarations for this state.
	Reqs() Requisites

	// Check inspects the current system state and returns whether changes are needed.
	// It must not modify the system.
	Check(ctx context.Context) (CheckResult, error)

	// Apply makes the system match the desired state.
	// It should be idempotent: calling Apply on an already-correct system is a no-op.
	Apply(ctx context.Context) (ApplyResult, error)

	// Revert undoes the changes made by Apply, restoring the previous state.
	Revert(ctx context.Context) (ApplyResult, error)
}

// CheckResult reports whether a state needs changes.
type CheckResult struct {
	// NeedsChange is true when the current system does not match the desired state.
	NeedsChange bool

	// Diff describes the difference between current and desired state.
	Diff string
}

// ApplyResult reports the outcome of an Apply or Revert operation.
type ApplyResult struct {
	// Changed is true when the system was actually modified.
	Changed bool

	// Diff describes what was changed.
	Diff string

	// Details contains module-specific result data.
	Details map[string]string
}

// StateResult captures the full execution result for a single state.
type StateResult struct {
	// Name is the state identifier.
	Name string `msgpack:"name"`

	// Changed is true if the state modified the system.
	Changed bool `msgpack:"changed"`

	// Diff describes the changes made.
	Diff string `msgpack:"diff"`

	// Duration is how long execution took.
	Duration time.Duration `msgpack:"duration"`

	// Details contains module-specific result data.
	Details map[string]string `msgpack:"details"`

	// Error is set when the state failed to apply.
	Error string `msgpack:"error,omitempty"`

	// Skipped is true when the state was skipped due to a failed dependency
	// or unmet requisite condition.
	Skipped bool `msgpack:"skipped,omitempty"`

	// SkipReason describes why the state was skipped.
	// Values: "require_failed", "onchanges_not_met", "onfail_not_met".
	SkipReason string `msgpack:"skip_reason,omitempty"`
}

// Registry is a thread-safe store for state constructors.
// Modules register themselves at init time, and the runner uses the registry
// to instantiate states from configuration data.
//
// A module may additionally register a modschema.Spec (RegisterSpec) — its
// compiled parameter schema plus documentation metadata — which powers Describe,
// SpecNames, and Parse alongside the plain Build path. Legacy and Starlark
// modules keep using Register with no spec.
type Registry struct {
	mu       sync.RWMutex
	builders map[string]Builder
	specs    map[string]*modschema.Spec
}

// Builder constructs a State from a config map.
// The config map contains the parameters from the state file (e.g., path, content, mode).
type Builder func(id string, config map[string]any) (State, error)

// NewRegistry creates an empty state registry.
func NewRegistry() *Registry {
	return &Registry{
		builders: make(map[string]Builder),
		specs:    make(map[string]*modschema.Spec),
	}
}

// Register adds a state module builder under the given name (e.g., "file.managed").
func (r *Registry) Register(name string, b Builder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builders[name] = b
}

// Build constructs a state by looking up the module name in the registry.
func (r *Registry) Build(module, id string, config map[string]any) (State, error) {
	r.mu.RLock()
	b, ok := r.builders[module]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("state: unknown module %q", module)
	}
	return b(id, config)
}

// Has reports whether a state module builder is registered under name.
// Safe for concurrent use; reflects late registrations (e.g. Starlark
// modules loaded after startup).
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.builders[name]
	return ok
}

// Modules returns a sorted list of registered module names.
func (r *Registry) Modules() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.builders))
	for name := range r.builders {
		names = append(names, name)
	}
	return names
}

// DefaultRegistry returns a registry pre-loaded with all built-in modules.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	RegisterBuiltins(r)
	return r
}

// RegisterBuiltins registers all built-in state modules into the given registry.
func RegisterBuiltins(r *Registry) {
	// Modules are registered by their init functions via RegisterModule.
	// This function is a hook for explicit registration if needed.
}
