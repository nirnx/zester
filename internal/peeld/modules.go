package peeld

import (
	"fmt"
	"log/slog"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules"
)

// registerStateModules registers every built-in state module with injected
// execution providers and the peel's decode policy. The ordered registration
// table lives next to the modules themselves (pkg/state/modules.RegisterAll).
//
// Decode policy (Phase-1 activation, keystone spec §5): the unknown-parameter
// policy is PolicyWarn — a typo'd or unrecognized parameter on a migrated
// (Spec-carrying) module logs a warning through the peel's slog logger and the
// state STILL builds. It never errors; flipping Unknown to PolicyError is a
// deliberate later gate-close, not this activation.
//
// Known-not-parameter keys are never warned:
//   - Reserved = state.ReservedKeySet(): the fleet-wide reserved keys —
//     requisites (require/watch/onchanges/onfail), generic attributes
//     (onlyif/unless/order/retry/failhard/prereq), and compiler directives
//     (names/listen/*_in). A module never consumes these; the runner, compiler,
//     and attribute wrapper do.
//   - ExtraReserved = {"test"}: the exec-layer dry-run flag. An ad-hoc run
//     (`zester '*' pkg.removed foo test=True`) passes the whole args map —
//     including "test" — as the state config to the builder, and runExecStates
//     reads args["test"] directly (isTestArg). It is a known control key, not a
//     module parameter, so it must never be flagged as unknown.
func registerStateModules(registry *state.Registry, mctx *exec.ModuleContext, logger *slog.Logger) {
	modules.RegisterAll(registry, mctx, modschema.DecodeOptions{
		Unknown:       modschema.PolicyWarn,
		Reserved:      state.ReservedKeySet(),
		ExtraReserved: []string{"test"},
		Warnf: func(format string, args ...any) {
			logger.Warn(fmt.Sprintf(format, args...))
		},
	})
}
