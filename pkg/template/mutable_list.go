package template

import (
	"fmt"

	"github.com/nikolalohinski/gonja/v2/exec"
)

// MutableList is a pointer-receiver wrapper around []any that provides
// Python-like in-place mutation semantics in Go template engines.
//
// Go template engines (gonja, pongo2, jet) resolve methods via reflection.
// Because MutableList methods use pointer receivers, mutations propagate
// back to the original context variable — solving the fundamental problem
// where reflect.Append on a bare []any creates a new slice header that
// the context never sees.
//
// Template usage:
//
//	{% set used_sudo = mlist() %}
//	{% do used_sudo.Append(1) %}
//	{% if used_sudo | length %}
//	  include: ...
//	{% endif %}
type MutableList struct {
	items []any
}

// NewMutableList creates a MutableList, optionally pre-populated.
func NewMutableList(items ...any) *MutableList {
	return &MutableList{items: append([]any(nil), items...)}
}

// Append adds a single item (like Python list.append).
// Returns the list for chaining and to satisfy gonja's reflection
// requirement that methods return 1 or 2 values.
func (l *MutableList) Append(v any) *MutableList {
	l.items = append(l.items, v)
	return l
}

// Extend appends all items from a list (like Python list.extend).
// The parameter is typed any because gonja evaluates template list
// literals to exec.ValuesList, not []any — a []any-only signature makes
// {% do l.Extend(['a', 'b']) %} fail at render time.
func (l *MutableList) Extend(vs any) (*MutableList, error) {
	switch v := vs.(type) {
	case []any:
		l.items = append(l.items, v...)
	case exec.ValuesList:
		for _, item := range v {
			l.items = append(l.items, item.ToGoSimpleType(false))
		}
	case *MutableList:
		l.items = append(l.items, v.items...)
	default:
		return nil, fmt.Errorf("template: MutableList.Extend: argument must be a list, got %T", vs)
	}
	return l, nil
}

// Len returns the number of items.
func (l *MutableList) Len() int {
	return len(l.items)
}

// Items returns the underlying slice for template iteration.
// Usage: {% for x in mylist.Items() %}
func (l *MutableList) Items() []any {
	return l.items
}

// Get returns the item at index i, or nil if out of bounds.
func (l *MutableList) Get(i int) any {
	if i < 0 || i >= len(l.items) {
		return nil
	}
	return l.items[i]
}

// String returns a human-readable representation.
func (l *MutableList) String() string {
	return fmt.Sprint(l.items)
}
