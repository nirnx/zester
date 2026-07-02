package masterapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAPIArtifactsEmbedded(t *testing.T) {
	if len(openapiYAML) == 0 {
		t.Fatal("embedded openapi.yaml is empty")
	}
	if len(openapiJSON) == 0 {
		t.Fatal("embedded openapi.json is empty")
	}
	if len(swaggerHTML) == 0 {
		t.Fatal("embedded swagger-ui html is empty")
	}

	if !strings.Contains(string(openapiYAML), "/jobs") {
		t.Fatal("openapi.yaml missing /jobs path")
	}

	var spec map[string]any
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	if _, ok := spec["paths"]; !ok {
		t.Fatal("openapi.json missing paths")
	}
}
