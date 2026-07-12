package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/state"
)

// ModuleRun implements the module.run state — a Salt-compatibility escape
// hatch that invokes another registered module by name. It delegates
// Check/Apply/Revert to the target module built with the supplied arguments,
// so idempotency is preserved.
//
// Supported forms:
//
//	run_it:
//	  module.run:
//	    - name: file.touch      # target module
//	    - path: /var/run/flag   # arguments forwarded to it
//
//	run_it:
//	  module.run:
//	    - file.touch:           # newer Salt form: dotted key is the target
//	        path: /var/run/flag
type ModuleRun struct {
	id    string
	reqs  state.Requisites
	inner state.State
	name  string
}

// NewModuleRunBuilder returns a builder that constructs module.run states.
// It captures the registry so the target module can be built at compile time.
func NewModuleRunBuilder(registry *state.Registry) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		target, args, err := resolveModuleRunTarget(config)
		if err != nil {
			return nil, fmt.Errorf("module.run:%s: %w", id, err)
		}
		inner, err := registry.Build(target, id, args)
		if err != nil {
			return nil, fmt.Errorf("module.run:%s: build target %q: %w", id, target, err)
		}
		return &ModuleRun{
			id:    id,
			reqs:  state.ParseRequisites(config),
			inner: inner,
			name:  target,
		}, nil
	}
}

// resolveModuleRunTarget determines the target module and its arguments from a
// module.run config map, supporting both the classic `name:` form and the
// newer dotted-key form.
func resolveModuleRunTarget(config map[string]any) (string, map[string]any, error) {
	if name, _ := config["name"].(string); name != "" {
		args := make(map[string]any, len(config))
		for k, v := range config {
			if k == "name" {
				continue
			}
			args[k] = v
		}
		return name, args, nil
	}

	// Newer form: a single dotted key names the target; its value (a map) is
	// the argument set.
	for k, v := range config {
		if !strings.Contains(k, ".") {
			continue
		}
		if isReservedStateKey(k) {
			continue
		}
		args := map[string]any{}
		if m, ok := v.(map[string]any); ok {
			args = m
		}
		// Carry requisites/attributes through to the inner build.
		for rk, rv := range config {
			if rk == k {
				continue
			}
			args[rk] = rv
		}
		return k, args, nil
	}

	return "", nil, fmt.Errorf("no target module: set `name:` or a `<module.func>:` key")
}

// reservedModuleRunKeys is the set of config keys module.run treats as
// directives (generic attributes, requisites, compiler directives) rather than
// a target module. It is the fleet-wide reserved union (state.ReservedKeySet)
// PLUS module.run's own local "name": "name" is a legitimate module parameter
// elsewhere (hence excluded from ReservedKeySet), but here it is the classic
// target selector and must never be mistaken for a "<module.func>:" key.
var reservedModuleRunKeys = func() map[string]struct{} {
	set := state.ReservedKeySet() // fresh copy; safe to extend locally
	set["name"] = struct{}{}
	return set
}()

// isReservedStateKey reports whether a key is a generic state attribute,
// requisite, compiler directive, or module.run's "name" selector — i.e. not a
// target module reference.
func isReservedStateKey(k string) bool {
	_, ok := reservedModuleRunKeys[k]
	return ok
}

// Name returns the state identifier.
func (m *ModuleRun) Name() string { return "module.run:" + m.id }

// Reqs returns the requisite declarations.
func (m *ModuleRun) Reqs() state.Requisites { return m.reqs }

// Check delegates to the target module.
func (m *ModuleRun) Check(ctx context.Context) (state.CheckResult, error) {
	return m.inner.Check(ctx)
}

// Apply delegates to the target module.
func (m *ModuleRun) Apply(ctx context.Context) (state.ApplyResult, error) {
	return m.inner.Apply(ctx)
}

// Revert delegates to the target module.
func (m *ModuleRun) Revert(ctx context.Context) (state.ApplyResult, error) {
	return m.inner.Revert(ctx)
}
