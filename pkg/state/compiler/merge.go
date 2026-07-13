package compiler

import (
	"maps"

	"github.com/nirnx/zester/pkg/state"
)

// requisiteKeys are the same-state requisite declarations appended (not
// replaced) during extend merging. It is DERIVED from state.RequisiteKeys()
// — the single source of truth ParseRequisites itself is driven from (keystone
// spec §6) — rather than re-declaring the key strings, so it cannot drift from
// the requisite set the runtime actually consumes.
var requisiteKeys = func() map[string]bool {
	m := make(map[string]bool, 4)
	for _, k := range state.RequisiteKeys() {
		m[k] = true
	}
	return m
}()

// mergeArgsList merges two args lists according to Salt extend rules:
// - Requisite lists are appended
// - All other keys are replaced (overlay wins)
func mergeArgsList(base, overlay []map[string]any) []map[string]any {
	// Build a map from the base list for easy lookup
	baseMap := make(map[string]any)
	for _, item := range base {
		for k, v := range item {
			if requisiteKeys[k] {
				// For requisites, we need to preserve existing values
				if existing, ok := baseMap[k]; ok {
					// Append to existing list
					existingList, _ := existing.([]any)
					newList, _ := v.([]any)
					baseMap[k] = append(existingList, newList...)
				} else {
					baseMap[k] = v
				}
			} else {
				baseMap[k] = v
			}
		}
	}

	// Apply overlay
	for _, item := range overlay {
		for k, v := range item {
			if requisiteKeys[k] {
				// Append requisites
				if existing, ok := baseMap[k]; ok {
					existingList, _ := existing.([]any)
					newList, _ := v.([]any)
					baseMap[k] = append(existingList, newList...)
				} else {
					baseMap[k] = v
				}
			} else {
				// Replace other keys
				baseMap[k] = v
			}
		}
	}

	// Convert back to args list format
	var result []map[string]any
	for k, v := range baseMap {
		result = append(result, map[string]any{k: v})
	}
	return result
}

// flattenArgsList converts the YAML args list format into a single config map
func flattenArgsList(args []map[string]any) map[string]any {
	result := make(map[string]any)
	for _, item := range args {
		maps.Copy(result, item)
	}
	return result
}
