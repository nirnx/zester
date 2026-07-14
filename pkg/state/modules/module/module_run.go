package modulemod

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// moduleRunParams is module.run's (empty) schema proto. module.run declares NO
// fixed parameters — every key is either a directive (requisite / generic
// attribute) or forwarded to the target module — so the compiled schema has
// zero fields and the Spec is marked OpenParams (unknown-key validation
// skipped; the surface is documented as accepting arbitrary parameters).
type moduleRunParams struct{}

// moduleRunSpec is the documentation-only schema for module.run. Because
// module.run is a passthrough, its parameter decoding is dynamic (handled by
// resolveModuleRunTarget below, unchanged); the Spec exists so module.run is
// self-documented in sys.doc / the generated page like every other module.
var moduleRunSpec = func() *modschema.Spec {
	s := regdef.MustSpec("module.run", modschema.KindState, moduleRunParams{}, modschema.Doc{
		Summary: "Invoke another registered state module by name (Salt-compatibility escape hatch).",
		Description: "`module.run` is a Salt-compatibility escape hatch that invokes another **registered " +
			"state module** by name. The target module is built at compile time with the supplied " +
			"arguments and its Check/Apply/Revert are delegated to — so `module.run` is exactly as " +
			"idempotent as its target.\n\n" +
			"It **accepts arbitrary parameters**, which are forwarded to the target module, and supports " +
			"two forms:\n\n" +
			"- **Classic `name:` form** — set `name:` to the target module (for example `file.touch`); " +
			"every other non-`name`, non-directive key is forwarded as the target's config.\n" +
			"- **Dotted-key form** (newer Salt style) — a single dotted key (for example `file.touch:`) " +
			"names the target and its map value is the argument set.\n\n" +
			"If neither a `name:` nor a dotted `<module.func>:` key is present, the build fails with " +
			"`no target module`. Requisite and generic-attribute keys (`require`, `watch`, `onlyif`, " +
			"`order`, …) declared alongside the target are recognized as directives and carried through " +
			"to the target — never mistaken for a dotted module key. The `module.run` state ID is passed " +
			"to the target as ITS state ID, so the target's primary-parameter-defaults-to-ID convention " +
			"applies to the `module.run` ID. Other states reference this one as `module.run:<id>` in " +
			"their requisites — not by the wrapped target's name.",
		Effects: modschema.Effects{
			Check:  "Delegates to the target module's Check (the target is built once at compile time with the resolved arguments).",
			Apply:  "Delegates to the target module's Apply.",
			Revert: "Delegates to the target module's Revert.",
		},
		Examples: []modschema.Example{
			{
				Title:       "Wrap a state module under a descriptive ID (classic form)",
				Kind:        "state",
				Explanation: "name selects the target; the remaining keys (path, makedirs) are forwarded to file.touch.",
				Code: "mark-provisioned:\n  module.run:\n    - name: file.touch\n" +
					"    - path: /var/lib/myapp/.provisioned\n    - makedirs: true\n",
			},
			{
				Title:       "Dotted-key form with a requisite",
				Kind:        "state",
				Explanation: "The dotted key names the target; the requisite alongside it is carried through, never treated as the target.",
				Code: "refresh-cache:\n  module.run:\n    - cmd.run:\n        command: apt-get update\n" +
					"    - onchanges:\n      - \"pkgrepo.managed:docker\"\n",
			},
		},
		Notes: []modschema.Note{
			{
				Level: "info",
				Title: "State registry targets only",
				Body: "The target must be a module registered in Zester's STATE registry (a built-in state " +
					"module or a Starlark custom module). This diverges from Salt: Salt's `module.run` invokes " +
					"EXECUTION modules, whereas Zester resolves the target in its STATE registry — Zester's " +
					"remote-execution functions (`pkg.version`, `service.restart`, …) cannot be targeted here, " +
					"so call them directly from the CLI instead.",
			},
			{
				Level: "info",
				Title: "Idempotent as its target (unlike Salt)",
				Body: "Unlike Salt — where `module.run` always reports a change — Zester delegates the full " +
					"Check/Apply/Revert lifecycle to the target module, so `module.run` is exactly as " +
					"idempotent as whatever it wraps.",
			},
		},
	})
	// module.run forwards arbitrary keys to its target; mark the surface so
	// unknown-key validation is skipped and sys.doc describes it honestly.
	s.OpenParams = true
	return s
}()

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
