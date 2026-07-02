package template

import (
	"reflect"
	"testing"

	"github.com/nikolalohinski/gonja/v2/exec"
)

func TestMutableList_Basics(t *testing.T) {
	l := NewMutableList()
	if l.Len() != 0 {
		t.Errorf("empty list Len() = %d, want 0", l.Len())
	}

	l.Append(1).Append("two")
	if l.Len() != 2 {
		t.Errorf("Len() = %d, want 2", l.Len())
	}

	l.Extend([]any{3.0, true})
	if l.Len() != 4 {
		t.Errorf("Len() after Extend = %d, want 4", l.Len())
	}

	want := []any{1, "two", 3.0, true}
	if !reflect.DeepEqual(l.Items(), want) {
		t.Errorf("Items() = %#v, want %#v", l.Items(), want)
	}
}

func TestMutableList_Prepopulated(t *testing.T) {
	l := NewMutableList("a", "b")
	if l.Len() != 2 {
		t.Errorf("Len() = %d, want 2", l.Len())
	}
	if got := l.Get(0); got != "a" {
		t.Errorf("Get(0) = %v, want %q", got, "a")
	}
	if got := l.Get(1); got != "b" {
		t.Errorf("Get(1) = %v, want %q", got, "b")
	}
}

func TestMutableList_ExtendVariants(t *testing.T) {
	l := NewMutableList("a")

	if _, err := l.Extend([]any{"b"}); err != nil {
		t.Fatalf("Extend([]any): %v", err)
	}
	if _, err := l.Extend(NewMutableList("c")); err != nil {
		t.Fatalf("Extend(*MutableList): %v", err)
	}
	// Template list literals arrive as exec.ValuesList.
	if _, err := l.Extend(exec.ValuesList{exec.AsValue("d")}); err != nil {
		t.Fatalf("Extend(ValuesList): %v", err)
	}

	want := []any{"a", "b", "c", "d"}
	if !reflect.DeepEqual(l.Items(), want) {
		t.Errorf("Items() = %#v, want %#v", l.Items(), want)
	}

	if _, err := l.Extend(42); err == nil {
		t.Error("Extend(non-list) should return an error")
	}
	if l.Len() != 4 {
		t.Errorf("failed Extend must not mutate list, Len() = %d", l.Len())
	}
}

func TestMutableList_ExtendNonListInTemplate(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	_, err = eng.RenderString("test", `{% do mlist().Extend(42) %}`, RenderContext{})
	if err == nil {
		t.Fatal("expected render error for Extend with non-list")
	}
}

func TestMutableList_GetOutOfBounds(t *testing.T) {
	l := NewMutableList("only")
	if got := l.Get(-1); got != nil {
		t.Errorf("Get(-1) = %v, want nil", got)
	}
	if got := l.Get(1); got != nil {
		t.Errorf("Get(1) = %v, want nil", got)
	}
}

func TestMutableList_String(t *testing.T) {
	l := NewMutableList(1, 2)
	if got := l.String(); got != "[1 2]" {
		t.Errorf("String() = %q, want %q", got, "[1 2]")
	}
}

func TestMutableList_CopiesInitialItems(t *testing.T) {
	src := []any{"x"}
	l := NewMutableList(src...)
	l.Append("y")
	if len(src) != 1 {
		t.Errorf("source slice mutated: %#v", src)
	}
}

// TestMutableList_TemplateMutation verifies the documented template pattern:
// mutations via {% do %} propagate back to the context variable.
func TestMutableList_TemplateMutation(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tpl := `{% set used = mlist() %}{% do used.Append('sudo') %}{% if used | length %}has:{{ used.Get(0) }}{% else %}empty{% endif %}`
	result, err := eng.RenderString("test", tpl, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "has:sudo" {
		t.Errorf("got %q, want %q", result, "has:sudo")
	}
}

func TestMutableList_TemplateExtendAndIterate(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tpl := `{% set l = mlist('a') %}{% do l.Extend(['b', 'c']) %}{% for x in l.Items() %}{{ x }}{% endfor %}|{{ l | length }}`
	result, err := eng.RenderString("test", tpl, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "abc|3" {
		t.Errorf("got %q, want %q", result, "abc|3")
	}
}
