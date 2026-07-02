package template

import (
	"strings"
	"testing"
)

func TestSettingsDecryptFilter(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tpl := `{{ settings.db_password | settings_decrypt }}`
	result, err := eng.RenderString("test", tpl, RenderContext{
		Settings: map[string]any{"db_password": "ENC[nkey,abc123]"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "ENC[nkey,abc123]" {
		t.Errorf("got %q, want passthrough of encrypted value", result)
	}
}

func TestYamlEncodeFilter(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tests := []struct {
		name     string
		tpl      string
		ctx      RenderContext
		contains string
	}{
		{
			name:     "string",
			tpl:      `{{ settings.name | yaml_encode }}`,
			ctx:      RenderContext{Settings: map[string]any{"name": "hello"}},
			contains: "hello",
		},
		{
			name:     "integer",
			tpl:      `{{ settings.count | yaml_encode }}`,
			ctx:      RenderContext{Settings: map[string]any{"count": 42}},
			contains: "42",
		},
		{
			name:     "boolean_true",
			tpl:      `{{ settings.enabled | yaml_encode }}`,
			ctx:      RenderContext{Settings: map[string]any{"enabled": true}},
			contains: "true",
		},
		{
			name:     "boolean_false",
			tpl:      `{{ settings.enabled | yaml_encode }}`,
			ctx:      RenderContext{Settings: map[string]any{"enabled": false}},
			contains: "false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := eng.RenderString("test", tt.tpl, tt.ctx)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if !strings.Contains(result, tt.contains) {
				t.Errorf("got %q, want it to contain %q", result, tt.contains)
			}
		})
	}
}

func TestToJSONFilter(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	result, err := eng.RenderString("test", `{{ settings.name | to_json }}`, RenderContext{
		Settings: map[string]any{"name": "hello"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != `"hello"` {
		t.Errorf("got %q, want %q", result, `"hello"`)
	}
}

func TestJSONFilterAlias(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tests := []struct {
		name string
		tpl  string
		ctx  RenderContext
		want string
	}{
		{
			name: "string",
			tpl:  `{{ settings.name | json }}`,
			ctx:  RenderContext{Settings: map[string]any{"name": "hello"}},
			want: `"hello"`,
		},
		{
			name: "map_sorted_keys",
			tpl:  `{{ settings.m | json }}`,
			ctx:  RenderContext{Settings: map[string]any{"m": map[string]any{"b": 2, "a": 1}}},
			want: `{"a":1,"b":2}`,
		},
		{
			name: "nil_is_null",
			tpl:  `{{ settings.missing | json }}`,
			ctx:  RenderContext{Settings: map[string]any{}},
			want: "null",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := eng.RenderString("test", tt.tpl, tt.ctx)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if result != tt.want {
				t.Errorf("got %q, want %q", result, tt.want)
			}
		})
	}
}

func TestYamlEncodeFilter_ComplexAndNil(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tests := []struct {
		name string
		tpl  string
		ctx  RenderContext
		want string
	}{
		{
			name: "map_as_json",
			tpl:  `{{ settings.m | yaml_encode }}`,
			ctx:  RenderContext{Settings: map[string]any{"m": map[string]any{"a": 1}}},
			want: `{"a":1}`,
		},
		{
			name: "list_as_json",
			tpl:  `{{ settings.l | yaml_encode }}`,
			ctx:  RenderContext{Settings: map[string]any{"l": []any{"x", "y"}}},
			want: `["x","y"]`,
		},
		{
			name: "nil_is_null",
			tpl:  `{{ settings.missing | yaml_encode }}`,
			ctx:  RenderContext{Settings: map[string]any{}},
			want: "null",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := eng.RenderString("test", tt.tpl, tt.ctx)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if result != tt.want {
				t.Errorf("got %q, want %q", result, tt.want)
			}
		})
	}
}

// regex_match is documented (docs/templating/filters.md) as substring
// matching despite its name — these tests pin the documented behavior.
func TestRegexMatchFilter(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tests := []struct {
		name string
		tpl  string
		ctx  RenderContext
		want string
	}{
		{
			name: "substring_present",
			tpl:  `{{ settings.host | regex_match("prod") }}`,
			ctx:  RenderContext{Settings: map[string]any{"host": "web-prod-01"}},
			want: "True",
		},
		{
			name: "substring_absent",
			tpl:  `{{ settings.host | regex_match("prod") }}`,
			ctx:  RenderContext{Settings: map[string]any{"host": "web-dev-01"}},
			want: "False",
		},
		{
			name: "no_pattern_arg_is_false",
			tpl:  `{{ settings.host | regex_match }}`,
			ctx:  RenderContext{Settings: map[string]any{"host": "anything"}},
			want: "False",
		},
		{
			name: "usable_in_condition",
			tpl:  `{% if settings.host | regex_match("prod") %}yes{% else %}no{% endif %}`,
			ctx:  RenderContext{Settings: map[string]any{"host": "prod-db"}},
			want: "yes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := eng.RenderString("test", tt.tpl, tt.ctx)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if result != tt.want {
				t.Errorf("got %q, want %q", result, tt.want)
			}
		})
	}
}

func TestDictMergeFilter(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tests := []struct {
		name string
		tpl  string
		ctx  RenderContext
		want string
	}{
		{
			name: "override_wins",
			tpl:  `{% set m = base | dict_merge(override) %}{{ m.a }}/{{ m.b }}/{{ m.c }}`,
			ctx: RenderContext{Extra: map[string]any{
				"base":     map[string]any{"a": 1, "b": 1},
				"override": map[string]any{"b": 2, "c": 3},
			}},
			want: "1/2/3",
		},
		{
			name: "shallow_merge_replaces_nested_map",
			tpl:  `{% set m = base | dict_merge(override) %}{{ m.nested.y }}/{{ m.nested.get('x', 'gone') }}`,
			ctx: RenderContext{Extra: map[string]any{
				"base":     map[string]any{"nested": map[string]any{"x": 1}},
				"override": map[string]any{"nested": map[string]any{"y": 2}},
			}},
			want: "2/gone",
		},
		{
			name: "non_dict_input_passthrough",
			tpl:  `{{ base | dict_merge(override) }}`,
			ctx: RenderContext{Extra: map[string]any{
				"base":     "just-a-string",
				"override": map[string]any{"a": 1},
			}},
			want: "just-a-string",
		},
		{
			name: "no_args_passthrough",
			tpl:  `{% set m = base | dict_merge %}{{ m.a }}`,
			ctx: RenderContext{Extra: map[string]any{
				"base": map[string]any{"a": 1},
			}},
			want: "1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := eng.RenderString("test", tt.tpl, tt.ctx)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if result != tt.want {
				t.Errorf("got %q, want %q", result, tt.want)
			}
		})
	}
}

func TestLengthFilter(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tests := []struct {
		name string
		tpl  string
		ctx  RenderContext
		want string
	}{
		{
			name: "mutable_list_via_lenner",
			tpl:  `{% set l = mlist(1, 2, 3) %}{{ l | length }}`,
			want: "3",
		},
		{
			name: "empty_mutable_list",
			tpl:  `{{ mlist() | length }}`,
			want: "0",
		},
		{
			name: "string_fallback",
			tpl:  `{{ "abc" | length }}`,
			want: "3",
		},
		{
			name: "go_slice_fallback",
			tpl:  `{{ items | length }}`,
			ctx:  RenderContext{Extra: map[string]any{"items": []any{"a", "b"}}},
			want: "2",
		},
		{
			name: "map_fallback",
			tpl:  `{{ settings | length }}`,
			ctx:  RenderContext{Settings: map[string]any{"a": 1, "b": 2}},
			want: "2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := eng.RenderString("test", tt.tpl, tt.ctx)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if result != tt.want {
				t.Errorf("got %q, want %q", result, tt.want)
			}
		})
	}
}
