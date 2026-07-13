package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"sort"

	"github.com/nirnx/zester/pkg/modschema"
)

// schemaID is the artifact's canonical $id, matching the site domain (see
// docs-fumadocs-migration memory: https://zester.cc).
const schemaID = "https://zester.cc/schema/zester-modules.schema.json"

// renderModuleSchemaArtifact renders the ONE combined JSON Schema artifact
// (§8, draft 2020-12) for every module in infos (already filtered to
// HasSpec==true — "gated on spec presence"): a top-level state-map schema
// (state ID -> exactly one module name -> parameters), a $defs entry per
// module, and a shared $defs.semanticTypes entry per distinct semantic type
// any migrated module's fields use.
//
// Each module's map value is validated by a "state-map oneOf" (§8): either it
// names a migrated module (and is checked against that module's $def) or it
// names NONE of the migrated modules (an as-yet-undocumented module, left
// unconstrained). The catch-all branch is an allOf of PER-NAME exclusions
// (`{"not":{"required":["<name>"]}}` for every migrated name) — deliberately
// NOT a single `{"not":{"required":[names...]}}`, which means "not ALL of
// these names are present" (trivially true whenever at least one is absent)
// rather than "NONE of these names is present": with >=2 migrated modules
// that single-not form let a valid migrated-module instance match BOTH its
// own branch AND the catch-all, so oneOf's exactly-one-match rejected every
// migrated instance (F1). With ZERO migrated modules there is nothing to
// constrain at all — see the `len(names) == 0` guard below, which omits the
// oneOf entirely rather than emitting the always-false `{"not":{"required":
// []}}` (an empty "required" is trivially satisfied by any object, so "not"
// of it is always false, rejecting every property outright).
func renderModuleSchemaArtifact(infos []modschema.ModuleInfo) ([]byte, error) {
	names := make([]string, 0, len(infos))
	moduleDefs := map[string]any{}
	semTypeDefs := map[string]any{}
	for _, mi := range infos {
		if !mi.HasSpec {
			continue
		}
		names = append(names, mi.Module)
		moduleDefs[mi.Module] = moduleParamSchema(mi)
		for _, st := range mi.SemTypes {
			if _, ok := semTypeDefs[st.Name]; ok {
				continue
			}
			semTypeDefs[st.Name] = withDescription(st.JSONSchema, st.Doc)
		}
	}
	sort.Strings(names)

	// The state-entry schema validates PARAMS for every KNOWN module key while
	// allowing (a) MULTIPLE modules under one state ID — explicitly supported by
	// the compiler (same ID, different modules = separate DAG states) — and
	// (b) unknown module keys (per-peel Starlark custom modules), which stay
	// unconstrained. A properties map does exactly that; the earlier
	// oneOf-over-required-branches design (with maxProperties: 1) wrongly
	// rejected valid multi-module entries.
	moduleProps := make(map[string]any, len(names))
	for _, name := range names {
		moduleProps[name] = map[string]any{"$ref": "#/$defs/modules/" + name}
	}
	stateMapValue := map[string]any{
		"type":                 "object",
		"minProperties":        1,
		"properties":           moduleProps,
		"additionalProperties": true,
	}

	top := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id":     schemaID,
		"title":   "Zester module parameters",
		"description": "A Zester state file (state ID -> exactly one module name -> " +
			"parameters), generated from each module's self-documenting schema (keystone " +
			"spec). Only migrated modules are constrained via $defs.modules; a module " +
			"absent from it has no schema yet and is left unconstrained.",
		"$defs": map[string]any{
			"modules":       moduleDefs,
			"semanticTypes": semTypeDefs,
		},
		"type": "object",
		// Top-level directives are NOT state entries (review finding): include
		// is a list of dot-notation references, extend an object of state
		// overrides (left loosely typed — its values follow the same
		// module-list shape but reference other files' states).
		"properties": map[string]any{
			"include": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"extend": map[string]any{"type": "object"},
		},
		"additionalProperties": stateMapValue,
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(top); err != nil {
		return nil, fmt.Errorf("docgen: render schema artifact: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// moduleParamSchema renders one module's $defs entry, modeling the REAL .zy
// shape the compiler enforces (parseStateData: the module's value MUST be a
// LIST of maps — `- content: ...` / `- mode: ...` — never a single object;
// review finding). Each list item is an object whose known properties are the
// module's canonical parameters; additionalProperties stays permissive because
// requisites, generic attributes, and aliases also ride the same list items
// and are the Decode-time policy's concern. A REQUIRED parameter cannot be
// expressed on items (each item carries only SOME params) — it is expressed
// with `contains`: some item must carry the key.
//
// A semantic-typed field's property is a $ref into the shared
// $defs.semanticTypes entry rather than an inlined copy (one module using
// TriState in three fields must not carry three copies, and the shared entry
// must be load-bearing).
func moduleParamSchema(mi modschema.ModuleInfo) map[string]any {
	props := map[string]any{}
	var required []string
	for _, f := range mi.Params {
		if f.SemanticType != "" {
			props[f.Name] = withDescription(map[string]any{
				"$ref": "#/$defs/semanticTypes/" + f.SemanticType,
			}, f.Usage)
		} else {
			props[f.Name] = withDescription(f.JSONSchema, f.Usage)
		}
		if f.Required {
			required = append(required, f.Name)
		}
	}
	item := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": true,
	}
	def := map[string]any{
		"type":        "array",
		"description": mi.Doc.Summary,
		"items":       item,
	}
	if len(required) > 0 {
		sort.Strings(required)
		conts := make([]any, 0, len(required))
		for _, name := range required {
			conts = append(conts, map[string]any{
				"contains": map[string]any{"required": []string{name}},
			})
		}
		def["allOf"] = conts
	}
	return def
}

// withDescription returns a shallow copy of frag with "description" set to
// desc (never mutating a shared semantic-type or field fragment; desc is
// skipped when empty so a fragment isn't polluted with a blank field).
func withDescription(frag map[string]any, desc string) map[string]any {
	out := make(map[string]any, len(frag)+1)
	maps.Copy(out, frag)
	if desc != "" {
		out["description"] = desc
	}
	return out
}
