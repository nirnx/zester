package template

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeFiles creates files under dir and returns an engine rooted there.
func newEngineWithFiles(t *testing.T, files map[string]string) *Engine {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	eng, err := NewEngine(EngineConfig{BasePath: dir})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}
	return eng
}

func TestImportYAML(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		tpl   string
		ctx   RenderContext
		want  string
	}{
		{
			name: "simple_map",
			files: map[string]string{
				"defaults.yaml": "pkg: nginx\nport: 8080\n",
			},
			tpl:  `{% import_yaml 'defaults.yaml' as d %}{{ d.pkg }}:{{ d.port }}`,
			want: "nginx:8080",
		},
		{
			name: "nested_map",
			files: map[string]string{
				"defaults.yaml": "web:\n  server: apache\n  workers: 4\n",
			},
			tpl:  `{% import_yaml 'defaults.yaml' as d %}{{ d.web.server }}/{{ d.web.workers }}`,
			want: "apache/4",
		},
		{
			name: "list_of_maps",
			files: map[string]string{
				"users.yaml": "users:\n  - name: alice\n  - name: bob\n",
			},
			tpl:  `{% import_yaml 'users.yaml' as u %}{% for user in u.users %}{{ user.name }},{% endfor %}`,
			want: "alice,bob,",
		},
		{
			name: "non_string_keys_normalized",
			files: map[string]string{
				// yaml.v3 produces map[string]any for string keys, but integer
				// keys yield map[int]any inside — normalizeYAML must stringify them.
				"ports.yaml": "ports:\n  8080: http\n  8443: https\n",
			},
			tpl:  `{% import_yaml 'ports.yaml' as p %}{{ p.ports.get('8080') }}-{{ p.ports.get('8443') }}`,
			want: "http-https",
		},
		{
			name: "path_from_variable",
			files: map[string]string{
				"data.yaml": "key: value\n",
			},
			tpl:  `{% set p = 'data.yaml' %}{% import_yaml p as d %}{{ d.key }}`,
			want: "value",
		},
		{
			name: "subdirectory_path",
			files: map[string]string{
				"common/map.yaml": "os: linux\n",
			},
			tpl:  `{% import_yaml 'common/map.yaml' as m %}{{ m.os }}`,
			want: "linux",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := newEngineWithFiles(t, tt.files)
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

func TestImportYAML_Errors(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		tpl     string
		wantErr string
	}{
		{
			name:    "missing_file",
			files:   map[string]string{},
			tpl:     `{% import_yaml 'nope.yaml' as d %}{{ d }}`,
			wantErr: "nope.yaml",
		},
		{
			name: "invalid_yaml",
			files: map[string]string{
				"bad.yaml": "key: [unclosed\n",
			},
			tpl:     `{% import_yaml 'bad.yaml' as d %}{{ d }}`,
			wantErr: "import_yaml: parse",
		},
		{
			name:    "missing_as_keyword",
			files:   map[string]string{"x.yaml": "a: 1\n"},
			tpl:     `{% import_yaml 'x.yaml' d %}`,
			wantErr: "expected 'as'",
		},
		{
			name:    "missing_var_name",
			files:   map[string]string{"x.yaml": "a: 1\n"},
			tpl:     `{% import_yaml 'x.yaml' as %}`,
			wantErr: "expected variable name",
		},
		{
			name:    "extra_arguments",
			files:   map[string]string{"x.yaml": "a: 1\n"},
			tpl:     `{% import_yaml 'x.yaml' as d extra %}`,
			wantErr: "unexpected extra arguments",
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

func TestNormalizeYAML(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want any
	}{
		{
			name: "map_any_keys_stringified",
			in:   map[any]any{1: "one", true: "yes"},
			want: map[string]any{"1": "one", "true": "yes"},
		},
		{
			name: "nested_map_inside_string_map",
			in:   map[string]any{"outer": map[any]any{2: "two"}},
			want: map[string]any{"outer": map[string]any{"2": "two"}},
		},
		{
			name: "list_elements_normalized",
			in:   []any{map[any]any{3: "three"}, "plain"},
			want: []any{map[string]any{"3": "three"}, "plain"},
		},
		{
			name: "scalar_passthrough",
			in:   42,
			want: 42,
		},
		{
			name: "nil_passthrough",
			in:   nil,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeYAML(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}
