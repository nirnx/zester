package schematest_test

// Architecture guards (keystone spec §1):
//
//   - schematest's own direct zester imports are pinned to
//     {modschema, paramtypes, pkg/bus, pkg/cliargs} (yaml.v3 is external).
//   - NO production (non-test) .go file anywhere in the module imports schematest.
//
// The first uses `go list`; the second parses import blocks so a stray production
// import is caught even though schematest is otherwise only reached from tests.

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	schematestPkg = "github.com/nirnx/zester/pkg/modschema/schematest"
	zesterPrefix  = "github.com/nirnx/zester/"
)

func TestArchSchematestDirectImports(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	out, err := exec.Command("go", "list", "-f", "{{range .Imports}}{{.}}\n{{end}}", schematestPkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", schematestPkg, err, out)
	}
	allowed := map[string]bool{
		"github.com/nirnx/zester/pkg/modschema":            true,
		"github.com/nirnx/zester/pkg/modschema/paramtypes": true,
		"github.com/nirnx/zester/pkg/bus":                  true,
		"github.com/nirnx/zester/pkg/cliargs":              true,
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		dep := strings.TrimSpace(line)
		if !strings.HasPrefix(dep, zesterPrefix) {
			continue // external (yaml.v3, stdlib) — unrestricted for the harness
		}
		if !allowed[dep] {
			t.Errorf("schematest must not import %s (allowed: modschema, paramtypes, bus, cliargs)", dep)
		}
	}
}

// TestArchSchematestExternalImports pins schematest's external (non-zester,
// non-stdlib) dependency surface: production code may import ONLY yaml.v3, and
// _test files may additionally import the santhosh-tekuri/jsonschema test dep
// (§1 / §9.4). Any other external module — a stray convenience library — fails
// here, keeping the harness's dependency footprint auditable.
func TestArchSchematestExternalImports(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	const format = `{{range .Imports}}prod {{.}}
{{end}}{{range .TestImports}}test {{.}}
{{end}}{{range .XTestImports}}test {{.}}
{{end}}`
	out, err := exec.Command("go", "list", "-f", format, schematestPkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", schematestPkg, err, out)
	}
	const yamlDep = "gopkg.in/yaml.v3"
	const jsonschemaDep = "github.com/santhosh-tekuri/jsonschema/v6"
	prodAllowed := map[string]bool{yamlDep: true}
	testAllowed := map[string]bool{yamlDep: true, jsonschemaDep: true}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		scope, dep := fields[0], fields[1]
		if strings.HasPrefix(dep, zesterPrefix) {
			continue // intra-module: covered by the direct-imports test
		}
		if !isExternalModule(dep) {
			continue // standard library — unrestricted
		}
		allowed, allowList := prodAllowed, "yaml.v3"
		if scope == "test" {
			allowed, allowList = testAllowed, "yaml.v3, jsonschema"
		}
		if !allowed[dep] {
			t.Errorf("schematest (%s) must not import external %s (allowed: %s)", scope, dep, allowList)
		}
	}
}

// isExternalModule reports whether an import path belongs to an external module
// (its first path segment is a domain, i.e. contains a dot) rather than the
// standard library (whose first segment never does).
func isExternalModule(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return strings.Contains(first, ".")
}

func TestArchNoProductionImportsSchematest(t *testing.T) {
	root := repoRoot(t)
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "site": true, "website": true,
		"vendor": true, "testdata": true,
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return nil // unparsable file is not our concern here
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == schematestPkg {
				t.Errorf("production file %s imports schematest (test-support only)", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// wd == <root>/pkg/modschema/schematest
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}
