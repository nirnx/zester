package execmod_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestArchExecmodNeverImportsState pins the seam that makes sys.doc's DocSource
// necessary (keystone spec §1/§7): execmod must never import pkg/state. The
// dispatch-precedence lookup that spans the state registry lives behind the
// execmod.DocSource interface, implemented by the peel, precisely so this wall
// holds. Without it, sys.doc could reach into pkg/state directly and the seam
// would rot.
func TestArchExecmodNeverImportsState(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}",
		"github.com/nirnx/zester/pkg/execmod").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps execmod: %v\n%s", err, out)
	}
	const stateP = "github.com/nirnx/zester/pkg/state"
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		dep := strings.TrimSpace(line)
		if dep == stateP || strings.HasPrefix(dep, stateP+"/") {
			t.Errorf("execmod must not import %s (the DocSource seam exists to avoid this)", dep)
		}
	}
}
