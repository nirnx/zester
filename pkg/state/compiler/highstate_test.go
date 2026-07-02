package compiler_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ptorbus/zester/pkg/state/compiler"
	"github.com/ptorbus/zester/pkg/template"
)

func TestHighstateCompile(t *testing.T) {
	c, tmpDir := setupTestCompiler(t)

	// Create top file
	topContent := `base:
  '*':
    - common
  'web*':
    - webserver
`
	err := os.WriteFile(filepath.Join(tmpDir, "top.zy"), []byte(topContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create common state
	err = os.MkdirAll(filepath.Join(tmpDir, "common"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	commonContent := `base-pkg:
  pkg.installed:
    - name: base-package
`
	err = os.WriteFile(filepath.Join(tmpDir, "common", "init.zy"), []byte(commonContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create webserver state
	err = os.MkdirAll(filepath.Join(tmpDir, "webserver"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	webContent := `nginx-pkg:
  pkg.installed:
    - name: nginx
`
	err = os.WriteFile(filepath.Join(tmpDir, "webserver", "init.zy"), []byte(webContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile highstate for web-01 (matches both * and web*)
	result, err := c.Highstate("web-01")
	if err != nil {
		t.Fatalf("highstate failed: %v", err)
	}

	// Should have both common and webserver states
	if len(result.States) != 2 {
		t.Fatalf("expected 2 states, got %d", len(result.States))
	}

	names := make(map[string]bool)
	for _, s := range result.States {
		names[s.Name()] = true
	}

	if !names["pkg.installed:base-pkg"] {
		t.Error("expected base-pkg state from common")
	}
	if !names["pkg.installed:nginx-pkg"] {
		t.Error("expected nginx-pkg state from webserver")
	}
}

func TestHighstateNoMatch(t *testing.T) {
	c, tmpDir := setupTestCompiler(t)

	// Create top file with specific patterns
	topContent := `base:
  'web*':
    - webserver
  'db*':
    - database
`
	err := os.WriteFile(filepath.Join(tmpDir, "top.zy"), []byte(topContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create webserver state (even though it won't match)
	err = os.MkdirAll(filepath.Join(tmpDir, "webserver"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	webContent := `nginx-pkg:
  pkg.installed:
    - name: nginx
`
	err = os.WriteFile(filepath.Join(tmpDir, "webserver", "init.zy"), []byte(webContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile highstate for app-01 (doesn't match any pattern)
	result, err := c.Highstate("app-01")
	if err != nil {
		t.Fatalf("highstate failed: %v", err)
	}

	// Should have empty result (not an error)
	if len(result.States) != 0 {
		t.Fatalf("expected 0 states for no match, got %d", len(result.States))
	}
}

func TestHighstateWithTemplates(t *testing.T) {
	tmpDir := t.TempDir()
	registry := setupTestRegistry()
	engine, err := template.NewEngine(template.EngineConfig{BasePath: tmpDir})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	facts := map[string]any{
		"role":        "webserver",
		"environment": "production",
	}
	settings := map[string]any{
		"domain": "example.com",
	}

	cfg := compiler.CompilerConfig{
		StatesDir: tmpDir,
		Engine:    engine,
		Registry:  registry,
		Facts:     facts,
		Settings:  settings,
	}

	c := compiler.NewCompiler(cfg)

	// Create top file with template
	topContent := `base:
  '*':
    - common
  'role:{{ facts.role }}':
    - {{ facts.role }}
`
	err = os.WriteFile(filepath.Join(tmpDir, "top.zy"), []byte(topContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create common state
	err = os.MkdirAll(filepath.Join(tmpDir, "common"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	commonContent := `common-pkg:
  pkg.installed:
    - name: common
`
	err = os.WriteFile(filepath.Join(tmpDir, "common", "init.zy"), []byte(commonContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create webserver state with template
	err = os.MkdirAll(filepath.Join(tmpDir, "webserver"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	webContent := `nginx-config:
  file.managed:
    - name: /etc/nginx/sites-enabled/{{ settings.domain }}
`
	err = os.WriteFile(filepath.Join(tmpDir, "webserver", "init.zy"), []byte(webContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile highstate
	result, err := c.Highstate("peel-01")
	if err != nil {
		t.Fatalf("highstate failed: %v", err)
	}

	// Should have both states
	if len(result.States) != 2 {
		t.Fatalf("expected 2 states, got %d", len(result.States))
	}

	names := make(map[string]bool)
	for _, s := range result.States {
		names[s.Name()] = true
	}

	if !names["pkg.installed:common-pkg"] {
		t.Error("expected common-pkg state")
	}
	if !names["file.managed:nginx-config"] {
		t.Error("expected nginx-config state")
	}
}

func TestHighstateWithIncludes(t *testing.T) {
	c, tmpDir := setupTestCompiler(t)

	// Create top file
	topContent := `base:
  '*':
    - app
`
	err := os.WriteFile(filepath.Join(tmpDir, "top.zy"), []byte(topContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create base state
	err = os.MkdirAll(filepath.Join(tmpDir, "base"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	baseContent := `base-pkg:
  pkg.installed:
    - name: base
`
	err = os.WriteFile(filepath.Join(tmpDir, "base", "init.zy"), []byte(baseContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create app state that includes base
	err = os.MkdirAll(filepath.Join(tmpDir, "app"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	appContent := `include:
  - base

app-pkg:
  pkg.installed:
    - name: app
`
	err = os.WriteFile(filepath.Join(tmpDir, "app", "init.zy"), []byte(appContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile highstate
	result, err := c.Highstate("peel-01")
	if err != nil {
		t.Fatalf("highstate failed: %v", err)
	}

	// Should have both base and app states (via include)
	if len(result.States) != 2 {
		t.Fatalf("expected 2 states, got %d", len(result.States))
	}

	names := make(map[string]bool)
	for _, s := range result.States {
		names[s.Name()] = true
	}

	if !names["pkg.installed:base-pkg"] {
		t.Error("expected base-pkg state (via include)")
	}
	if !names["pkg.installed:app-pkg"] {
		t.Error("expected app-pkg state")
	}
}
