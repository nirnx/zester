package moduledoc_test

// Architecture import test (keystone spec §1): pkg/moduledoc is a leaf — its
// own (non-test) code imports modschema only, never pkg/state, pkg/execmod,
// or internal/*. `go list -deps` (without -test) never pulls in _test.go
// imports, so this pins the PRODUCTION import graph even though this very
// test file imports pkg/state/modules (for TestDocdataMatchesLive).

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePrefix = "github.com/nirnx/zester/"

func TestArchModuledocImports(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}",
		"github.com/nirnx/zester/pkg/moduledoc").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	allowed := map[string]bool{
		"github.com/nirnx/zester/pkg/moduledoc":            true,
		"github.com/nirnx/zester/pkg/modschema":            true,
		"github.com/nirnx/zester/pkg/modschema/paramtypes": true,
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		dep := strings.TrimSpace(line)
		if !strings.HasPrefix(dep, modulePrefix) {
			continue
		}
		if !allowed[dep] {
			t.Errorf("pkg/moduledoc must not import %s (allowed: modschema + paramtypes only)", dep)
		}
	}
}
