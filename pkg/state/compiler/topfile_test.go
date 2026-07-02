package compiler_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ptorbus/zester/pkg/settings"
	"github.com/ptorbus/zester/pkg/state/compiler"
	"github.com/ptorbus/zester/pkg/template"
)

func TestParseStateTopFile(t *testing.T) {
	topContent := `base:
  '*':
    - common
  'web*':
    - webserver
`
	top, err := compiler.ParseStateTopFile([]byte(topContent))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if len(top.Environments) != 1 {
		t.Fatalf("expected 1 environment, got %d", len(top.Environments))
	}

	env := top.Environments[0]
	if env.Name != "base" {
		t.Errorf("expected env name 'base', got %s", env.Name)
	}

	if len(env.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(env.Entries))
	}

	// Check first entry
	if env.Entries[0].Pattern != "*" {
		t.Errorf("expected pattern '*', got %s", env.Entries[0].Pattern)
	}
	if len(env.Entries[0].StateRefs) != 1 || env.Entries[0].StateRefs[0] != "common" {
		t.Errorf("expected state ref 'common', got %v", env.Entries[0].StateRefs)
	}

	// Check second entry
	if env.Entries[1].Pattern != "web*" {
		t.Errorf("expected pattern 'web*', got %s", env.Entries[1].Pattern)
	}
	if len(env.Entries[1].StateRefs) != 1 || env.Entries[1].StateRefs[0] != "webserver" {
		t.Errorf("expected state ref 'webserver', got %v", env.Entries[1].StateRefs)
	}
}

func TestParseStateTopFileMultiEnv(t *testing.T) {
	topContent := `base:
  '*':
    - common

prod:
  'web*':
    - webserver
`
	top, err := compiler.ParseStateTopFile([]byte(topContent))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if len(top.Environments) != 2 {
		t.Fatalf("expected 2 environments, got %d", len(top.Environments))
	}

	// Check order is preserved
	if top.Environments[0].Name != "base" {
		t.Errorf("expected first env 'base', got %s", top.Environments[0].Name)
	}
	if top.Environments[1].Name != "prod" {
		t.Errorf("expected second env 'prod', got %s", top.Environments[1].Name)
	}
}

func TestResolveForPeelWildcard(t *testing.T) {
	topContent := `base:
  '*':
    - common
    - base
`
	top, err := compiler.ParseStateTopFile([]byte(topContent))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	matcher := &settings.SimpleTargetMatcher{}
	refs := top.ResolveForPeel("any-peel-id", map[string]any{}, matcher)

	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d: %v", len(refs), refs)
	}

	if refs[0] != "common" || refs[1] != "base" {
		t.Errorf("expected ['common', 'base'], got %v", refs)
	}
}

func TestResolveForPeelGlob(t *testing.T) {
	topContent := `base:
  'web*':
    - webserver
  'db*':
    - database
`
	top, err := compiler.ParseStateTopFile([]byte(topContent))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	matcher := &settings.SimpleTargetMatcher{}

	// Test web* match
	refs := top.ResolveForPeel("web-01", map[string]any{}, matcher)
	if len(refs) != 1 || refs[0] != "webserver" {
		t.Errorf("expected ['webserver'] for web-01, got %v", refs)
	}

	// Test db* match
	refs = top.ResolveForPeel("db-master", map[string]any{}, matcher)
	if len(refs) != 1 || refs[0] != "database" {
		t.Errorf("expected ['database'] for db-master, got %v", refs)
	}

	// Test no match
	refs = top.ResolveForPeel("app-01", map[string]any{}, matcher)
	if len(refs) != 0 {
		t.Errorf("expected no refs for app-01, got %v", refs)
	}
}

func TestResolveForPeelFact(t *testing.T) {
	topContent := `base:
  'role:webserver':
    - webserver
  'role:database':
    - database
`
	top, err := compiler.ParseStateTopFile([]byte(topContent))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	matcher := &settings.SimpleTargetMatcher{}

	// Test role:webserver match
	facts := map[string]any{"role": "webserver"}
	refs := top.ResolveForPeel("peel-01", facts, matcher)
	if len(refs) != 1 || refs[0] != "webserver" {
		t.Errorf("expected ['webserver'] for role:webserver, got %v", refs)
	}

	// Test role:database match
	facts = map[string]any{"role": "database"}
	refs = top.ResolveForPeel("peel-02", facts, matcher)
	if len(refs) != 1 || refs[0] != "database" {
		t.Errorf("expected ['database'] for role:database, got %v", refs)
	}

	// Test no match
	facts = map[string]any{"role": "other"}
	refs = top.ResolveForPeel("peel-03", facts, matcher)
	if len(refs) != 0 {
		t.Errorf("expected no refs for role:other, got %v", refs)
	}
}

func TestResolveForPeelDedup(t *testing.T) {
	topContent := `base:
  '*':
    - common
    - security
  'web*':
    - common
    - webserver
`
	top, err := compiler.ParseStateTopFile([]byte(topContent))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	matcher := &settings.SimpleTargetMatcher{}

	// web-01 matches both * and web*, so common appears twice in pattern matches
	// but should only appear once in result
	refs := top.ResolveForPeel("web-01", map[string]any{}, matcher)

	// Should have: common, security, webserver (common deduplicated)
	if len(refs) != 3 {
		t.Fatalf("expected 3 refs, got %d: %v", len(refs), refs)
	}

	// Check deduplication - common should appear only once (first occurrence preserved)
	if refs[0] != "common" || refs[1] != "security" || refs[2] != "webserver" {
		t.Errorf("expected ['common', 'security', 'webserver'], got %v", refs)
	}
}

func TestLoadStateTopFile(t *testing.T) {
	tmpDir := t.TempDir()
	engine, err := template.NewEngine(template.EngineConfig{BasePath: tmpDir})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	// Create top file with template variable
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

	facts := map[string]any{"role": "webserver"}
	settings := map[string]any{}

	top, err := compiler.LoadStateTopFile(tmpDir, engine, facts, settings)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// Template should have been rendered
	if len(top.Environments) != 1 {
		t.Fatalf("expected 1 environment, got %d", len(top.Environments))
	}

	// Check that template was rendered correctly
	found := false
	for _, entry := range top.Environments[0].Entries {
		if entry.Pattern == "role:webserver" {
			found = true
			if len(entry.StateRefs) != 1 || entry.StateRefs[0] != "webserver" {
				t.Errorf("expected state ref 'webserver', got %v", entry.StateRefs)
			}
		}
	}

	if !found {
		t.Error("expected to find 'role:webserver' pattern after template rendering")
	}
}
