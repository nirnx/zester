package template

import (
	"strings"
	"testing"
)

// renderWithDict renders tpl with a Go map "d" in the context.
func renderWithDict(t *testing.T, tpl string, d map[string]any) string {
	t.Helper()
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}
	result, err := eng.RenderString("test", tpl, RenderContext{
		Extra: map[string]any{"d": d},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return result
}

func TestDictMethod_Get(t *testing.T) {
	tests := []struct {
		name string
		tpl  string
		d    map[string]any
		want string
	}{
		{
			name: "existing_key",
			tpl:  `{{ d.get('shell') }}`,
			d:    map[string]any{"shell": "/bin/zsh"},
			want: "/bin/zsh",
		},
		{
			name: "missing_key_with_default",
			tpl:  `{{ d.get('missing', 'fallback') }}`,
			d:    map[string]any{},
			want: "fallback",
		},
		{
			name: "missing_key_keyword_default",
			tpl:  `{{ d.get('missing', default='kw') }}`,
			d:    map[string]any{},
			want: "kw",
		},
		{
			name: "missing_key_no_default_is_none",
			tpl:  `{% if d.get('missing') is none %}none{% endif %}`,
			d:    map[string]any{},
			want: "none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderWithDict(t, tt.tpl, tt.d); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDictMethod_Keys(t *testing.T) {
	got := renderWithDict(t, `{% for k in d.keys() %}{{ k }},{% endfor %}`, map[string]any{
		"charlie": 3, "alpha": 1, "bravo": 2,
	})
	// keys() sorts for deterministic template output.
	if got != "alpha,bravo,charlie," {
		t.Errorf("got %q, want %q", got, "alpha,bravo,charlie,")
	}
}

func TestDictMethod_Values(t *testing.T) {
	got := renderWithDict(t, `{% for v in d.values() %}{{ v }},{% endfor %}`, map[string]any{
		"b": 2, "a": 1, "c": 3,
	})
	// values() are ordered by sorted key.
	if got != "1,2,3," {
		t.Errorf("got %q, want %q", got, "1,2,3,")
	}
}

func TestDictMethod_Items(t *testing.T) {
	got := renderWithDict(t, `{% for pair in d.items() %}{{ pair.0 }}={{ pair.1 }};{% endfor %}`, map[string]any{
		"b": 2, "a": 1,
	})
	if got != "a=1;b=2;" {
		t.Errorf("got %q, want %q", got, "a=1;b=2;")
	}
}

func TestDictMethod_Update_GoMapSelf(t *testing.T) {
	// Context-provided Go map updated with another context map.
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}
	result, err := eng.RenderString("test", `{% do d.update(other) %}{{ d.a }}/{{ d.b }}`, RenderContext{
		Extra: map[string]any{
			"d":     map[string]any{"a": 1},
			"other": map[string]any{"b": 2},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "1/2" {
		t.Errorf("got %q, want %q", result, "1/2")
	}
}

func TestDictMethod_Update_DictLiteralArg(t *testing.T) {
	// The canonical Salt pattern: {% do d.update({'k': v}) %} where the
	// argument is a template dict literal (evaluates to *exec.Dict).
	got := renderWithDict(t, `{% do d.update({'sudouser': true}) %}{{ d.name }}:{{ d.sudouser }}`, map[string]any{
		"name": "john",
	})
	if got != "john:True" {
		t.Errorf("got %q, want %q", got, "john:True")
	}
}

func TestDictMethod_Update_DictLiteralSelf(t *testing.T) {
	// Self created by {% set %} is *exec.Dict, not a Go map — update must
	// still mutate it in place (replace existing keys, add new ones).
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}
	tpl := `{% set d = {'a': 1, 'b': 1} %}{% do d.update({'b': 2, 'c': 3}) %}{{ d.a }}{{ d.b }}{{ d.c }}`
	result, err := eng.RenderString("test", tpl, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "123" {
		t.Errorf("got %q, want %q", result, "123")
	}
}

func TestDictMethod_Update_NonDictArg(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}
	_, err = eng.RenderString("test", `{% do d.update('notadict') %}`, RenderContext{
		Extra: map[string]any{"d": map[string]any{}},
	})
	if err == nil {
		t.Fatal("expected error for non-dict argument")
	}
	if !strings.Contains(err.Error(), "argument must be a dict") {
		t.Errorf("error %q does not contain %q", err.Error(), "argument must be a dict")
	}
}

func TestDictMethod_ArgumentsRejected(t *testing.T) {
	// keys/values/items take no arguments.
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}
	_, err = eng.RenderString("test", `{{ d.keys('bogus') }}`, RenderContext{
		Extra: map[string]any{"d": map[string]any{"a": 1}},
	})
	if err == nil {
		t.Fatal("expected error for keys() with argument")
	}
}
