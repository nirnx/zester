package modules

import (
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// BuildFunc constructs a state.Builder from the injected providers and the
// decode policy. Every module built through this shape can thread DecodeOptions
// into its spec.Decode call; a legacy providers-only factory (which ignores the
// options) is adapted with providerBuild.
type BuildFunc func(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder

// Registration is one row of the built-in state-module table. Exactly one of the
// three builder shapes is set:
//
//   - Build             — needs the ModuleContext (providers) and decode policy.
//   - BuildPlain        — needs nothing (for example the test.* helpers).
//   - BuildWithRegistry — needs the registry itself (module.run dispatches to it).
//
// Spec is non-nil once a module is migrated to a self-documenting schema; the
// row is then RegisterSpec'd (name = Spec.Module) instead of plain-Register'd.
type Registration struct {
	Name string
	Spec *modschema.Spec

	Build             BuildFunc
	BuildPlain        state.Builder
	BuildWithRegistry func(reg *state.Registry) state.Builder
}

// providerBuild adapts a legacy providers-only builder factory (no DecodeOptions)
// to the opts-threaded BuildFunc shape. Unmigrated modules ignore the options.
func providerBuild(f func(*exec.ModuleContext) state.Builder) BuildFunc {
	return func(mctx *exec.ModuleContext, _ modschema.DecodeOptions) state.Builder {
		return f(mctx)
	}
}

// mustSpec compiles a module spec at package init, panicking on a compile error
// — an invalid schema declaration is a programming error, caught at load, never
// a runtime condition.
func mustSpec(module string, kind modschema.Kind, proto any, doc modschema.Doc) *modschema.Spec {
	s, err := modschema.NewSpec(module, kind, proto, doc)
	if err != nil {
		panic(fmt.Sprintf("modules: spec %s: %v", module, err))
	}
	return s
}

// Registration is the ordered table of every built-in state module. It
// reproduces the historical registration list verbatim (same names, same order),
// with module.run LAST so it registers after every target it may dispatch to.
// Migrated modules carry a Spec; the rest keep their legacy providers-only
// factories through providerBuild.
var registrations = []Registration{
	{Name: "file.managed", Build: providerBuild(NewFileManagedBuilder)},
	{Name: "file.directory", Build: providerBuild(NewFileDirectoryBuilder)},
	{Name: "file.absent", Build: providerBuild(NewFileAbsentBuilder)},
	{Name: "file.append", Build: providerBuild(NewFileAppendBuilder)},
	{Name: "cmd.run", Build: providerBuild(NewCmdRunBuilder)},
	{Name: "pkg.installed", Build: providerBuild(NewPkgInstalledBuilder)},
	{Name: "user.present", Build: providerBuild(NewUserPresentBuilder)},
	{Name: "user.absent", Build: providerBuild(NewUserAbsentBuilder)},
	{Name: "group.present", Build: providerBuild(NewGroupPresentBuilder)},
	{Name: "group.absent", Build: providerBuild(NewGroupAbsentBuilder)},
	{Name: "file.symlink", Build: providerBuild(NewFileSymlinkBuilder)},
	{Name: "file.blockreplace", Build: providerBuild(NewFileBlockReplaceBuilder)},
	{Name: "file.recurse", Build: providerBuild(NewFileRecurseBuilder)},
	// pkg.removed — pilot #1: self-documenting schema. NewPkgRemovedBuilder is
	// itself a BuildFunc (it threads opts into spec.Decode), so no adapter.
	{Name: "pkg.removed", Spec: pkgRemovedSpec, Build: NewPkgRemovedBuilder},
	// service.running / service.dead — the semantic-type pilot (tranche 0E):
	// enable migrated onto paramtypes.TriState. NewSvcRunningBuilder /
	// NewSvcDeadBuilder are themselves BuildFuncs (they thread opts into
	// spec.Decode), so no providerBuild adapter.
	{Name: "service.running", Spec: svcRunningSpec, Build: NewSvcRunningBuilder},
	{Name: "service.dead", Spec: svcDeadSpec, Build: NewSvcDeadBuilder},
	{Name: "service.enabled", Build: providerBuild(NewSvcEnabledBuilder)},
	{Name: "cron.present", Build: providerBuild(NewCronPresentBuilder)},
	{Name: "cron.absent", Build: providerBuild(NewCronAbsentBuilder)},
	{Name: "mount.mounted", Build: providerBuild(NewMountMountedBuilder)},
	{Name: "sysctl.present", Build: providerBuild(NewSysctlPresentBuilder)},
	{Name: "locale.present", Build: providerBuild(NewLocalePresentBuilder)},
	{Name: "timezone.system", Build: providerBuild(NewTimezoneSystemBuilder)},
	{Name: "pip.installed", Build: providerBuild(NewPipInstalledBuilder)},
	{Name: "git.cloned", Build: providerBuild(NewGitClonedBuilder)},
	{Name: "git.latest", Build: providerBuild(NewGitLatestBuilder)},
	{Name: "file.line", Build: providerBuild(NewFileLineBuilder)},
	{Name: "file.replace", Build: providerBuild(NewFileReplaceBuilder)},
	{Name: "file.comment", Build: providerBuild(NewFileCommentBuilder)},
	{Name: "file.uncomment", Build: providerBuild(NewFileUncommentBuilder)},
	{Name: "file.keyvalue", Build: providerBuild(NewFileKeyValueBuilder)},
	{Name: "file.copy", Build: providerBuild(NewFileCopyBuilder)},
	{Name: "file.touch", Build: providerBuild(NewFileTouchBuilder)},
	{Name: "pkg.latest", Build: providerBuild(NewPkgLatestBuilder)},
	{Name: "pkg.purged", Build: providerBuild(NewPkgPurgedBuilder)},
	{Name: "pkgrepo.managed", Build: providerBuild(NewPkgrepoManagedBuilder)},
	{Name: "archive.extracted", Build: providerBuild(NewArchiveExtractedBuilder)},
	{Name: "host.present", Build: providerBuild(NewHostPresentBuilder)},
	{Name: "host.absent", Build: providerBuild(NewHostAbsentBuilder)},
	{Name: "ssh_auth.present", Build: providerBuild(NewSSHAuthPresentBuilder)},
	{Name: "ssh_auth.absent", Build: providerBuild(NewSSHAuthAbsentBuilder)},
	{Name: "test.ping", BuildPlain: NewTestPing},
	{Name: "test.nop", BuildPlain: NewTestNop},
	{Name: "test.fail_without_changes", BuildPlain: NewTestFailWithoutChanges},
	{Name: "test.succeed_with_changes", BuildPlain: NewTestSucceedWithChanges},
	{Name: "test.configurable_test_state", BuildPlain: NewTestConfigurableTestState},
	// module.run captures the registry so it can invoke any other module by name;
	// it must be registered after the targets it may dispatch to.
	{Name: "module.run", BuildWithRegistry: NewModuleRunBuilder},
}

// RegisterAll registers every built-in state module into reg, threading the
// decode policy opts to the migrated (Spec-carrying) modules. Spec rows are
// RegisterSpec'd (so Describe/SpecNames/Parse see them); the rest are plain
// Register'd. A malformed registration (no builder, or a Spec whose module name
// disagrees with the row name, or a RegisterSpec failure) is a programming error
// and panics at startup.
func RegisterAll(reg *state.Registry, mctx *exec.ModuleContext, opts modschema.DecodeOptions) {
	for _, r := range registrations {
		b := buildOne(r, reg, mctx, opts)
		if r.Spec != nil {
			if r.Spec.Module != r.Name {
				panic(fmt.Sprintf("modules: registration %q has spec for module %q", r.Name, r.Spec.Module))
			}
			if err := reg.RegisterSpec(r.Spec, b); err != nil {
				panic(fmt.Sprintf("modules: register spec %s: %v", r.Name, err))
			}
			continue
		}
		reg.Register(r.Name, b)
	}
}

// buildOne resolves the single configured builder shape of a registration.
func buildOne(r Registration, reg *state.Registry, mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	n := 0
	if r.Build != nil {
		n++
	}
	if r.BuildPlain != nil {
		n++
	}
	if r.BuildWithRegistry != nil {
		n++
	}
	if n != 1 {
		panic(fmt.Sprintf("modules: registration %q must set exactly one builder shape, has %d", r.Name, n))
	}
	switch {
	case r.Build != nil:
		return r.Build(mctx, opts)
	case r.BuildPlain != nil:
		return r.BuildPlain
	default:
		return r.BuildWithRegistry(reg)
	}
}
