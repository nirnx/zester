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

// isReservedStateKey reports whether a key is a generic state attribute or
// requisite rather than a module target.
func isReservedStateKey(k string) bool {
	switch k {
	case "require", "watch", "onchanges", "onfail", "prereq",
		"require_in", "watch_in", "onchanges_in", "onfail_in", "prereq_in",
		"listen", "listen_in", "onlyif", "unless", "order", "retry",
		"failhard", "names", "name":
		return true
	}
	return false
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
