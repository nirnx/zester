package template

import (
	"os/user"
	"reflect"
	"testing"
)

func TestMakePillarGet(t *testing.T) {
	stgs := map[string]any{
		"users": map[string]any{
			"john": map[string]any{
				"shell": "/bin/zsh",
				"uid":   1000,
			},
		},
		"flat": "value",
	}
	fn := makePillarGet(stgs)

	tests := []struct {
		name string
		key  string
		args []any
		want any
	}{
		{name: "nested_lookup", key: "users:john:shell", want: "/bin/zsh"},
		{name: "top_level", key: "flat", want: "value"},
		{name: "intermediate_map", key: "users:john", want: stgs["users"].(map[string]any)["john"]},
		{name: "missing_key_no_default", key: "users:jane:shell", want: nil},
		{name: "missing_key_with_default", key: "users:jane:shell", args: []any{"/bin/bash"}, want: "/bin/bash"},
		{name: "traverse_through_non_map", key: "flat:deeper", args: []any{"dflt"}, want: "dflt"},
		{name: "traverse_through_non_map_no_default", key: "flat:deeper", want: nil},
		{name: "missing_top_level_with_default", key: "nope", args: []any{42}, want: 42},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fn(tt.key, tt.args...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("pillar_get(%q) = %#v, want %#v", tt.key, got, tt.want)
			}
		})
	}
}

func TestPillarGet_Template(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	ctx := RenderContext{
		Settings: map[string]any{
			"users": map[string]any{"john": map[string]any{"shell": "/bin/zsh"}},
		},
	}

	tests := []struct {
		name string
		tpl  string
		want string
	}{
		{
			name: "found",
			tpl:  `{{ pillar_get('users:john:shell', '/bin/bash') }}`,
			want: "/bin/zsh",
		},
		{
			name: "default_used",
			tpl:  `{{ pillar_get('users:jane:shell', '/bin/bash') }}`,
			want: "/bin/bash",
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

func TestGrainsFilterBy_Template(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	lookupSet := `{% set lookup = {'debian': {'pkg': 'apache2', 'shell': '/bin/bash'}, 'rhel': {'pkg': 'httpd'}, 'default': {'pkg': 'generic'}} %}`

	tests := []struct {
		name  string
		tpl   string
		facts map[string]any
		want  string
	}{
		{
			name:  "default_grain_os_family",
			tpl:   lookupSet + `{% set cfg = grains_filter_by(lookup) %}{{ cfg.pkg }}`,
			facts: map[string]any{"os": map[string]any{"family": "debian"}},
			want:  "apache2",
		},
		{
			name:  "explicit_grain_key",
			tpl:   lookupSet + `{% set cfg = grains_filter_by(lookup, 'osname') %}{{ cfg.pkg }}`,
			facts: map[string]any{"osname": "rhel"},
			want:  "httpd",
		},
		{
			name:  "grain_value_not_in_lookup_falls_to_default",
			tpl:   lookupSet + `{% set cfg = grains_filter_by(lookup) %}{{ cfg.pkg }}`,
			facts: map[string]any{"os": map[string]any{"family": "arch"}},
			want:  "generic",
		},
		{
			name:  "missing_grain_key_falls_to_default",
			tpl:   lookupSet + `{% set cfg = grains_filter_by(lookup) %}{{ cfg.pkg }}`,
			facts: map[string]any{},
			want:  "generic",
		},
		{
			name:  "merge_dict_third_arg",
			tpl:   lookupSet + `{% set cfg = grains_filter_by(lookup, 'os.family', {'pkg': 'overridden', 'extra': 'x'}) %}{{ cfg.pkg }}/{{ cfg.extra }}/{{ cfg.shell }}`,
			facts: map[string]any{"os": map[string]any{"family": "debian"}},
			want:  "overridden/x//bin/bash",
		},
		{
			name:  "merge_dict_as_second_arg",
			tpl:   lookupSet + `{% set cfg = grains_filter_by(lookup, {'extra': 'y'}) %}{{ cfg.pkg }}/{{ cfg.extra }}`,
			facts: map[string]any{"os": map[string]any{"family": "debian"}},
			want:  "apache2/y",
		},
		{
			name:  "base_dict_fourth_arg_goes_under",
			tpl:   lookupSet + `{% set cfg = grains_filter_by(lookup, 'os.family', {}, {'pkg': 'base-pkg', 'base_only': 'b'}) %}{{ cfg.pkg }}/{{ cfg.base_only }}`,
			facts: map[string]any{"os": map[string]any{"family": "debian"}},
			want:  "apache2/b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := eng.RenderString("test", tt.tpl, RenderContext{Facts: tt.facts})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if result != tt.want {
				t.Errorf("got %q, want %q", result, tt.want)
			}
		})
	}
}

func TestGrainsFilterBy_NoDefaultReturnsNone(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	// No matching grain and no 'default' key -> nil -> falsy in templates.
	tpl := `{% set cfg = grains_filter_by({'debian': {'pkg': 'apache2'}}) %}{% if cfg %}have{% else %}none{% endif %}`
	result, err := eng.RenderString("test", tpl, RenderContext{Facts: map[string]any{}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "none" {
		t.Errorf("got %q, want %q", result, "none")
	}
}

func TestGrainsFilterBy_NonDictLookupReturnsNone(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tpl := `{% set cfg = grains_filter_by('notadict') %}{% if cfg %}have{% else %}none{% endif %}`
	result, err := eng.RenderString("test", tpl, RenderContext{
		Facts: map[string]any{"os": map[string]any{"family": "debian"}},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "none" {
		t.Errorf("got %q, want %q", result, "none")
	}
}

func TestGrainsFilterBy_NonMapLookupValue(t *testing.T) {
	// A lookup entry that isn't a dict is returned as-is (no merge applied).
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tpl := `{{ grains_filter_by({'debian': 'just-a-string'}) }}`
	result, err := eng.RenderString("test", tpl, RenderContext{
		Facts: map[string]any{"os": map[string]any{"family": "debian"}},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "just-a-string" {
		t.Errorf("got %q, want %q", result, "just-a-string")
	}
}

func TestUserInfo_CurrentUser(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Skipf("cannot determine current user: %v", err)
	}

	info := userInfo(current.Username)
	if info["name"] != current.Username {
		t.Errorf("name = %v, want %q", info["name"], current.Username)
	}
	if info["uid"] != current.Uid {
		t.Errorf("uid = %v, want %q", info["uid"], current.Uid)
	}
	if info["home"] != current.HomeDir {
		t.Errorf("home = %v, want %q", info["home"], current.HomeDir)
	}
	if _, ok := info["groups"]; !ok {
		t.Error("groups key missing")
	}
}

func TestUserInfo_UnknownUser(t *testing.T) {
	info := userInfo("zester-no-such-user-xyz")
	if len(info) != 0 {
		t.Errorf("expected empty map for unknown user, got %#v", info)
	}
}

func TestCmdHasExec(t *testing.T) {
	if !cmdHasExec("sh") {
		t.Error("cmd_has_exec('sh') = false, want true")
	}
	if cmdHasExec("zester-no-such-binary-xyz") {
		t.Error("cmd_has_exec for bogus binary = true, want false")
	}
}

func TestCmdHasExec_Template(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	tpl := `{% if cmd_has_exec('sh') %}yes{% else %}no{% endif %}`
	result, err := eng.RenderString("test", tpl, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "yes" {
		t.Errorf("got %q, want %q", result, "yes")
	}
}

func TestFileDirname(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/etc/nginx/nginx.conf", "/etc/nginx"},
		{"/etc", "/"},
		{"relative/file.txt", "relative"},
		{"file.txt", "."},
	}
	for _, tt := range tests {
		if got := fileDirname(tt.path); got != tt.want {
			t.Errorf("file_dirname(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestFileDirname_Template(t *testing.T) {
	eng, err := NewEngine(EngineConfig{BasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}

	result, err := eng.RenderString("test", `{{ file_dirname('/etc/nginx/nginx.conf') }}`, RenderContext{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result != "/etc/nginx" {
		t.Errorf("got %q, want %q", result, "/etc/nginx")
	}
}

func TestDeepMerge(t *testing.T) {
	tests := []struct {
		name    string
		base    map[string]any
		overlay map[string]any
		want    map[string]any
	}{
		{
			name:    "disjoint_keys",
			base:    map[string]any{"a": 1},
			overlay: map[string]any{"b": 2},
			want:    map[string]any{"a": 1, "b": 2},
		},
		{
			name:    "overlay_wins_scalars",
			base:    map[string]any{"a": 1},
			overlay: map[string]any{"a": 2},
			want:    map[string]any{"a": 2},
		},
		{
			name:    "nested_maps_merged",
			base:    map[string]any{"m": map[string]any{"x": 1, "y": 2}},
			overlay: map[string]any{"m": map[string]any{"y": 3, "z": 4}},
			want:    map[string]any{"m": map[string]any{"x": 1, "y": 3, "z": 4}},
		},
		{
			name:    "map_replaced_by_scalar",
			base:    map[string]any{"m": map[string]any{"x": 1}},
			overlay: map[string]any{"m": "scalar"},
			want:    map[string]any{"m": "scalar"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseCopy := map[string]any{}
			for k, v := range tt.base {
				baseCopy[k] = v
			}
			got := deepMerge(tt.base, tt.overlay)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("deepMerge = %#v, want %#v", got, tt.want)
			}
			// Base must not be mutated.
			if !reflect.DeepEqual(tt.base, baseCopy) {
				t.Errorf("base mutated: %#v", tt.base)
			}
		})
	}
}
