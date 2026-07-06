package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/template"
)

func TestLoadFile_BasicYAML(t *testing.T) {
	dir := t.TempDir()
	content := `
db_host: localhost
db_port: 5432
`
	writeFile(t, dir, "basic.zy", content)

	eng := newTestEngine(t, dir)
	result, err := LoadFile(eng, filepath.Join(dir, "basic.zy"), nil, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if result["db_host"] != "localhost" {
		t.Errorf("db_host = %v, want localhost", result["db_host"])
	}
	if result["db_port"] != 5432 {
		t.Errorf("db_port = %v, want 5432", result["db_port"])
	}
}

func TestLoadFile_WithTemplating(t *testing.T) {
	dir := t.TempDir()
	content := `
hostname: {{ facts.hostname }}
workers: {{ facts.cpu_count }}
`
	writeFile(t, dir, "tpl.zy", content)

	eng := newTestEngine(t, dir)
	facts := map[string]any{
		"hostname":  "web-01",
		"cpu_count": 4,
	}
	result, err := LoadFile(eng, filepath.Join(dir, "tpl.zy"), facts, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if result["hostname"] != "web-01" {
		t.Errorf("hostname = %v, want web-01", result["hostname"])
	}
	if result["workers"] != 4 {
		t.Errorf("workers = %v, want 4", result["workers"])
	}
}

func TestLoadFile_CrossReference(t *testing.T) {
	dir := t.TempDir()
	content := `
app_url: "https://{{ settings.domain }}:{{ settings.port }}"
`
	writeFile(t, dir, "cross.zy", content)

	eng := newTestEngine(t, dir)
	currentSettings := map[string]any{
		"domain": "example.com",
		"port":   443,
	}
	result, err := LoadFile(eng, filepath.Join(dir, "cross.zy"), nil, currentSettings)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if result["app_url"] != "https://example.com:443" {
		t.Errorf("app_url = %v, want https://example.com:443", result["app_url"])
	}
}

func TestMergeSettings_Basic(t *testing.T) {
	base := map[string]any{
		"a": 1,
		"b": "hello",
	}
	overlay := map[string]any{
		"b": "world",
		"c": true,
	}

	result := MergeSettings(base, overlay)

	if result["a"] != 1 {
		t.Errorf("a = %v, want 1", result["a"])
	}
	if result["b"] != "world" {
		t.Errorf("b = %v, want world", result["b"])
	}
	if result["c"] != true {
		t.Errorf("c = %v, want true", result["c"])
	}
}

func TestMergeSettings_DeepMerge(t *testing.T) {
	base := map[string]any{
		"db": map[string]any{
			"host": "localhost",
			"port": 5432,
		},
	}
	overlay := map[string]any{
		"db": map[string]any{
			"port":     5433,
			"database": "mydb",
		},
	}

	result := MergeSettings(base, overlay)
	db := result["db"].(map[string]any)

	if db["host"] != "localhost" {
		t.Errorf("db.host = %v, want localhost", db["host"])
	}
	if db["port"] != 5433 {
		t.Errorf("db.port = %v, want 5433", db["port"])
	}
	if db["database"] != "mydb" {
		t.Errorf("db.database = %v, want mydb", db["database"])
	}
}

func TestMergeSettings_OverlayReplacesNonMap(t *testing.T) {
	base := map[string]any{"val": "old"}
	overlay := map[string]any{"val": 42}

	result := MergeSettings(base, overlay)
	if result["val"] != 42 {
		t.Errorf("val = %v, want 42", result["val"])
	}
}

func TestMergeSettings_EmptyBase(t *testing.T) {
	overlay := map[string]any{"x": 1}
	result := MergeSettings(nil, overlay)
	if result["x"] != 1 {
		t.Errorf("x = %v, want 1", result["x"])
	}
}

func TestResolveRefToKVKey(t *testing.T) {
	tests := []struct {
		ref  string
		want string
	}{
		{"common.base", "common/base.zy"},
		{"webservers.nginx", "webservers/nginx.zy"},
		{"single", "single.zy"},
		{"a.b.c", "a/b/c.zy"},
	}
	for _, tt := range tests {
		got := resolveRefToKVKey(tt.ref)
		if got != tt.want {
			t.Errorf("resolveRefToKVKey(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func newTestEngine(t *testing.T, basePath string) *template.Engine {
	t.Helper()
	eng, err := template.NewEngine(template.EngineConfig{BasePath: basePath})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}
	return eng
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
