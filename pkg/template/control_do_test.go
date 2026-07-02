package template

import (
	"strings"
	"testing"
)

func TestDo(t *testing.T) {
	tests := []struct {
		name string
		tpl  string
		ctx  RenderContext
		want string
	}{
		{
			name: "list_append_side_effect",
			tpl:  `{% set l = mlist() %}{% do l.Append('a') %}{% do l.Append('b') %}{% for x in l.Items() %}{{ x }},{% endfor %}`,
			want: "a,b,",
		},
		{
			name: "produces_no_output",
			tpl:  `before{% do mlist().Append(1) %}after`,
			want: "beforeafter",
		},
		{
			name: "dict_update_side_effect",
			tpl:  `{% do d.update({'b': 2}) %}{{ d.a }}{{ d.b }}`,
			ctx:  RenderContext{Extra: map[string]any{"d": map[string]any{"a": 1}}},
			want: "12",
		},
		{
			name: "plain_expression_discarded",
			tpl:  `{% do 1 + 2 %}ok`,
			want: "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
			if err != nil {
				t.Fatalf("create engine: %v", err)
			}
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

func TestDo_Errors(t *testing.T) {
	tests := []struct {
		name    string
		tpl     string
		wantErr string
	}{
		{
			name:    "missing_expression",
			tpl:     `{% do %}`,
			wantErr: "do: expected expression",
		},
		{
			name:    "extra_arguments",
			tpl:     `{% do 1 2 %}`,
			wantErr: "unexpected extra arguments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
			if err != nil {
				t.Fatalf("create engine: %v", err)
			}
			_, err = eng.RenderString("test", tt.tpl, RenderContext{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
