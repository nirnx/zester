package modschema_test

// Architecture import test (keystone spec §1): pins the "never imports" walls via
// `go list -deps`.
//
//	modschema  → stdlib + pkg/modschema/paramtypes ONLY (never pkg/state, internal/*)
//	paramtypes → stdlib ONLY (never modschema, never any other zester package)

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePrefix = "github.com/nirnx/zester/"

func moduleDeps(t *testing.T, pkg string) []string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", pkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", pkg, err, out)
	}
	var deps []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, modulePrefix) {
			deps = append(deps, line)
		}
	}
	return deps
}

func TestArchModschemaImports(t *testing.T) {
	allowed := map[string]bool{
		"github.com/nirnx/zester/pkg/modschema":            true,
		"github.com/nirnx/zester/pkg/modschema/paramtypes": true,
	}
	for _, dep := range moduleDeps(t, "github.com/nirnx/zester/pkg/modschema") {
		if !allowed[dep] {
			t.Errorf("modschema must not import %s (allowed: stdlib + paramtypes only)", dep)
		}
	}
}

func TestArchParamtypesImports(t *testing.T) {
	allowed := map[string]bool{
		"github.com/nirnx/zester/pkg/modschema/paramtypes": true,
	}
	for _, dep := range moduleDeps(t, "github.com/nirnx/zester/pkg/modschema/paramtypes") {
		if !allowed[dep] {
			t.Errorf("paramtypes must not import %s (allowed: stdlib only)", dep)
		}
	}
}
