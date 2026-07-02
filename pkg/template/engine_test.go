package template

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEngine_RenderString_BasicVariable(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	result, err := eng.RenderString("test", "Hello {{ facts.hostname }}!", RenderContext{
		Facts: map[string]any{"hostname": "web-01"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "Hello web-01!" {
		t.Errorf("got %q, want %q", result, "Hello web-01!")
	}
}

func TestEngine_RenderString_ForLoop(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tpl := `{% for vhost in settings.vhosts %}{{ vhost.name }},{% endfor %}`
	result, err := eng.RenderString("test", tpl, RenderContext{
		Settings: map[string]any{
			"vhosts": []map[string]any{
				{"name": "app1"},
				{"name": "app2"},
			},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "app1,app2," {
		t.Errorf("got %q, want %q", result, "app1,app2,")
	}
}

func TestEngine_RenderString_Conditional(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tpl := `{% if facts.os == "linux" %}linux{% else %}other{% endif %}`
	result, err := eng.RenderString("test", tpl, RenderContext{
		Facts: map[string]any{"os": "linux"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "linux" {
		t.Errorf("got %q, want %q", result, "linux")
	}
}

func TestEngine_RenderString_DefaultFilter(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tpl := `{{ settings.missing | default("fallback") }}`
	result, err := eng.RenderString("test", tpl, RenderContext{
		Settings: map[string]any{},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "fallback" {
		t.Errorf("got %q, want %q", result, "fallback")
	}
}

func TestEngine_RenderString_SetVariable(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tpl := `{% set workers = 4 %}{{ workers }}`
	result, err := eng.RenderString("test", tpl, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "4" {
		t.Errorf("got %q, want %q", result, "4")
	}
}

func TestEngine_RenderString_ExtraContext(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	result, err := eng.RenderString("test", "{{ custom_var }}", RenderContext{
		Extra: map[string]any{"custom_var": "hello"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "hello" {
		t.Errorf("got %q, want %q", result, "hello")
	}
}

func TestEngine_RenderString_BasketFunction(t *testing.T) {
	mockBasket := func(target, function string) []map[string]any {
		return []map[string]any{
			{"peel_id": "web-01", "value": "10.0.1.5"},
			{"peel_id": "web-02", "value": "10.0.1.6"},
		}
	}

	eng, err := NewEngine(EngineConfig{
		BasePath: t.TempDir(),
		BasketFn: mockBasket,
	})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tpl := `{% for r in basket("role:web", "network.ip_addrs") %}{{ r.value }} {% endfor %}`
	result, err := eng.RenderString("test", tpl, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "10.0.1.5 10.0.1.6 " {
		t.Errorf("got %q, want %q", result, "10.0.1.5 10.0.1.6 ")
	}
}

func TestEngine_RenderString_BasketFunctionNil(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tpl := `{% for r in basket("target", "func") %}{{ r.value }}{% endfor %}done`
	result, err := eng.RenderString("test", tpl, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "done" {
		t.Errorf("got %q, want %q", result, "done")
	}
}

func TestEngine_RenderString_NilContexts(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	result, err := eng.RenderString("test", "ok", RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "ok" {
		t.Errorf("got %q, want %q", result, "ok")
	}
}

func TestEngine_RenderString_InvalidTemplate(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	_, err = eng.RenderString("bad", "{% if %}", RenderContext{})
	if err == nil {
		t.Error("expected error for invalid template")
	}
}

func TestEngine_RenderFile(t *testing.T) {
	dir := t.TempDir()
	tplContent := `server {{ facts.hostname }} { listen {{ settings.port }}; }`
	if err := os.WriteFile(filepath.Join(dir, "test.zy"), []byte(tplContent), 0644); err != nil {
		t.Fatalf("write template file: %v", err)
	}

	eng, err := NewEngine(EngineConfig{BasePath: dir})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	result, err := eng.RenderFile("test.zy", RenderContext{
		Facts:    map[string]any{"hostname": "web-01"},
		Settings: map[string]any{"port": 8080},
	})
	if err != nil {
		t.Fatalf("render file: %v", err)
	}
	expected := "server web-01 { listen 8080; }"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestEngine_RenderFile_NotFound(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	_, err = eng.RenderFile("nonexistent.zy", RenderContext{})
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestEngine_FactsGet(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	ctx := RenderContext{
		Facts: map[string]any{
			"os": map[string]any{"family": "debian"},
		},
	}

	tests := []struct {
		name string
		tpl  string
		want string
	}{
		{
			name: "nested_dot_lookup",
			tpl:  `{{ facts_get('os.family') }}`,
			want: "debian",
		},
		{
			name: "missing_key_default",
			tpl:  `{{ facts_get('os.arch', 'unknown') }}`,
			want: "unknown",
		},
		{
			name: "missing_key_no_default_is_none",
			tpl:  `{% if facts_get('nope') is none %}none{% endif %}`,
			want: "none",
		},
		{
			name: "traverse_through_non_map",
			tpl:  `{{ facts_get('os.family.deeper', 'dflt') }}`,
			want: "dflt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := eng.RenderString("test", tt.tpl, ctx)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if result != tt.want {
				t.Errorf("got %q, want %q", result, tt.want)
			}
		})
	}
}

func TestEngine_SaltAliases(t *testing.T) {
	// grains -> facts and pillar -> settings must reference the same data.
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tpl := `{{ grains.hostname }}={{ facts.hostname }}|{{ pillar.env }}={{ settings.env }}`
	result, err := eng.RenderString("test", tpl, RenderContext{
		Facts:    map[string]any{"hostname": "web-01"},
		Settings: map[string]any{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "web-01=web-01|prod=prod" {
		t.Errorf("got %q, want %q", result, "web-01=web-01|prod=prod")
	}
}
