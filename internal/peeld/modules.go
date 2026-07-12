package peeld

import (
	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules"
)

// registerStateModules registers every built-in state module with injected
// execution providers. The ordered registration table lives next to the modules
// themselves (pkg/state/modules.RegisterAll).
//
// Zero DecodeOptions is passed for now: PolicyIgnore, no reserved keys — the
// migrated modules decode identically to their legacy behavior. Wiring the
// peel's reserved-key set and flipping the unknown-key policy to PolicyWarn (and
// later PolicyError) is a deliberate later activation, not part of the relocation.
func registerStateModules(registry *state.Registry, mctx *exec.ModuleContext) {
	modules.RegisterAll(registry, mctx, modschema.DecodeOptions{})
}
