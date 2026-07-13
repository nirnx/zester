package starmod_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// TestLoader_StrictParams_UnknownKeyFails audits the Starlark validating-builder
// decode path under the strict flip (keystone spec §5). When the peel wires
// PolicyError (strict_params on) and a Starlark module DECLARED its parameters
// (PARAMS), an unknown key is a HARD build failure — the typo guard reaches
// custom modules too. Reserved directives and the loader-added "name" override
// never false-positive, and a proto-shaped decode failure that is NOT an unknown
// key (a missing required field) stays non-fatal, because the Starlark module is
// dynamically typed and reads the raw config itself.
func TestLoader_StrictParams_UnknownKeyFails(t *testing.T) {
	dir := t.TempDir()
	writeStarFile(t, filepath.Join(dir, "_modules"), "nginx.star", nginxWithDocsAndParams)

	loader := loaderWithOpts(t, dir, modschema.DecodeOptions{Unknown: modschema.PolicyError})
	registry := state.NewRegistry()
	if _, err := loader.LoadGlobal(registry); err != nil {
		t.Fatal(err)
	}

	// Unknown key → hard failure with the typed UnknownKeyError.
	_, err := registry.Build("nginx.configured", "web", map[string]any{
		"path":  "/etc/nginx/nginx.conf",
		"bogus": "value",
	})
	if err == nil {
		t.Fatal("strict build must fail on an unknown Starlark parameter")
	}
	var uke *modschema.UnknownKeyError
	if !errors.As(err, &uke) {
		t.Fatalf("want a modschema.UnknownKeyError, got %T: %v", err, err)
	}
	if uke.Key != "bogus" {
		t.Errorf("UnknownKeyError.Key = %q, want bogus", uke.Key)
	}

	// A reserved requisite + the "name" primary override + a known param must
	// build cleanly under strict (no false positive).
	if _, err := registry.Build("nginx.configured", "web", map[string]any{
		"name":    "/etc/nginx/nginx.conf",
		"path":    "/etc/nginx/nginx.conf",
		"require": []any{"pkg.installed:nginx"},
	}); err != nil {
		t.Fatalf("reserved/name keys must not fail a strict Starlark build: %v", err)
	}

	// A missing REQUIRED param (PARAMS marks path required) is non-fatal even
	// under strict: the module is dynamically typed, so a proto-shaped decode
	// miss that is not an unknown key must never block construction.
	if _, err := registry.Build("nginx.configured", "web", map[string]any{
		"require": []any{"pkg.installed:nginx"},
	}); err != nil {
		t.Fatalf("a missing required param must stay non-fatal for a dynamically-typed Starlark module: %v", err)
	}
}
