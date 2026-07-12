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

	stateMapValue := map[string]any{
		"type":          "object",
		"minProperties": 1,
		"maxProperties": 1,
	}
	if len(names) > 0 {
		branches := make([]any, 0, len(names)+1)
		for _, name := range names {
			branches = append(branches, map[string]any{
				"required": []string{name},
				"properties": map[string]any{
					name: map[string]any{"$ref": "#/$defs/modules/" + name},
				},
			})
		}
		noneMigrated := make([]any, 0, len(names))
		for _, name := range names {
			noneMigrated = append(noneMigrated, map[string]any{
				"not": map[string]any{"required": []string{name}},
			})
		}
		branches = append(branches, map[string]any{"allOf": noneMigrated})
		stateMapValue["oneOf"] = branches
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
		"type":                 "object",
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

// moduleParamSchema renders one module's $defs entry: an object schema over
// its canonical parameter names (aliases are a Decode-time convenience, not
// part of the strict schema surface). additionalProperties stays permissive
// — requisites, generic attributes, and any not-yet-modeled alias are the
// Decode-time unknown-key policy's concern, not this artifact's.
//
// A semantic-typed field's property is a $ref into the shared
// $defs.semanticTypes entry (F4) rather than an inlined copy of the type's
// JSONSchema fragment: one migrated module using, say, TriState in three
// fields must not carry three duplicated inline copies of TriState's schema,
// and the shared $defs entry must be load-bearing (referenced), not merely
// present. $ref may carry sibling keywords (draft 2019-09+, so draft
// 2020-12) — withDescription still attaches the field's own usage text.
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
	def := map[string]any{
		"type":                 "object",
		"description":          mi.Doc.Summary,
		"properties":           props,
		"additionalProperties": true,
	}
	if len(required) > 0 {
		def["required"] = required
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
