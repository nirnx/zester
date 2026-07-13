package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/nirnx/zester/pkg/cliargs"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// exampleSkip records a non-templated, non-cli example that could not be
// fully validated end-to-end and WHY — §8 requires these be recorded, never
// silently dropped.
type exampleSkip struct {
	Module, Title, Reason string
}

// validateModuleExamples validates every Doc.Example for module against §8:
//   - a "cli" example is not YAML; it is recorded-skipped rather than parsed.
//   - a templated "state" example (contains Jinja delimiters) is
//     recorded-skipped: it is not a self-contained literal YAML document.
//   - every other "state" example must parse as YAML, resolve to exactly the
//     module's own config map, and decode cleanly through reg.Parse — the
//     SAME compiled plan the module's real builder executes (i.e. it "parses
//     as YAML against the module's JSON Schema": the compiled plan IS the
//     schema's source). A decode failure is a real doc bug and fails
//     generation.
//   - a self-contained one (no requisite keys referencing other states) must
//     ALSO construct via reg.Build — the strongest available proof the
//     example actually works. One that isn't self-contained is
//     recorded-skipped for this stronger check only.
//
// module must already be registered in reg via RegisterSpec (docgen only
// calls this for modules with a Spec). sensitive is the set of config keys
// (canonical name + aliases) that decode into a sensitive parameter (F2, §8:
// "Sensitive params never appear in examples or rendered defaults") — build
// it via sensitiveExampleKeys(mi.Params).
func validateModuleExamples(reg *state.Registry, artifact *jsonschema.Schema, module string, examples []modschema.Example, sensitive map[string]struct{}) ([]exampleSkip, error) {
	var skips []exampleSkip
	for _, ex := range examples {
		if ex.Kind == "cli" {
			if err := rejectSensitiveKeys(module, ex.Title, cliExampleKeys(ex.Code), sensitive); err != nil {
				return nil, err
			}
			skips = append(skips, exampleSkip{module, ex.Title, "cli example is not YAML"})
			continue
		}
		if ex.Kind != "state" {
			return nil, fmt.Errorf("docgen: module %s: example %q has unknown kind %q", module, ex.Title, ex.Kind)
		}
		if isTemplated(ex.Code) {
			skips = append(skips, exampleSkip{module, ex.Title, "templated example, not self-contained YAML"})
			continue
		}

		id, params, err := parseStateExample(ex.Code, module)
		if err != nil {
			return nil, fmt.Errorf("docgen: module %s: example %q: %w", module, ex.Title, err)
		}

		if err := rejectSensitiveKeys(module, ex.Title, mapKeysOf(params), sensitive); err != nil {
			return nil, err
		}

		if _, err := reg.Parse(module, id, params); err != nil {
			return nil, fmt.Errorf("docgen: module %s: example %q does not decode against its own schema: %w", module, ex.Title, err)
		}

		// The example must ALSO validate against the EMITTED artifact, not
		// only the compiled plan: the artifact is what editors consume, and
		// exercising it here is what catches artifact-shape regressions the
		// plan cannot see (review finding: two rounds of shape bugs shipped
		// because generated examples never touched the generated schema).
		if artifact != nil {
			inst, err := exampleJSONInstance(ex.Code)
			if err != nil {
				return nil, fmt.Errorf("docgen: module %s: example %q: %w", module, ex.Title, err)
			}
			if err := artifact.Validate(inst); err != nil {
				return nil, fmt.Errorf("docgen: module %s: example %q is rejected by the generated JSON Schema artifact: %w", module, ex.Title, err)
			}
		}

		if hasRequisiteKeys(params) {
			skips = append(skips, exampleSkip{module, ex.Title, "references external requisites, not self-contained"})
			continue
		}
		if _, err := reg.Build(module, id, params); err != nil {
			return nil, fmt.Errorf("docgen: module %s: example %q failed Registry.Build: %w", module, ex.Title, err)
		}
	}
	return skips, nil
}

// validateNonStateExamples validates Doc.Examples for a KindExec or
// KindDispatch module: these are phase-less, imperative surfaces with no
// state-file YAML shape at all (no state ID, no requisite keys, nothing to
// decode against a config map) — reachable only as `zester ... module.func
// args...` or a template's `salt['module.func'](...)` call, both plain CLI-
// style text, never YAML. So every example must be "cli"; anything else is a
// content bug (a "state" example on a phase-less module is meaningless) and
// fails generation. Every cli example is still recorded-skipped, never
// silently — §8's "never silently" applies regardless of kind. sensitive is
// the set of config keys (canonical name + aliases) that decode into a
// sensitive parameter (F2) — build it via sensitiveExampleKeys(mi.Params).
func validateNonStateExamples(module string, examples []modschema.Example, sensitive map[string]struct{}) ([]exampleSkip, error) {
	var skips []exampleSkip
	for _, ex := range examples {
		if ex.Kind != "cli" {
			return nil, fmt.Errorf("docgen: module %s: example %q has kind %q, want \"cli\" (exec/dispatch modules have no state-file YAML shape)",
				module, ex.Title, ex.Kind)
		}
		if err := rejectSensitiveKeys(module, ex.Title, cliExampleKeys(ex.Code), sensitive); err != nil {
			return nil, err
		}
		skips = append(skips, exampleSkip{module, ex.Title, "cli example is not YAML"})
	}
	return skips, nil
}

// sensitiveExampleKeys returns every config key — the canonical Name AND
// every alias — that decodes into a sensitive-marked field, so example
// validation can reject ANY representation of it, not just the canonical
// spelling.
func sensitiveExampleKeys(params []modschema.Field) map[string]struct{} {
	keys := map[string]struct{}{}
	for _, f := range params {
		if !f.Sensitive {
			continue
		}
		keys[f.Name] = struct{}{}
		for _, a := range f.Aliases {
			keys[a] = struct{}{}
		}
	}
	return keys
}

// rejectSensitiveKeys errors if any of keys names a sensitive parameter (§8:
// "Sensitive params never appear in examples") — a content bug that fails
// generation outright, not a recorded-skip.
func rejectSensitiveKeys(module, title string, keys []string, sensitive map[string]struct{}) error {
	for _, k := range keys {
		if _, ok := sensitive[k]; ok {
			return fmt.Errorf("docgen: module %s: example %q sets sensitive parameter %q — "+
				"sensitive params must never appear in examples", module, title, k)
		}
	}
	return nil
}

// cliExampleKeys extracts the key=value token keys from a "cli" example's
// literal Code (e.g. `zester '*' user.present name=joe password=hunter2`)
// through the real CLI key=value ingress, pkg/cliargs.ParseKeyValues — the
// same parser the operator CLI itself uses — rather than a bespoke regex, so
// this reads examples exactly as the CLI would parse them.
func cliExampleKeys(code string) []string {
	args := map[string]any{}
	cliargs.ParseKeyValues(strings.Fields(code), args)
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	return keys
}

// mapKeysOf returns m's keys (a state example's flattened config map).
func mapKeysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// isTemplated reports whether code contains Jinja delimiters.
func isTemplated(code string) bool {
	return strings.Contains(code, "{{") || strings.Contains(code, "{%")
}

// parseStateExample parses a self-contained state-example YAML document
// (exactly one state-ID key, whose value is a map with exactly one key equal
// to module) and flattens the Salt-style list-of-single-key-maps (or a
// direct map) into one config map.
func parseStateExample(code, module string) (id string, params map[string]any, err error) {
	var top map[string]any
	if err := yaml.Unmarshal([]byte(code), &top); err != nil {
		return "", nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if len(top) != 1 {
		return "", nil, fmt.Errorf("expected exactly one top-level state ID key, got %d", len(top))
	}
	for k, v := range top {
		id = k
		modMap, ok := v.(map[string]any)
		if !ok || len(modMap) != 1 {
			return "", nil, fmt.Errorf("state %q: expected exactly one module key", id)
		}
		for mk, mv := range modMap {
			if mk != module {
				return "", nil, fmt.Errorf("state %q: module key %q does not match %q", id, mk, module)
			}
			params, err = flattenModuleConfig(mv)
			if err != nil {
				return "", nil, fmt.Errorf("state %q: %w", id, err)
			}
		}
	}
	return id, params, nil
}

// flattenModuleConfig merges the Salt-style list-of-single-key-maps config
// shape (or accepts an already-flat map) into one config map.
func flattenModuleConfig(v any) (map[string]any, error) {
	switch cfg := v.(type) {
	case map[string]any:
		return cfg, nil
	case []any:
		out := map[string]any{}
		for _, item := range cfg {
			m, ok := item.(map[string]any)
			if !ok || len(m) != 1 {
				return nil, fmt.Errorf("expected a list of single-key maps, got %T", item)
			}
			maps.Copy(out, m)
		}
		return out, nil
	case nil:
		return map[string]any{}, nil
	default:
		return nil, fmt.Errorf("unexpected module config shape %T", v)
	}
}

// crossStateKeySet is the set of reserved keys that make an example reference
// ANOTHER state by ID (so it is not self-contained): the same-state
// requisites, the compiler-only "_in" inverse forms and "listen" alias, and
// "prereq" (a generic attribute, but one that names other states). "names"
// (mere same-state expansion) is deliberately excluded. Per pkg/state/
// reserved.go's own rule, this is DERIVED from state's exported key lists —
// never a hand-copied literal — so it can't drift from ParseRequisites/the
// compiler's real consumed keys.
func crossStateKeySet() map[string]struct{} {
	set := map[string]struct{}{"prereq": {}}
	for _, k := range state.RequisiteKeys() {
		set[k] = struct{}{}
	}
	for _, k := range state.CompilerKeys() {
		if k == "names" {
			continue
		}
		set[k] = struct{}{}
	}
	return set
}

func hasRequisiteKeys(params map[string]any) bool {
	cross := crossStateKeySet()
	for k := range params {
		if _, ok := cross[k]; ok {
			return true
		}
	}
	return false
}

// exampleJSONInstance parses a state example's YAML and round-trips it through
// JSON so the schema validator sees exactly the value shapes an editor's JSON
// Schema engine sees.
func exampleJSONInstance(code string) (any, error) {
	var doc any
	if err := yaml.Unmarshal([]byte(code), &doc); err != nil {
		return nil, fmt.Errorf("parse example YAML: %w", err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode example for schema validation: %w", err)
	}
	var inst any
	if err := json.Unmarshal(raw, &inst); err != nil {
		return nil, err
	}
	return inst, nil
}
