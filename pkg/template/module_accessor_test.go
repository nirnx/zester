package template

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// recordedCall captures a single ModuleFn invocation.
type recordedCall struct {
	name   string
	args   []any
	kwargs map[string]any
}

// stubModuleFn returns a ModuleFunc that records calls and returns results
// from the given map (keyed by module name).
func stubModuleFn(calls *[]recordedCall, results map[string]any) ModuleFunc {
	return func(name string, args []any, kwargs map[string]any) (any, error) {
		*calls = append(*calls, recordedCall{name: name, args: args, kwargs: kwargs})
		if res, ok := results[name]; ok {
			return res, nil
		}
		return nil, fmt.Errorf("unknown module %q", name)
	}
}

func TestEngine_SaltAccessor_SubscriptCall(t *testing.T) {
	var calls []recordedCall
	eng, err := NewEngine(EngineConfig{
		BasePath: t.TempDir(),
		ModuleFn: stubModuleFn(&calls, map[string]any{"pkg.version": "1.18.0"}),
	})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	result, err := eng.RenderString("test", `{{ salt["pkg.version"]("nginx") }}`, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "1.18.0" {
		t.Errorf("got %q, want %q", result, "1.18.0")
	}
	if len(calls) != 1 {
		t.Fatalf("got %d module calls, want 1", len(calls))
	}
	if calls[0].name != "pkg.version" {
		t.Errorf("got module name %q, want %q", calls[0].name, "pkg.version")
	}
	if !reflect.DeepEqual(calls[0].args, []any{"nginx"}) {
		t.Errorf("got args %v, want [nginx]", calls[0].args)
	}
	if len(calls[0].kwargs) != 0 {
		t.Errorf("got kwargs %v, want empty", calls[0].kwargs)
	}
}

func TestEngine_SaltAccessor_SingleQuotes(t *testing.T) {
	var calls []recordedCall
	eng, err := NewEngine(EngineConfig{
		BasePath: t.TempDir(),
		ModuleFn: stubModuleFn(&calls, map[string]any{"grains.get": "linux"}),
	})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	result, err := eng.RenderString("test", `{{ salt['grains.get']('os') }}`, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "linux" {
		t.Errorf("got %q, want %q", result, "linux")
	}
	if len(calls) != 1 || calls[0].name != "grains.get" {
		t.Fatalf("got calls %v, want one grains.get call", calls)
	}
	if !reflect.DeepEqual(calls[0].args, []any{"os"}) {
		t.Errorf("got args %v, want [os]", calls[0].args)
	}
}

func TestEngine_SaltAccessor_Kwargs(t *testing.T) {
	var calls []recordedCall
	eng, err := NewEngine(EngineConfig{
		BasePath: t.TempDir(),
		ModuleFn: stubModuleFn(&calls, map[string]any{"test.echo": "ok"}),
	})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	result, err := eng.RenderString("test",
		`{{ salt['test.echo']('hello', refresh=true, count=2) }}`, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "ok" {
		t.Errorf("got %q, want %q", result, "ok")
	}
	if len(calls) != 1 {
		t.Fatalf("got %d module calls, want 1", len(calls))
	}
	if !reflect.DeepEqual(calls[0].args, []any{"hello"}) {
		t.Errorf("got args %v, want [hello]", calls[0].args)
	}
	want := map[string]any{"refresh": true, "count": 2}
	if !reflect.DeepEqual(calls[0].kwargs, want) {
		t.Errorf("got kwargs %v, want %v", calls[0].kwargs, want)
	}
}

func TestEngine_SaltAccessor_InSetStatement(t *testing.T) {
	var calls []recordedCall
	eng, err := NewEngine(EngineConfig{
		BasePath: t.TempDir(),
		ModuleFn: stubModuleFn(&calls, map[string]any{"pkg.version": "1.18.0"}),
	})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tpl := `{% set v = salt['pkg.version']('nginx') %}version={{ v }}`
	result, err := eng.RenderString("test", tpl, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "version=1.18.0" {
		t.Errorf("got %q, want %q", result, "version=1.18.0")
	}
}

func TestEngine_SaltAccessor_NilModuleFn(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	_, err = eng.RenderString("test", `{{ salt["pkg.version"]("nginx") }}`, RenderContext{})
	if err == nil {
		t.Fatal("expected render error with nil ModuleFn, got nil")
	}
	if !strings.Contains(err.Error(), "no module dispatcher configured") {
		t.Errorf("error %q should mention missing module dispatcher", err)
	}
	if !strings.Contains(err.Error(), "pkg.version") {
		t.Errorf("error %q should include the module name", err)
	}
}

func TestEngine_SaltAccessor_ModuleError(t *testing.T) {
	moduleFn := func(name string, args []any, kwargs map[string]any) (any, error) {
		return nil, errors.New("package database locked")
	}
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir(), ModuleFn: moduleFn})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	_, err = eng.RenderString("test", `{{ salt["pkg.version"]("nginx") }}`, RenderContext{})
	if err == nil {
		t.Fatal("expected render error when ModuleFn fails, got nil")
	}
	if !strings.Contains(err.Error(), "pkg.version") {
		t.Errorf("error %q should include the module name", err)
	}
	if !strings.Contains(err.Error(), "package database locked") {
		t.Errorf("error %q should include the underlying module error", err)
	}
}

func TestEngine_SaltCall_Fallback(t *testing.T) {
	var calls []recordedCall
	eng, err := NewEngine(EngineConfig{
		BasePath: t.TempDir(),
		ModuleFn: stubModuleFn(&calls, map[string]any{"pkg.version": "1.18.0"}),
	})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	// Dynamic module name via variable: the subscript form can't resolve
	// this (source scan only sees literals), so salt_call covers it.
	tpl := `{% set mod = "pkg.version" %}{{ salt_call(mod, "nginx") }}`
	result, err := eng.RenderString("test", tpl, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "1.18.0" {
		t.Errorf("got %q, want %q", result, "1.18.0")
	}
	if len(calls) != 1 || calls[0].name != "pkg.version" {
		t.Fatalf("got calls %v, want one pkg.version call", calls)
	}
	if !reflect.DeepEqual(calls[0].args, []any{"nginx"}) {
		t.Errorf("got args %v, want [nginx]", calls[0].args)
	}
}

func TestEngine_SaltCall_NilModuleFn(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	_, err = eng.RenderString("test", `{{ salt_call("pkg.version", "nginx") }}`, RenderContext{})
	if err == nil {
		t.Fatal("expected render error with nil ModuleFn, got nil")
	}
	if !strings.Contains(err.Error(), "no module dispatcher configured") {
		t.Errorf("error %q should mention missing module dispatcher", err)
	}
}

func TestEngine_SaltAccessor_RenderFile(t *testing.T) {
	dir := t.TempDir()
	tpl := `nginx: {{ salt['pkg.version']('nginx') }}`
	if err := os.WriteFile(filepath.Join(dir, "web.zy"), []byte(tpl), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	var calls []recordedCall
	eng, err := NewEngine(EngineConfig{
		BasePath: dir,
		ModuleFn: stubModuleFn(&calls, map[string]any{"pkg.version": "1.18.0"}),
	})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	result, err := eng.RenderFile("web.zy", RenderContext{})
	if err != nil {
		t.Fatalf("render file: %v", err)
	}
	if result != "nginx: 1.18.0" {
		t.Errorf("got %q, want %q", result, "nginx: 1.18.0")
	}
	if len(calls) != 1 || calls[0].name != "pkg.version" {
		t.Fatalf("got calls %v, want one pkg.version call", calls)
	}
}

func TestScanSaltModuleNames(t *testing.T) {
	source := `
{{ salt['pkg.version']('nginx') }}
{{ salt["grains.get"]("os") }}
{{ salt[ 'cmd.run' ]('uptime') }}
{{ salt['pkg.version']('curl') }}
`
	names := scanSaltModuleNames(source)
	want := []string{"pkg.version", "grains.get", "cmd.run"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("got %v, want %v", names, want)
	}
}
