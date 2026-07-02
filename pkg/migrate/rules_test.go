package migrate

import (
	"strings"
	"testing"
)

func TestSaltFunctions(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		changes int
	}{
		{
			name:    "pillar.get",
			input:   `{% set port = salt['pillar.get']('nginx:port', 80) %}`,
			want:    `{% set port = pillar_get('nginx:port', 80) %}`,
			changes: 1,
		},
		{
			name:    "grains.filter_by",
			input:   `{% set lookup = salt['grains.filter_by'](lookup_table) %}`,
			want:    `{% set lookup = grains_filter_by(lookup_table) %}`,
			changes: 1,
		},
		{
			name:    "user.info",
			input:   `{% set info = salt['user.info']('root') %}`,
			want:    `{% set info = user_info('root') %}`,
			changes: 1,
		},
		{
			name:    "cmd.has_exec",
			input:   `{% if salt['cmd.has_exec']('nginx') %}`,
			want:    `{% if cmd_has_exec('nginx') %}`,
			changes: 1,
		},
		{
			name:    "file.dirname",
			input:   `{% set dir = salt['file.dirname']('/etc/nginx/nginx.conf') %}`,
			want:    `{% set dir = file_dirname('/etc/nginx/nginx.conf') %}`,
			changes: 1,
		},
		{
			name:    "multiple on one line",
			input:   `{% set x = salt['pillar.get']('a') %}{{ salt['user.info']('root') }}`,
			want:    `{% set x = pillar_get('a') %}{{ user_info('root') }}`,
			changes: 1,
		},
		{
			name:    "no match",
			input:   `{% set x = pillar_get('key') %}`,
			want:    `{% set x = pillar_get('key') %}`,
			changes: 0,
		},
		{
			name:    "space between ] and (",
			input:   `{{ salt['pillar.get']  ('key', 'default') }}`,
			want:    `{{ pillar_get('key', 'default') }}`,
			changes: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Migrate("test.sls", tt.input, Options{})
			if r.Content != tt.want {
				t.Errorf("content:\n got  %q\n want %q", r.Content, tt.want)
			}
			if len(r.Changes) != tt.changes {
				t.Errorf("changes: got %d, want %d", len(r.Changes), tt.changes)
			}
			for _, c := range r.Changes {
				if c.Rule != "salt-functions" {
					t.Errorf("rule: got %q, want %q", c.Rule, "salt-functions")
				}
			}
		})
	}
}

func TestUnknownSaltCalls(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		warnings int
	}{
		{
			name:     "unknown mine.get",
			input:    `{{ salt['mine.get']('web*', 'network.ip_addrs') }}`,
			warnings: 1,
		},
		{
			name:     "no salt calls",
			input:    `{{ pillar_get('key') }}`,
			warnings: 0,
		},
		{
			name:     "known calls produce no warnings after rule 1",
			input:    `{{ salt['pillar.get']('key') }}`,
			warnings: 0, // rule 1 transforms it, rule 2 sees no remaining salt[]
		},
		{
			name:     "multiple unknown calls",
			input:    "{{ salt['mine.get']('a') }}\n{{ salt['cp.get_file']('b') }}",
			warnings: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Migrate("test.sls", tt.input, Options{})
			if len(r.Warnings) != tt.warnings {
				t.Errorf("warnings: got %d, want %d", len(r.Warnings), tt.warnings)
				for _, w := range r.Warnings {
					t.Logf("  warning: %s", w.Message)
				}
			}
		})
	}
}

func TestMlistDetection(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     string
		changes  int
		warnings int
	}{
		{
			name: "basic append",
			input: `{% set pkgs = [] %}
{% for p in base_pkgs %}
{% do pkgs.append(p) %}
{% endfor %}`,
			want: `{% set pkgs = mlist() %}
{% for p in base_pkgs %}
{% do pkgs.Append(p) %}
{% endfor %}`,
			changes:  2, // declaration + append
			warnings: 0,
		},
		{
			name: "extend",
			input: `{% set items = [] %}
{% do items.extend(extra) %}`,
			want: `{% set items = mlist() %}
{% do items.Extend(extra) %}`,
			changes:  2,
			warnings: 0,
		},
		{
			name: "no append used",
			input: `{% set items = [] %}
{{ items }}`,
			want: `{% set items = [] %}
{{ items }}`,
			changes:  0,
			warnings: 0,
		},
		{
			name:     "append on undeclared var",
			input:    `{% do mystery.append('x') %}`,
			want:     `{% do mystery.append('x') %}`,
			changes:  0,
			warnings: 1,
		},
		{
			name: "whitespace in set tag",
			input: `{%- set pkgs = [] -%}
{% do pkgs.append('vim') %}`,
			want: `{%- set pkgs = mlist() -%}
{% do pkgs.Append('vim') %}`,
			changes:  2,
			warnings: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Migrate("test.sls", tt.input, Options{})
			if r.Content != tt.want {
				t.Errorf("content:\n got  %q\n want %q", r.Content, tt.want)
			}
			// Count only mlist-rule changes.
			mlistChanges := 0
			for _, c := range r.Changes {
				if c.Rule == "mlist" {
					mlistChanges++
				}
			}
			if mlistChanges != tt.changes {
				t.Errorf("changes: got %d, want %d", mlistChanges, tt.changes)
			}
			if len(r.Warnings) != tt.warnings {
				t.Errorf("warnings: got %d, want %d", len(r.Warnings), tt.warnings)
			}
		})
	}
}

func TestOctalModes(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		changes int
	}{
		{
			name:    "basic mode",
			input:   `    - mode: 0644`,
			want:    `    - mode: "0644"`,
			changes: 1,
		},
		{
			name:    "dir_mode",
			input:   `    - dir_mode: 0755`,
			want:    `    - dir_mode: "0755"`,
			changes: 1,
		},
		{
			name:    "file_mode",
			input:   `    - file_mode: 0600`,
			want:    `    - file_mode: "0600"`,
			changes: 1,
		},
		{
			name:    "already quoted",
			input:   `    - mode: "0644"`,
			want:    `    - mode: "0644"`,
			changes: 0,
		},
		{
			name:    "non-octal number",
			input:   `    - mode: 644`,
			want:    `    - mode: 644`,
			changes: 0,
		},
		{
			name:    "three-digit octal",
			input:   `    - mode: 0755`,
			want:    `    - mode: "0755"`,
			changes: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Migrate("test.sls", tt.input, Options{})
			if r.Content != tt.want {
				t.Errorf("content:\n got  %q\n want %q", r.Content, tt.want)
			}
			octalChanges := 0
			for _, c := range r.Changes {
				if c.Rule == "octal-modes" {
					octalChanges++
				}
			}
			if octalChanges != tt.changes {
				t.Errorf("changes: got %d, want %d", octalChanges, tt.changes)
			}
		})
	}
}

func TestRequisiteWarnings(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		warnings int
		contains string // substring expected in warning message
	}{
		{
			name: "require block",
			input: `    - require:
      - pkg: nginx
      - file: /etc/nginx/nginx.conf`,
			warnings: 1,
			contains: `pkg.installed:nginx`,
		},
		{
			name: "watch block",
			input: `    - watch:
      - file: /etc/nginx/nginx.conf`,
			warnings: 1,
			contains: `file.managed:/etc/nginx/nginx.conf`,
		},
		{
			name:     "no requisites",
			input:    `    - name: nginx`,
			warnings: 0,
		},
		{
			name: "require keeps name",
			input: `    - require:
      - pkg: nginx`,
			warnings: 1,
			contains: "require:",
		},
		{
			name: "watch keeps name",
			input: `    - watch:
      - service: nginx`,
			warnings: 1,
			contains: "watch:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Migrate("test.sls", tt.input, Options{})
			if len(r.Warnings) != tt.warnings {
				t.Errorf("warnings: got %d, want %d", len(r.Warnings), tt.warnings)
				for _, w := range r.Warnings {
					t.Logf("  warning: %s", w.Message)
				}
			}
			if tt.contains != "" && len(r.Warnings) > 0 {
				found := false
				for _, w := range r.Warnings {
					if strings.Contains(w.Message, tt.contains) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected warning containing %q", tt.contains)
				}
			}
		})
	}
}

func TestVariableRename(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		changes int
	}{
		{
			name:    "grains dot access",
			input:   `{{ grains.os.family }}`,
			want:    `{{ facts.os.family }}`,
			changes: 1,
		},
		{
			name:    "grains bracket access",
			input:   `{{ grains['os']['family'] }}`,
			want:    `{{ facts['os']['family'] }}`,
			changes: 1,
		},
		{
			name:    "pillar dot access",
			input:   `{{ pillar.nginx.port }}`,
			want:    `{{ settings.nginx.port }}`,
			changes: 1,
		},
		{
			name:    "pillar bracket access",
			input:   `{{ pillar['nginx']['port'] }}`,
			want:    `{{ settings['nginx']['port'] }}`,
			changes: 1,
		},
		{
			name:    "does not rename pillar_get",
			input:   `{{ pillar_get('key') }}`,
			want:    `{{ pillar_get('key') }}`,
			changes: 0,
		},
		{
			name:    "does not rename grains_filter_by",
			input:   `{{ grains_filter_by(lookup) }}`,
			want:    `{{ grains_filter_by(lookup) }}`,
			changes: 0,
		},
		{
			name:    "mixed line",
			input:   `{% if grains.os == 'Debian' and pillar.pkg.install %}`,
			want:    `{% if facts.os == 'Debian' and settings.pkg.install %}`,
			changes: 1, // both on same line = 1 change
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Migrate("test.sls", tt.input, Options{RenameVars: true})
			if r.Content != tt.want {
				t.Errorf("content:\n got  %q\n want %q", r.Content, tt.want)
			}
			renameChanges := 0
			for _, c := range r.Changes {
				if c.Rule == "variable-rename" {
					renameChanges++
				}
			}
			if renameChanges != tt.changes {
				t.Errorf("changes: got %d, want %d", renameChanges, tt.changes)
			}
		})
	}
}

func TestVariableRenameDisabledByDefault(t *testing.T) {
	input := `{{ grains.os }} {{ pillar.key }}`
	r := Migrate("test.sls", input, Options{RenameVars: false})
	if r.Content != input {
		t.Errorf("expected no changes without RenameVars, got %q", r.Content)
	}
}

func TestOutputPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"nginx/init.sls", "nginx/init.zy"},
		{"map.jinja", "map.jinja"},
		{"states/web/config.sls", "states/web/config.zy"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			r := Migrate(tt.input, "", Options{})
			if r.NewPath != tt.want {
				t.Errorf("NewPath: got %q, want %q", r.NewPath, tt.want)
			}
		})
	}
}

func TestMigrateMultipleRules(t *testing.T) {
	// Integration test: multiple rules fire on the same file.
	input := `{% set port = salt['pillar.get']('nginx:port', 80) %}
{% set pkgs = [] %}
{% do pkgs.append('nginx') %}
nginx_config:
  file.managed:
    - name: /etc/nginx/nginx.conf
    - mode: 0644
    - require:
      - pkg: nginx
{{ salt['mine.get']('web*', 'network.ip_addrs') }}`

	r := Migrate("nginx/init.sls", input, Options{})

	if r.NewPath != "nginx/init.zy" {
		t.Errorf("NewPath: got %q, want %q", r.NewPath, "nginx/init.zy")
	}

	// Verify salt function was converted.
	if !strings.Contains(r.Content, "pillar_get(") {
		t.Error("expected pillar_get() in output")
	}
	if strings.Contains(r.Content, "salt['pillar.get']") {
		t.Error("expected salt['pillar.get'] to be removed")
	}

	// Verify mlist conversion.
	if !strings.Contains(r.Content, "mlist()") {
		t.Error("expected mlist() in output")
	}
	if !strings.Contains(r.Content, ".Append(") {
		t.Error("expected .Append() in output")
	}

	// Verify octal mode quoting.
	if !strings.Contains(r.Content, `"0644"`) {
		t.Error("expected quoted 0644 in output")
	}

	// Check for expected warnings.
	hasUnknownSalt := false
	hasRequisite := false
	for _, w := range r.Warnings {
		if strings.Contains(w.Message, "mine.get") {
			hasUnknownSalt = true
		}
		if strings.Contains(w.Message, "requisite") {
			hasRequisite = true
		}
	}
	if !hasUnknownSalt {
		t.Error("expected warning for unknown salt['mine.get']")
	}
	if !hasRequisite {
		t.Error("expected warning for requisite block")
	}
}
