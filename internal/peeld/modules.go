package peeld

import (
	"fmt"
	"log/slog"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules"
)

// decodeOptions builds the peel's module-decode policy from the strict_params
// knob, shared by BOTH the built-in state modules (registerStateModules →
// modules.RegisterAll) and the Starlark loader (LoaderConfig.DecodeOptions), so
// the two decode surfaces can never disagree on which keys are reserved or on
// how an unknown key is treated.
//
// The strict flip (keystone spec §5 endgame): strict == true wires
// PolicyError — a typo'd or unrecognized parameter on a migrated (Spec-carrying)
// module FAILS the build with a typed modschema.UnknownKeyError (naming the
// module, the key, and a did-you-mean suggestion). strict == false relaxes to
// PolicyWarn — the historical behavior: a warning is logged through the peel's
// slog logger and the state STILL builds.
//
// Known-not-parameter keys are never flagged under EITHER policy:
//   - Reserved = state.ReservedKeySet(): the fleet-wide reserved keys —
//     requisites (require/watch/onchanges/onfail), generic attributes
//     (onlyif/unless/order/retry/failhard/prereq), and compiler directives
//     (names/listen/*_in). A module never consumes these; the runner, compiler,
//     and attribute wrapper do. They are STILL present in the config map at
//     Build time on the compiled-highstate and ad-hoc paths, so under
//     PolicyError they must be excused here or every real state would fail.
//   - ExtraReserved = {"test", "name"}: "test" is the exec-layer dry-run flag.
//     An ad-hoc run (`zester '*' pkg.removed foo test=True`) and a reactor
//     dispatch pass the whole args map — including "test" — as the state
//     config to the builder, and runExecStates reads args["test"] directly
//     (isTestArg). "name" is the Salt universal state-identifier idiom (M1,
//     keystone spec final fix pass): `test.nop: - name: anchor` is valid Salt
//     even though test.nop declares no `name` parameter of its own — the state
//     ID is meant to double as a label there. A module that DOES declare a
//     `name` parameter (or an alias) still consumes it as that parameter FIRST
//     (Decode's checkUnknownKeys only reaches Reserved/ExtraReserved for a key
//     no parameter claimed — declared names win before the reserved check),
//     so this entry only ever excuses "name" on a module with no `name`
//     parameter/alias; it can never mask a genuine typo of a declared field.
//     Both are known control keys, not module parameters, so neither may fail
//     the build under strict.
func decodeOptions(strict bool, logger *slog.Logger) modschema.DecodeOptions {
	policy := modschema.PolicyError
	if !strict {
		policy = modschema.PolicyWarn
	}
	return modschema.DecodeOptions{
		Unknown:       policy,
		Reserved:      state.ReservedKeySet(),
		ExtraReserved: []string{"test", "name"},
		Warnf: func(format string, args ...any) {
			logger.Warn(fmt.Sprintf(format, args...))
		},
	}
}

// registerStateModules registers every built-in state module with injected
// execution providers and the peel's decode policy. The ordered registration
// table lives next to the modules themselves (pkg/state/modules.RegisterAll).
func registerStateModules(registry *state.Registry, mctx *exec.ModuleContext, opts modschema.DecodeOptions) {
	modules.RegisterAll(registry, mctx, opts)
}
