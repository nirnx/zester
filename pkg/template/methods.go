package template

import (
	"fmt"
	"sort"

	"github.com/nikolalohinski/gonja/v2/exec"
)

// zesterDictMethods returns an enhanced dict MethodSet that adds Salt-compatible
// methods: get(key, default), items() returning [key, value] pairs, and
// update(other_dict) for in-place merging.
func zesterDictMethods() *exec.MethodSet[map[string]interface{}] {
	return exec.NewMethodSet[map[string]interface{}](map[string]exec.Method[map[string]interface{}]{
		"keys": func(self map[string]interface{}, selfValue *exec.Value, arguments *exec.VarArgs) (interface{}, error) {
			if err := arguments.Take(); err != nil {
				return nil, exec.ErrInvalidCall(err)
			}
			keys := make([]string, 0, len(self))
			for key := range self {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			return keys, nil
		},
		"values": func(self map[string]interface{}, selfValue *exec.Value, arguments *exec.VarArgs) (interface{}, error) {
			if err := arguments.Take(); err != nil {
				return nil, exec.ErrInvalidCall(err)
			}
			keys := make([]string, 0, len(self))
			for key := range self {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			values := make([]interface{}, 0, len(self))
			for _, key := range keys {
				values = append(values, self[key])
			}
			return values, nil
		},
		"items": func(self map[string]interface{}, selfValue *exec.Value, arguments *exec.VarArgs) (interface{}, error) {
			if err := arguments.Take(); err != nil {
				return nil, exec.ErrInvalidCall(err)
			}
			keys := make([]string, 0, len(self))
			for key := range self {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			items := make([]interface{}, 0, len(self))
			for _, key := range keys {
				items = append(items, []interface{}{key, self[key]})
			}
			return items, nil
		},
		"get": func(self map[string]interface{}, selfValue *exec.Value, arguments *exec.VarArgs) (interface{}, error) {
			var key string
			var dflt interface{}
			if err := arguments.Take(
				exec.PositionalArgument("key", nil, exec.StringArgument(&key)),
				exec.KeywordArgument("default", exec.AsValue(nil), exec.AnyArgument(&dflt)),
			); err != nil {
				return nil, exec.ErrInvalidCall(err)
			}
			if val, ok := self[key]; ok {
				return val, nil
			}
			return dflt, nil
		},
		"update": func(self map[string]interface{}, selfValue *exec.Value, arguments *exec.VarArgs) (interface{}, error) {
			var other interface{}
			if err := arguments.Take(
				exec.PositionalArgument("other", nil, exec.AnyArgument(&other)),
			); err != nil {
				return nil, exec.ErrInvalidCall(err)
			}
			otherMap, ok := other.(map[string]interface{})
			if !ok {
				// Template dict literals ({'k': v}) evaluate to *exec.Dict,
				// not map[string]any — convert so {% do d.update({'k': v}) %}
				// works (the canonical Salt pattern).
				if d, isDict := other.(*exec.Dict); isDict {
					otherMap = make(map[string]interface{}, len(d.Pairs))
					for _, pair := range d.Pairs {
						otherMap[pair.Key.String()] = pair.Value.ToGoSimpleType(false)
					}
					ok = true
				}
			}
			if !ok {
				return nil, exec.ErrInvalidCall(fmt.Errorf("update: argument must be a dict"))
			}
			// When self is a template dict literal ({% set d = {...} %}),
			// selfValue wraps *exec.Dict — selfValue.Set() would fail on the
			// struct kind, so mutate the Pairs slice in place instead.
			if d, isDict := selfValue.Interface().(*exec.Dict); isDict {
				for k, v := range otherMap {
					key := exec.AsValue(k)
					replaced := false
					for _, pair := range d.Pairs {
						if pair.Key.EqualValueTo(key) {
							pair.Value = exec.AsValue(v)
							replaced = true
							break
						}
					}
					if !replaced {
						d.Pairs = append(d.Pairs, &exec.Pair{Key: key, Value: exec.AsValue(v)})
					}
				}
				return nil, nil
			}
			// Use selfValue.Set() to modify the underlying reflect.Value
			// directly, not the copy from ToGoSimpleType. gonja's evalMethod
			// calls ToGoSimpleType to produce 'self', which is a copy, so
			// modifying 'self' doesn't persist. But selfValue wraps the
			// original reflect.Value, so Set() uses SetMapIndex which
			// modifies the actual Go map in context.
			for k, v := range otherMap {
				if err := selfValue.Set(exec.AsValue(k), v); err != nil {
					return nil, exec.ErrInvalidCall(fmt.Errorf("update: set %q: %w", k, err))
				}
			}
			return nil, nil
		},
	})
}
