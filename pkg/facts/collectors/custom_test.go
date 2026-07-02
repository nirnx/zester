package collectors_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/facts/collectors"
)

func writeFactsFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "facts")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write facts file: %v", err)
	}
	return path
}

func TestCustom_Metadata(t *testing.T) {
	c := collectors.Custom{}
	if c.Name() != "custom" {
		t.Errorf("Name() = %q, want %q", c.Name(), "custom")
	}
	if c.Interval() != 30*time.Second {
		t.Errorf("Interval() = %v, want 30s", c.Interval())
	}
	if !c.MergeAtRoot() {
		t.Error("MergeAtRoot() = false, want true")
	}
}

func TestCustom_ValidYAML(t *testing.T) {
	path := writeFactsFile(t, `
datacenter: us-east-1
tier: production
roles:
  - webserver
  - proxy
labels:
  env: production
  region:
    zone: a
`)

	facts, err := collectors.Custom{Path: path}.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if facts["datacenter"] != "us-east-1" {
		t.Errorf("datacenter = %v, want us-east-1", facts["datacenter"])
	}
	if facts["tier"] != "production" {
		t.Errorf("tier = %v, want production", facts["tier"])
	}

	roles, ok := facts["roles"].([]any)
	if !ok {
		t.Fatalf("roles = %T, want []any", facts["roles"])
	}
	if len(roles) != 2 || roles[0] != "webserver" || roles[1] != "proxy" {
		t.Errorf("roles = %v, want [webserver proxy]", roles)
	}

	// Nested keys must be preserved as nested maps.
	labels, ok := facts["labels"].(map[string]any)
	if !ok {
		t.Fatalf("labels = %T, want map[string]any", facts["labels"])
	}
	if labels["env"] != "production" {
		t.Errorf("labels.env = %v, want production", labels["env"])
	}
	region, ok := labels["region"].(map[string]any)
	if !ok {
		t.Fatalf("labels.region = %T, want map[string]any", labels["region"])
	}
	if region["zone"] != "a" {
		t.Errorf("labels.region.zone = %v, want a", region["zone"])
	}
}

func TestCustom_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist")

	facts, err := collectors.Custom{Path: path}.Collect(context.Background())
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if facts == nil {
		t.Fatal("expected non-nil facts map")
	}
	if len(facts) != 0 {
		t.Errorf("expected empty facts, got %v", facts)
	}
}

func TestCustom_EmptyFile(t *testing.T) {
	path := writeFactsFile(t, "")

	facts, err := collectors.Custom{Path: path}.Collect(context.Background())
	if err != nil {
		t.Fatalf("expected no error for empty file, got %v", err)
	}
	if facts == nil || len(facts) != 0 {
		t.Errorf("expected empty non-nil facts, got %v", facts)
	}
}

func TestCustom_NullYAML(t *testing.T) {
	// "null" unmarshals to a nil map; the collector must normalize it.
	path := writeFactsFile(t, "null\n")

	facts, err := collectors.Custom{Path: path}.Collect(context.Background())
	if err != nil {
		t.Fatalf("expected no error for null document, got %v", err)
	}
	if facts == nil || len(facts) != 0 {
		t.Errorf("expected empty non-nil facts, got %v", facts)
	}
}

func TestCustom_MalformedYAML(t *testing.T) {
	path := writeFactsFile(t, "key: [unclosed\n")

	_, err := collectors.Custom{Path: path}.Collect(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
	if !strings.Contains(err.Error(), "parse custom facts") {
		t.Errorf("expected wrapped parse error, got %v", err)
	}
}

func TestCustom_NonMapYAML(t *testing.T) {
	path := writeFactsFile(t, "just a scalar string\n")

	_, err := collectors.Custom{Path: path}.Collect(context.Background())
	if err == nil {
		t.Fatal("expected error for non-map YAML document")
	}
	if !strings.Contains(err.Error(), "parse custom facts") {
		t.Errorf("expected wrapped parse error, got %v", err)
	}
}

func TestCustom_UnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission bits are not enforced")
	}
	path := writeFactsFile(t, "role: webserver\n")
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	_, err := collectors.Custom{Path: path}.Collect(context.Background())
	if err == nil {
		t.Fatal("expected error for unreadable file")
	}
	if !strings.Contains(err.Error(), "read custom facts") {
		t.Errorf("expected wrapped read error, got %v", err)
	}
}
