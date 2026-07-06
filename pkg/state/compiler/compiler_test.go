package compiler_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/compiler"
	"github.com/nirnx/zester/pkg/template"
)

// testState is a simple state implementation for testing
type testState struct {
	name string
	reqs state.Requisites
}

func (s *testState) Name() string           { return s.name }
func (s *testState) Reqs() state.Requisites { return s.reqs }
func (s *testState) Check(ctx context.Context) (state.CheckResult, error) {
	return state.CheckResult{}, nil
}
func (s *testState) Apply(ctx context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{}, nil
}
func (s *testState) Revert(ctx context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{}, nil
}

// setupTestRegistry creates a registry with test builders
func setupTestRegistry() *state.Registry {
	registry := state.NewRegistry()

	// Register test builders
	modules := []string{"cmd.run", "pkg.installed", "file.managed", "test.ping"}
	for _, module := range modules {
		mod := module // capture for closure
		registry.Register(mod, func(id string, config map[string]any) (state.State, error) {
			reqs := state.ParseRequisites(config)
			return &testState{name: mod + ":" + id, reqs: reqs}, nil
		})
	}

	return registry
}

// setupTestCompiler creates a compiler with temp directory and test registry
func setupTestCompiler(t *testing.T) (*compiler.Compiler, string) {
	tmpDir := t.TempDir()
	registry := setupTestRegistry()
	engine, err := template.NewEngine(template.EngineConfig{BasePath: tmpDir})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	cfg := compiler.CompilerConfig{
		StatesDir: tmpDir,
		Engine:    engine,
		Registry:  registry,
		Facts:     map[string]any{"os": "linux"},
		Settings:  map[string]any{"env": "test"},
	}

	return compiler.NewCompiler(cfg), tmpDir
}

func TestStateRefResolve(t *testing.T) {
	ref := compiler.StateRef("webserver.config")
	path := ref.ResolveToPath("/states")
	expected := filepath.Join("/states", "webserver", "config.zy")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestStateRefResolveInit(t *testing.T) {
	ref := compiler.StateRef("webserver")
	path := ref.ResolveToPath("/states")
	expected := filepath.Join("/states", "webserver", "init.zy")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestCompileSimple(t *testing.T) {
	c, tmpDir := setupTestCompiler(t)

	// Create a simple state file
	err := os.MkdirAll(filepath.Join(tmpDir, "simple"), 0755)
	if err != nil {
		t.Fatal(err)
	}

	stateContent := `install-nginx:
  pkg.installed:
    - name: nginx
`
	err = os.WriteFile(filepath.Join(tmpDir, "simple", "init.zy"), []byte(stateContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile
	result, err := c.Compile(compiler.StateRef("simple"))
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	if len(result.States) != 1 {
		t.Fatalf("expected 1 state, got %d", len(result.States))
	}

	if result.States[0].Name() != "pkg.installed:install-nginx" {
		t.Errorf("expected state name pkg.installed:install-nginx, got %s", result.States[0].Name())
	}
}

func TestCompileInclude(t *testing.T) {
	c, tmpDir := setupTestCompiler(t)

	// Create base state
	err := os.MkdirAll(filepath.Join(tmpDir, "base"), 0755)
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

	// Create including state
	err = os.MkdirAll(filepath.Join(tmpDir, "web"), 0755)
	if err != nil {
		t.Fatal(err)
	}

	webContent := `include:
  - base

nginx-pkg:
  pkg.installed:
    - name: nginx
`
	err = os.WriteFile(filepath.Join(tmpDir, "web", "init.zy"), []byte(webContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile
	result, err := c.Compile(compiler.StateRef("web"))
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	if len(result.States) != 2 {
		t.Fatalf("expected 2 states, got %d", len(result.States))
	}

	// Check that both states are present
	names := make(map[string]bool)
	for _, s := range result.States {
		names[s.Name()] = true
	}

	if !names["pkg.installed:base-pkg"] || !names["pkg.installed:nginx-pkg"] {
		t.Errorf("expected both base-pkg and nginx-pkg states")
	}
}

func TestCompileIncludeChain(t *testing.T) {
	c, tmpDir := setupTestCompiler(t)

	// Create A
	err := os.MkdirAll(filepath.Join(tmpDir, "a"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	aContent := `state-a:
  test.ping: []
`
	err = os.WriteFile(filepath.Join(tmpDir, "a", "init.zy"), []byte(aContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create B (includes A)
	err = os.MkdirAll(filepath.Join(tmpDir, "b"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	bContent := `include:
  - a

state-b:
  test.ping: []
`
	err = os.WriteFile(filepath.Join(tmpDir, "b", "init.zy"), []byte(bContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create C (includes B)
	err = os.MkdirAll(filepath.Join(tmpDir, "c"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	cContent := `include:
  - b

state-c:
  test.ping: []
`
	err = os.WriteFile(filepath.Join(tmpDir, "c", "init.zy"), []byte(cContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile C (should include B and A)
	result, err := c.Compile(compiler.StateRef("c"))
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	if len(result.States) != 3 {
		t.Fatalf("expected 3 states, got %d", len(result.States))
	}

	// Check all states present
	names := make(map[string]bool)
	for _, s := range result.States {
		names[s.Name()] = true
	}

	for _, expected := range []string{"test.ping:state-a", "test.ping:state-b", "test.ping:state-c"} {
		if !names[expected] {
			t.Errorf("expected state %s", expected)
		}
	}
}

func TestCompileDiamondInclude(t *testing.T) {
	c, tmpDir := setupTestCompiler(t)

	// Create D (base)
	err := os.MkdirAll(filepath.Join(tmpDir, "d"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	dContent := `state-d:
  test.ping: []
`
	err = os.WriteFile(filepath.Join(tmpDir, "d", "init.zy"), []byte(dContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create B (includes D)
	err = os.MkdirAll(filepath.Join(tmpDir, "b"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	bContent := `include:
  - d

state-b:
  test.ping: []
`
	err = os.WriteFile(filepath.Join(tmpDir, "b", "init.zy"), []byte(bContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create C (includes D)
	err = os.MkdirAll(filepath.Join(tmpDir, "c"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	cContent := `include:
  - d

state-c:
  test.ping: []
`
	err = os.WriteFile(filepath.Join(tmpDir, "c", "init.zy"), []byte(cContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create A (includes B and C, both of which include D)
	err = os.MkdirAll(filepath.Join(tmpDir, "a"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	aContent := `include:
  - b
  - c

state-a:
  test.ping: []
`
	err = os.WriteFile(filepath.Join(tmpDir, "a", "init.zy"), []byte(aContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile A
	result, err := c.Compile(compiler.StateRef("a"))
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	// Should have 4 states (D should only be included once)
	if len(result.States) != 4 {
		t.Fatalf("expected 4 states, got %d", len(result.States))
	}

	// Check all states present
	names := make(map[string]bool)
	for _, s := range result.States {
		names[s.Name()] = true
	}

	for _, expected := range []string{"test.ping:state-a", "test.ping:state-b", "test.ping:state-c", "test.ping:state-d"} {
		if !names[expected] {
			t.Errorf("expected state %s", expected)
		}
	}
}

func TestCompileCycleDetection(t *testing.T) {
	c, tmpDir := setupTestCompiler(t)

	// Create A (includes B)
	err := os.MkdirAll(filepath.Join(tmpDir, "a"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	aContent := `include:
  - b

state-a:
  test.ping: []
`
	err = os.WriteFile(filepath.Join(tmpDir, "a", "init.zy"), []byte(aContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create B (includes A - creates cycle)
	err = os.MkdirAll(filepath.Join(tmpDir, "b"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	bContent := `include:
  - a

state-b:
  test.ping: []
`
	err = os.WriteFile(filepath.Join(tmpDir, "b", "init.zy"), []byte(bContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile should fail with cycle error
	_, err = c.Compile(compiler.StateRef("a"))
	if err == nil {
		t.Fatal("expected cycle detection error")
	}

	// Error message should mention cycle
	if !containsSubstring(err.Error(), "cycle") {
		t.Errorf("expected cycle error, got: %v", err)
	}
}

func TestCompileExtendOverride(t *testing.T) {
	c, tmpDir := setupTestCompiler(t)

	// Create base state
	err := os.MkdirAll(filepath.Join(tmpDir, "base"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	baseContent := `nginx-config:
  file.managed:
    - source: /etc/nginx/nginx.conf.base
    - mode: "0644"
`
	err = os.WriteFile(filepath.Join(tmpDir, "base", "init.zy"), []byte(baseContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create extending state
	err = os.MkdirAll(filepath.Join(tmpDir, "prod"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	prodContent := `include:
  - base

extend:
  nginx-config:
    file.managed:
      - source: /etc/nginx/nginx.conf.prod
      - mode: "0600"
`
	err = os.WriteFile(filepath.Join(tmpDir, "prod", "init.zy"), []byte(prodContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile
	result, err := c.Compile(compiler.StateRef("prod"))
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	if len(result.States) != 1 {
		t.Fatalf("expected 1 state, got %d", len(result.States))
	}

	// Extended values should be present (we can't easily check the actual values
	// with our test state, but we verified the state was built successfully)
}

func TestCompileExtendAppendRequisites(t *testing.T) {
	c, tmpDir := setupTestCompiler(t)

	// Create base state with one requisite
	err := os.MkdirAll(filepath.Join(tmpDir, "base"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	baseContent := `install-nginx:
  pkg.installed:
    - name: nginx

nginx-service:
  cmd.run:
    - name: systemctl start nginx
    - require:
      - pkg.installed:install-nginx
`
	err = os.WriteFile(filepath.Join(tmpDir, "base", "init.zy"), []byte(baseContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create extending state that adds more requisites
	err = os.MkdirAll(filepath.Join(tmpDir, "extended"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	extContent := `include:
  - base

nginx-config:
  file.managed:
    - name: /etc/nginx/nginx.conf

extend:
  nginx-service:
    cmd.run:
      - require:
        - file.managed:nginx-config
      - watch:
        - file.managed:nginx-config
`
	err = os.WriteFile(filepath.Join(tmpDir, "extended", "init.zy"), []byte(extContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile
	result, err := c.Compile(compiler.StateRef("extended"))
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	// Find nginx-service state
	var nginxService state.State
	for _, s := range result.States {
		if s.Name() == "cmd.run:nginx-service" {
			nginxService = s
			break
		}
	}

	if nginxService == nil {
		t.Fatal("nginx-service state not found")
	}

	reqs := nginxService.Reqs()

	// Should have both requires (original + extended)
	if len(reqs.Require) != 2 {
		t.Errorf("expected 2 requires, got %d: %v", len(reqs.Require), reqs.Require)
	}

	// Should have watch from extend
	if len(reqs.Watch) != 1 {
		t.Errorf("expected 1 watch, got %d: %v", len(reqs.Watch), reqs.Watch)
	}
}

func TestCompileExtendUnknownState(t *testing.T) {
	c, tmpDir := setupTestCompiler(t)

	// Create state without the extended state
	err := os.MkdirAll(filepath.Join(tmpDir, "base"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	baseContent := `install-nginx:
  pkg.installed:
    - name: nginx
`
	err = os.WriteFile(filepath.Join(tmpDir, "base", "init.zy"), []byte(baseContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create extending state that references non-existent state
	err = os.MkdirAll(filepath.Join(tmpDir, "bad"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	badContent := `include:
  - base

extend:
  nonexistent-state:
    pkg.installed:
      - name: does-not-exist
`
	err = os.WriteFile(filepath.Join(tmpDir, "bad", "init.zy"), []byte(badContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile should fail
	_, err = c.Compile(compiler.StateRef("bad"))
	if err == nil {
		t.Fatal("expected error for extending unknown state")
	}

	if !containsSubstring(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestCompileMultiple(t *testing.T) {
	c, tmpDir := setupTestCompiler(t)

	// Create state A
	err := os.MkdirAll(filepath.Join(tmpDir, "a"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	aContent := `state-a:
  test.ping: []
`
	err = os.WriteFile(filepath.Join(tmpDir, "a", "init.zy"), []byte(aContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create state B
	err = os.MkdirAll(filepath.Join(tmpDir, "b"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	bContent := `state-b:
  test.ping: []
`
	err = os.WriteFile(filepath.Join(tmpDir, "b", "init.zy"), []byte(bContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile multiple
	result, err := c.CompileMultiple([]compiler.StateRef{"a", "b"})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	if len(result.States) != 2 {
		t.Fatalf("expected 2 states, got %d", len(result.States))
	}

	// Check states present
	names := make(map[string]bool)
	for _, s := range result.States {
		names[s.Name()] = true
	}

	if !names["test.ping:state-a"] || !names["test.ping:state-b"] {
		t.Errorf("expected both state-a and state-b")
	}
}

func TestCompileStateIDConflict_DifferentModules(t *testing.T) {
	c, tmpDir := setupTestCompiler(t)

	// Create state A with state-x using test.ping
	err := os.MkdirAll(filepath.Join(tmpDir, "a"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	aContent := `state-x:
  test.ping: []
`
	err = os.WriteFile(filepath.Join(tmpDir, "a", "init.zy"), []byte(aContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create state B with same state-x ID but different module (cmd.run)
	err = os.MkdirAll(filepath.Join(tmpDir, "b"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	bContent := `state-x:
  cmd.run:
    - command: echo "from b"
`
	err = os.WriteFile(filepath.Join(tmpDir, "b", "init.zy"), []byte(bContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile both — merge produces 2 states (one per module under state-x)
	result, err := c.CompileMultiple([]compiler.StateRef{"a", "b"})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	if len(result.States) != 2 {
		t.Fatalf("expected 2 states (multi-module merge), got %d", len(result.States))
	}

	names := make(map[string]bool)
	for _, s := range result.States {
		names[s.Name()] = true
	}
	if !names["test.ping:state-x"] {
		t.Error("expected test.ping:state-x from file A")
	}
	if !names["cmd.run:state-x"] {
		t.Error("expected cmd.run:state-x from file B")
	}
}

func TestCompileStateIDConflict_SameModuleMerge(t *testing.T) {
	c, tmpDir := setupTestCompiler(t)

	// Create state A with install-nginx using pkg.installed
	err := os.MkdirAll(filepath.Join(tmpDir, "a"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	aContent := `install-nginx:
  pkg.installed:
    - name: nginx
    - require:
      - "cmd.run:setup-repo"
`
	err = os.WriteFile(filepath.Join(tmpDir, "a", "init.zy"), []byte(aContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create state B with same install-nginx ID + same module, adding more config
	err = os.MkdirAll(filepath.Join(tmpDir, "b"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	bContent := `install-nginx:
  pkg.installed:
    - refresh: true
    - require:
      - "file.managed:repo-config"
`
	err = os.WriteFile(filepath.Join(tmpDir, "b", "init.zy"), []byte(bContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Compile both — same module merges args (requisites appended, others replaced)
	result, err := c.CompileMultiple([]compiler.StateRef{"a", "b"})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	if len(result.States) != 1 {
		t.Fatalf("expected 1 state (same-module merge), got %d", len(result.States))
	}

	s := result.States[0]
	if s.Name() != "pkg.installed:install-nginx" {
		t.Errorf("expected pkg.installed:install-nginx, got %s", s.Name())
	}

	// Requisites should be appended from both files
	reqs := s.Reqs()
	if len(reqs.Require) != 2 {
		t.Errorf("expected 2 requires (appended from both files), got %d: %v", len(reqs.Require), reqs.Require)
	}
}

// Helper function
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
