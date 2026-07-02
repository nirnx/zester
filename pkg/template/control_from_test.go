package template

import (
	"strings"
	"testing"
)

func TestFromImport_Variable(t *testing.T) {
	eng := newEngineWithFiles(t, map[string]string{
		"map.zy": `{% set mymap = {'pkg': 'nginx', 'port': 80} %}`,
	})

	tpl := `{% from "map.zy" import mymap %}{{ mymap.pkg }}:{{ mymap.port }}`
	result, err := eng.RenderString("test", tpl, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "nginx:80" {
		t.Errorf("got %q, want %q", result, "nginx:80")
	}
}

func TestFromImport_VariableWithAlias(t *testing.T) {
	eng := newEngineWithFiles(t, map[string]string{
		"map.zy": `{% set mymap = {'pkg': 'nginx'} %}`,
	})

	tpl := `{% from "map.zy" import mymap as m %}{{ m.pkg }}`
	result, err := eng.RenderString("test", tpl, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "nginx" {
		t.Errorf("got %q, want %q", result, "nginx")
	}
}

func TestFromImport_WithContext(t *testing.T) {
	// "with context" makes the current render's facts visible to the
	// imported template — the core Salt map.jinja pattern.
	eng := newEngineWithFiles(t, map[string]string{
		"map.zy": `{% set pkgname = facts.os + '-pkg' %}`,
	})

	tpl := `{% from "map.zy" import pkgname with context %}{{ pkgname }}`
	result, err := eng.RenderString("test", tpl, RenderContext{
		Facts: map[string]any{"os": "debian"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "debian-pkg" {
		t.Errorf("got %q, want %q", result, "debian-pkg")
	}
}

func TestFromImport_WithoutContext(t *testing.T) {
	// "without context" isolates the imported template from the caller's
	// variables — imports that don't depend on the caller still work.
	eng := newEngineWithFiles(t, map[string]string{
		"map.zy": `{% set standalone = 'fixed' %}`,
	})

	tpl := `{% from "map.zy" import standalone without context %}{{ standalone }}`
	result, err := eng.RenderString("test", tpl, RenderContext{
		Facts: map[string]any{"os": "debian"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "fixed" {
		t.Errorf("got %q, want %q", result, "fixed")
	}
}

func TestFromImport_Macro(t *testing.T) {
	eng := newEngineWithFiles(t, map[string]string{
		"macros.zy": `{% macro greet(name) %}Hello {{ name }}{% endmacro %}`,
	})

	tpl := `{% from "macros.zy" import greet %}{{ greet("world") }}`
	result, err := eng.RenderString("test", tpl, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "Hello world" {
		t.Errorf("got %q, want %q", result, "Hello world")
	}
}

func TestFromImport_MultipleNames(t *testing.T) {
	eng := newEngineWithFiles(t, map[string]string{
		"shared.zy": `{% set first = 'one' %}{% set second = 'two' %}`,
	})

	tpl := `{% from "shared.zy" import first, second %}{{ first }}/{{ second }}`
	result, err := eng.RenderString("test", tpl, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "one/two" {
		t.Errorf("got %q, want %q", result, "one/two")
	}
}

func TestFromImport_Errors(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		tpl     string
		wantErr string
	}{
		{
			name:    "missing_file",
			files:   map[string]string{},
			tpl:     `{% from "nope.zy" import x %}`,
			wantErr: "nope.zy",
		},
		{
			name:    "name_not_found",
			files:   map[string]string{"map.zy": `{% set other = 1 %}`},
			tpl:     `{% from "map.zy" import missing %}`,
			wantErr: `name "missing" not found`,
		},
		{
			name:    "missing_import_keyword",
			files:   map[string]string{"map.zy": `{% set x = 1 %}`},
			tpl:     `{% from "map.zy" x %}`,
			wantErr: "expected 'import' keyword",
		},
		{
			name:    "missing_filename",
			files:   map[string]string{},
			tpl:     `{% from %}`,
			wantErr: "expected filename expression",
		},
		{
			name:    "missing_alias_after_as",
			files:   map[string]string{"map.zy": `{% set x = 1 %}`},
			tpl:     `{% from "map.zy" import x as %}`,
			wantErr: "expected alias after 'as'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := newEngineWithFiles(t, tt.files)
			_, err := eng.RenderString("test", tt.tpl, RenderContext{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
