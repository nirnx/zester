// Package starmod provides Starlark-based custom state module support.
// Users drop .star files into _modules/ directories within their states,
// and they become available as state modules alongside built-in Go modules.
package starmod

import (
	"fmt"
	"sort"

	"github.com/nirnx/zester/pkg/state"
	"go.starlark.net/starlark"
)

// GoToStarlark converts a Go value to a Starlark value.
// Supported types: string, int (all widths), float64, bool, nil,
// map[string]any, []any. Unsupported types return an error.
func GoToStarlark(v any) (starlark.Value, error) {
	switch val := v.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(val), nil
	case string:
		return starlark.String(val), nil
	case int:
		return starlark.MakeInt(val), nil
	case int8:
		return starlark.MakeInt(int(val)), nil
	case int16:
		return starlark.MakeInt(int(val)), nil
	case int32:
		return starlark.MakeInt(int(val)), nil
	case int64:
		return starlark.MakeInt64(val), nil
	case uint:
		return starlark.MakeUint(val), nil
	case uint8:
		return starlark.MakeUint(uint(val)), nil
	case uint16:
		return starlark.MakeUint(uint(val)), nil
	case uint32:
		return starlark.MakeUint(uint(val)), nil
	case uint64:
		return starlark.MakeUint64(val), nil
	case float32:
		return starlark.Float(val), nil
	case float64:
		return starlark.Float(val), nil
	case map[string]any:
		d := starlark.NewDict(len(val))
		// Sort keys for deterministic output.
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sv, err := GoToStarlark(val[k])
			if err != nil {
				return nil, fmt.Errorf("convert map key %q: %w", k, err)
			}
			if err := d.SetKey(starlark.String(k), sv); err != nil {
				return nil, fmt.Errorf("set map key %q: %w", k, err)
			}
		}
		return d, nil
	case []any:
		elems := make([]starlark.Value, len(val))
		for i, elem := range val {
			sv, err := GoToStarlark(elem)
			if err != nil {
				return nil, fmt.Errorf("convert list index %d: %w", i, err)
			}
			elems[i] = sv
		}
		return starlark.NewList(elems), nil
	default:
		return nil, fmt.Errorf("unsupported Go type %T", v)
	}
}

// StarlarkToGo converts a Starlark value to a Go value.
// Returns: string, int64, float64, bool, nil, map[string]any, []any.
func StarlarkToGo(v starlark.Value) any {
	switch val := v.(type) {
	case starlark.NoneType:
		return nil
	case starlark.Bool:
		return bool(val)
	case starlark.String:
		return string(val)
	case starlark.Int:
		if i, ok := val.Int64(); ok {
			return i
		}
		if u, ok := val.Uint64(); ok {
			return u
		}
		// Fallback for very large ints.
		return val.String()
	case starlark.Float:
		return float64(val)
	case *starlark.List:
		result := make([]any, val.Len())
		for i := 0; i < val.Len(); i++ {
			result[i] = StarlarkToGo(val.Index(i))
		}
		return result
	case starlark.Tuple:
		result := make([]any, len(val))
		for i, elem := range val {
			result[i] = StarlarkToGo(elem)
		}
		return result
	case *starlark.Dict:
		result := make(map[string]any, val.Len())
		for _, item := range val.Items() {
			key := StarlarkToGo(item[0])
			keyStr, ok := key.(string)
			if !ok {
				keyStr = fmt.Sprintf("%v", key)
			}
			result[keyStr] = StarlarkToGo(item[1])
		}
		return result
	default:
		return v.String()
	}
}

// DictToCheckResult converts a Starlark value (expected dict) to a state.CheckResult.
// Expected keys: "needs_change" (bool, required), "diff" (string, optional).
func DictToCheckResult(v starlark.Value) (state.CheckResult, error) {
	d, ok := v.(*starlark.Dict)
	if !ok {
		return state.CheckResult{}, fmt.Errorf("starmod: check function must return a dict, got %s", v.Type())
	}

	var result state.CheckResult

	ncVal, found, err := d.Get(starlark.String("needs_change"))
	if err != nil {
		return result, fmt.Errorf("starmod: check result: %w", err)
	}
	if !found {
		return result, fmt.Errorf("starmod: check result missing required key \"needs_change\"")
	}
	nc, ok := ncVal.(starlark.Bool)
	if !ok {
		return result, fmt.Errorf("starmod: check result \"needs_change\" must be bool, got %s", ncVal.Type())
	}
	result.NeedsChange = bool(nc)

	diffVal, found, err := d.Get(starlark.String("diff"))
	if err != nil {
		return result, fmt.Errorf("starmod: check result: %w", err)
	}
	if found {
		if s, ok := diffVal.(starlark.String); ok {
			result.Diff = string(s)
		}
	}

	return result, nil
}

// DictToApplyResult converts a Starlark value (expected dict) to a state.ApplyResult.
// Expected keys: "changed" (bool, required), "diff" (string, optional),
// "details" (dict of string->string, optional).
func DictToApplyResult(v starlark.Value) (state.ApplyResult, error) {
	d, ok := v.(*starlark.Dict)
	if !ok {
		return state.ApplyResult{}, fmt.Errorf("starmod: apply/revert function must return a dict, got %s", v.Type())
	}

	var result state.ApplyResult

	chVal, found, err := d.Get(starlark.String("changed"))
	if err != nil {
		return result, fmt.Errorf("starmod: apply result: %w", err)
	}
	if !found {
		return result, fmt.Errorf("starmod: apply result missing required key \"changed\"")
	}
	ch, ok := chVal.(starlark.Bool)
	if !ok {
		return result, fmt.Errorf("starmod: apply result \"changed\" must be bool, got %s", chVal.Type())
	}
	result.Changed = bool(ch)

	diffVal, found, err := d.Get(starlark.String("diff"))
	if err != nil {
		return result, fmt.Errorf("starmod: apply result: %w", err)
	}
	if found {
		if s, ok := diffVal.(starlark.String); ok {
			result.Diff = string(s)
		}
	}

	detailsVal, found, err := d.Get(starlark.String("details"))
	if err != nil {
		return result, fmt.Errorf("starmod: apply result: %w", err)
	}
	if found {
		if dd, ok := detailsVal.(*starlark.Dict); ok {
			result.Details = make(map[string]string, dd.Len())
			for _, item := range dd.Items() {
				k := StarlarkToGo(item[0])
				v := StarlarkToGo(item[1])
				result.Details[fmt.Sprintf("%v", k)] = fmt.Sprintf("%v", v)
			}
		}
	}

	return result, nil
}
