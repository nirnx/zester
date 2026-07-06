package starmod_test

import (
	"testing"

	"github.com/nirnx/zester/pkg/starmod"
	"go.starlark.net/starlark"
)

func TestGoToStarlark_Primitives(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string // starlark.Value.String()
	}{
		{"nil", nil, "None"},
		{"bool true", true, "True"},
		{"bool false", false, "False"},
		{"string", "hello", `"hello"`},
		{"int", 42, "42"},
		{"int64", int64(100), "100"},
		{"float64", 3.14, "3.14"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := starmod.GoToStarlark(tt.in)
			if err != nil {
				t.Fatalf("GoToStarlark(%v) error: %v", tt.in, err)
			}
			if v.String() != tt.want {
				t.Errorf("got %s, want %s", v.String(), tt.want)
			}
		})
	}
}

func TestGoToStarlark_Map(t *testing.T) {
	m := map[string]any{
		"name": "nginx",
		"port": 80,
	}
	v, err := starmod.GoToStarlark(m)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := v.(*starlark.Dict)
	if !ok {
		t.Fatalf("expected *starlark.Dict, got %T", v)
	}
	if d.Len() != 2 {
		t.Errorf("dict len = %d, want 2", d.Len())
	}

	nameVal, _, _ := d.Get(starlark.String("name"))
	if nameVal.(starlark.String) != "nginx" {
		t.Errorf("name = %v, want nginx", nameVal)
	}
}

func TestGoToStarlark_Slice(t *testing.T) {
	s := []any{"a", "b", "c"}
	v, err := starmod.GoToStarlark(s)
	if err != nil {
		t.Fatal(err)
	}
	list, ok := v.(*starlark.List)
	if !ok {
		t.Fatalf("expected *starlark.List, got %T", v)
	}
	if list.Len() != 3 {
		t.Errorf("list len = %d, want 3", list.Len())
	}
}

func TestGoToStarlark_Nested(t *testing.T) {
	m := map[string]any{
		"nested": map[string]any{
			"key": "value",
		},
		"list": []any{1, 2, 3},
	}
	v, err := starmod.GoToStarlark(m)
	if err != nil {
		t.Fatal(err)
	}
	d := v.(*starlark.Dict)
	nestedVal, _, _ := d.Get(starlark.String("nested"))
	nd := nestedVal.(*starlark.Dict)
	keyVal, _, _ := nd.Get(starlark.String("key"))
	if keyVal.(starlark.String) != "value" {
		t.Errorf("nested.key = %v, want value", keyVal)
	}
}

func TestGoToStarlark_UnsupportedType(t *testing.T) {
	_, err := starmod.GoToStarlark(make(chan int))
	if err == nil {
		t.Error("expected error for unsupported type")
	}
}

func TestStarlarkToGo_Primitives(t *testing.T) {
	tests := []struct {
		name string
		in   starlark.Value
		want any
	}{
		{"none", starlark.None, nil},
		{"true", starlark.True, true},
		{"false", starlark.False, false},
		{"string", starlark.String("hello"), "hello"},
		{"int", starlark.MakeInt(42), int64(42)},
		{"float", starlark.Float(3.14), float64(3.14)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := starmod.StarlarkToGo(tt.in)
			if got != tt.want {
				t.Errorf("got %v (%T), want %v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}

func TestStarlarkToGo_Dict(t *testing.T) {
	d := starlark.NewDict(2)
	d.SetKey(starlark.String("a"), starlark.MakeInt(1))
	d.SetKey(starlark.String("b"), starlark.String("two"))

	got := starmod.StarlarkToGo(d)
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", got)
	}
	if m["a"] != int64(1) {
		t.Errorf("a = %v, want 1", m["a"])
	}
	if m["b"] != "two" {
		t.Errorf("b = %v, want two", m["b"])
	}
}

func TestStarlarkToGo_List(t *testing.T) {
	list := starlark.NewList([]starlark.Value{
		starlark.MakeInt(1),
		starlark.String("two"),
		starlark.True,
	})
	got := starmod.StarlarkToGo(list)
	s, ok := got.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", got)
	}
	if len(s) != 3 {
		t.Fatalf("len = %d, want 3", len(s))
	}
	if s[0] != int64(1) {
		t.Errorf("s[0] = %v, want 1", s[0])
	}
}

func TestDictToCheckResult_Valid(t *testing.T) {
	d := starlark.NewDict(2)
	d.SetKey(starlark.String("needs_change"), starlark.True)
	d.SetKey(starlark.String("diff"), starlark.String("content differs"))

	result, err := starmod.DictToCheckResult(d)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsChange {
		t.Error("NeedsChange should be true")
	}
	if result.Diff != "content differs" {
		t.Errorf("Diff = %q, want %q", result.Diff, "content differs")
	}
}

func TestDictToCheckResult_NoDiff(t *testing.T) {
	d := starlark.NewDict(1)
	d.SetKey(starlark.String("needs_change"), starlark.False)

	result, err := starmod.DictToCheckResult(d)
	if err != nil {
		t.Fatal(err)
	}
	if result.NeedsChange {
		t.Error("NeedsChange should be false")
	}
	if result.Diff != "" {
		t.Errorf("Diff should be empty, got %q", result.Diff)
	}
}

func TestDictToCheckResult_NotDict(t *testing.T) {
	_, err := starmod.DictToCheckResult(starlark.String("bad"))
	if err == nil {
		t.Error("expected error for non-dict")
	}
}

func TestDictToCheckResult_MissingKey(t *testing.T) {
	d := starlark.NewDict(1)
	d.SetKey(starlark.String("diff"), starlark.String("x"))

	_, err := starmod.DictToCheckResult(d)
	if err == nil {
		t.Error("expected error for missing needs_change")
	}
}

func TestDictToApplyResult_Valid(t *testing.T) {
	details := starlark.NewDict(1)
	details.SetKey(starlark.String("message"), starlark.String("done"))

	d := starlark.NewDict(3)
	d.SetKey(starlark.String("changed"), starlark.True)
	d.SetKey(starlark.String("diff"), starlark.String("wrote config"))
	d.SetKey(starlark.String("details"), details)

	result, err := starmod.DictToApplyResult(d)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("Changed should be true")
	}
	if result.Diff != "wrote config" {
		t.Errorf("Diff = %q, want %q", result.Diff, "wrote config")
	}
	if result.Details["message"] != "done" {
		t.Errorf("Details[message] = %q, want %q", result.Details["message"], "done")
	}
}

func TestDictToApplyResult_MinimalValid(t *testing.T) {
	d := starlark.NewDict(1)
	d.SetKey(starlark.String("changed"), starlark.False)

	result, err := starmod.DictToApplyResult(d)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Error("Changed should be false")
	}
}

func TestDictToApplyResult_NotDict(t *testing.T) {
	_, err := starmod.DictToApplyResult(starlark.MakeInt(42))
	if err == nil {
		t.Error("expected error for non-dict")
	}
}

func TestDictToApplyResult_MissingKey(t *testing.T) {
	d := starlark.NewDict(1)
	d.SetKey(starlark.String("diff"), starlark.String("x"))

	_, err := starmod.DictToApplyResult(d)
	if err == nil {
		t.Error("expected error for missing changed")
	}
}

func TestRoundTrip(t *testing.T) {
	original := map[string]any{
		"name":  "test",
		"count": 42,
		"items": []any{"a", "b"},
		"nested": map[string]any{
			"key": "value",
		},
	}

	sv, err := starmod.GoToStarlark(original)
	if err != nil {
		t.Fatal(err)
	}

	roundTripped := starmod.StarlarkToGo(sv)
	m, ok := roundTripped.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", roundTripped)
	}
	if m["name"] != "test" {
		t.Errorf("name = %v", m["name"])
	}
	if m["count"] != int64(42) {
		t.Errorf("count = %v (%T)", m["count"], m["count"])
	}
	items, ok := m["items"].([]any)
	if !ok || len(items) != 2 {
		t.Errorf("items = %v", m["items"])
	}
	nested, ok := m["nested"].(map[string]any)
	if !ok || nested["key"] != "value" {
		t.Errorf("nested = %v", m["nested"])
	}
}
