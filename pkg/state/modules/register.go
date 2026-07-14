package modules

import (
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// BuildFunc constructs a state.Builder from the injected providers and the
// decode policy. Every module built through this shape threads DecodeOptions
// into its spec.Decode call.
type BuildFunc func(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder

// PlainBuildFunc constructs a state.Builder that needs no exec providers but
// still threads the decode policy (reserved keys, unknown-key handling) into its
// spec.Decode call. The test.* helpers use this shape: no ModuleContext, but a
// real self-documenting decode.
type PlainBuildFunc func(opts modschema.DecodeOptions) state.Builder

// Registration is one row of the built-in state-module table. Exactly one of the
// three builder shapes is set:
//
//   - Build             — needs the ModuleContext (providers) and decode policy.
//   - BuildPlain        — needs no providers, but threads the decode policy (the
//     test.* helpers).
//   - BuildWithRegistry — needs the registry itself (module.run dispatches to it).
//
// Spec is non-nil once a module is migrated to a self-documenting schema; the
// row is then RegisterSpec'd (name = Spec.Module) instead of plain-Register'd.
type Registration struct {
	Name string
	Spec *modschema.Spec

	Build             BuildFunc
	BuildPlain        PlainBuildFunc
	BuildWithRegistry func(reg *state.Registry) state.Builder
}

// mustSpec compiles a module spec at package init, panicking on a compile error
// — an invalid schema declaration is a programming error, caught at load, never
// a runtime condition.
func mustSpec(module string, kind modschema.Kind, proto any, doc modschema.Doc, opts ...modschema.SpecOption) *modschema.Spec {
	s, err := modschema.NewSpec(module, kind, proto, doc, opts...)
	if err != nil {
		panic(fmt.Sprintf("modules: spec %s: %v", module, err))
	}
	return s
}

// Registration is the ordered table of every built-in state module. It
// reproduces the historical registration list verbatim (same names, same order),
// with module.run LAST so it registers after every target it may dispatch to.
// Every module now carries a Spec (the migration ratchet reached zero — keystone
// spec §9 gate 3); the doc-coverage conformance test asserts no exemptions
// remain.
var registrations = []Registration{
	// file.managed — self-documenting schema (the BD-1 flagship: template on
	// paramtypes.TemplateFlag, mode on paramtypes.FileMode with a lazy 0644
	// default). NewFileManagedBuilder is itself a BuildFunc (it threads opts into
	// spec.Decode), so no providerBuild adapter.
	{Name: "file.managed", Spec: fileManagedSpec, Build: NewFileManagedBuilder},
	// file.directory — self-documenting schema (the declared-facet exemplar:
	// mode on a lazy paramtypes.FileMode with a dir_mode fallback alias).
	// NewFileDirectoryBuilder is itself a BuildFunc, so no providerBuild adapter.
	{Name: "file.directory", Spec: fileDirectorySpec, Build: NewFileDirectoryBuilder},
	// file.absent / file.append — self-documenting schema (all-primitives /
	// StringList wave). Both builders are themselves BuildFuncs, so no adapter.
	{Name: "file.absent", Spec: fileAbsentSpec, Build: NewFileAbsentBuilder},
	{Name: "file.append", Spec: fileAppendSpec, Build: NewFileAppendBuilder},
	// cmd.run — self-documenting schema (command primary; args on
	// paramtypes.StringList, env on paramtypes.StringMap; the
	// require-file-provider-when-creates rule stays in the builder tail).
	// NewCmdRunBuilder is itself a BuildFunc, so no providerBuild adapter.
	{Name: "cmd.run", Spec: cmdRunSpec, Build: NewCmdRunBuilder},
	// pkg.installed — self-documenting schema (all-primitives migration wave).
	// NewPkgInstalledBuilder is itself a BuildFunc, so no adapter.
	{Name: "pkg.installed", Spec: pkgInstalledSpec, Build: NewPkgInstalledBuilder},
	// user.present — self-documenting schema (the semantic-type-heavy migration:
	// gid on paramtypes.GroupRef, groups/optional_groups on paramtypes.StringList,
	// a sensitive password). NewUserPresentBuilder is itself a BuildFunc (it
	// threads opts into spec.Decode), so no providerBuild adapter.
	{Name: "user.present", Spec: userPresentSpec, Build: NewUserPresentBuilder},
	// user.absent — self-documenting schema (all-primitives: name primary,
	// purge/force plain bools; nothing sensitive). NewUserAbsentBuilder is
	// itself a BuildFunc, so no providerBuild adapter.
	{Name: "user.absent", Spec: userAbsentSpec, Build: NewUserAbsentBuilder},
	// group.present / group.absent — self-documenting schema (group.present's
	// gid is a PLAIN int; members/addusers/delusers on paramtypes.StringList).
	// Both builders are themselves BuildFuncs, so no providerBuild adapter.
	{Name: "group.present", Spec: groupPresentSpec, Build: NewGroupPresentBuilder},
	{Name: "group.absent", Spec: groupAbsentSpec, Build: NewGroupAbsentBuilder},
	// file.symlink — self-documenting schema (all-primitives wave).
	// NewFileSymlinkBuilder is itself a BuildFunc, so no adapter.
	{Name: "file.symlink", Spec: fileSymlinkSpec, Build: NewFileSymlinkBuilder},
	// file.blockreplace — self-documenting schema (all-primitives / marker
	// wave). NewFileBlockReplaceBuilder is itself a BuildFunc, so no adapter.
	{Name: "file.blockreplace", Spec: fileBlockReplaceSpec, Build: NewFileBlockReplaceBuilder},
	// file.recurse — self-documenting schema (the declared-only facet exemplar:
	// dir_mode DECLARED-ONLY on a lazy paramtypes.FileMode, file_mode lazy 0644).
	// NewFileRecurseBuilder is itself a BuildFunc, so no providerBuild adapter.
	{Name: "file.recurse", Spec: fileRecurseSpec, Build: NewFileRecurseBuilder},
	// pkg.removed — pilot #1: self-documenting schema. NewPkgRemovedBuilder is
	// itself a BuildFunc (it threads opts into spec.Decode), so no adapter.
	{Name: "pkg.removed", Spec: pkgRemovedSpec, Build: NewPkgRemovedBuilder},
	// service.running / service.dead — the semantic-type pilot (tranche 0E):
	// enable migrated onto paramtypes.TriState. NewSvcRunningBuilder /
	// NewSvcDeadBuilder are themselves BuildFuncs (they thread opts into
	// spec.Decode), so no providerBuild adapter.
	{Name: "service.running", Spec: svcRunningSpec, Build: NewSvcRunningBuilder},
	{Name: "service.dead", Spec: svcDeadSpec, Build: NewSvcDeadBuilder},
	// service.enabled — self-documenting schema (name primary only; a numeric
	// name coerces, a composite name is rejected — BD-6). NewSvcEnabledBuilder is
	// itself a BuildFunc, so no providerBuild adapter.
	{Name: "service.enabled", Spec: svcEnabledSpec, Build: NewSvcEnabledBuilder},
	// cron.present / cron.absent — self-documenting schema (cron.present's
	// schedule fields carry eager default=* — a numeric minute now coerces to
	// "5", the BD-3 activation). Both builders are themselves BuildFuncs.
	{Name: "cron.present", Spec: cronPresentSpec, Build: NewCronPresentBuilder},
	{Name: "cron.absent", Spec: cronAbsentSpec, Build: NewCronAbsentBuilder},
	// mount.mounted — self-documenting schema (parameter-decode-ONLY migration:
	// Check/Apply/Revert and the deferred live-facet policy are unchanged;
	// dump/pass are plain ints, persist an eager default=true bool).
	// NewMountMountedBuilder is itself a BuildFunc, so no providerBuild adapter.
	{Name: "mount.mounted", Spec: mountMountedSpec, Build: NewMountMountedBuilder},
	// sysctl.present — self-documenting schema (value required, persist an eager
	// default=true bool; the require-file-provider-when-persist rule stays in the
	// builder tail). NewSysctlPresentBuilder is itself a BuildFunc.
	{Name: "sysctl.present", Spec: sysctlPresentSpec, Build: NewSysctlPresentBuilder},
	// archive.extracted / git.cloned / git.latest / pip.installed /
	// timezone.system / locale.present — self-documenting schema (the tooling
	// wave: all-primitives, no semantic types needed). git.go/git_latest.go
	// were split into git_cloned.go (the GitCloned module) plus git.go kept as
	// the shared rev-comparison helpers (isHexRevPrefix/isFullHexSHA/
	// resolveRevCommit/revAtHead) used by both git.cloned and git.latest. Each
	// builder is itself a BuildFunc, so no providerBuild adapter.
	{Name: "locale.present", Spec: localePresentSpec, Build: NewLocalePresentBuilder},
	{Name: "timezone.system", Spec: timezoneSystemSpec, Build: NewTimezoneSystemBuilder},
	{Name: "pip.installed", Spec: pipInstalledSpec, Build: NewPipInstalledBuilder},
	{Name: "git.cloned", Spec: gitClonedSpec, Build: NewGitClonedBuilder},
	{Name: "git.latest", Spec: gitLatestSpec, Build: NewGitLatestBuilder},
	// file.line / file.replace / file.keyvalue — self-documenting schema (the
	// file-surgery wave: file.line's `mode` is an action enum, file.replace's
	// `pattern` is required with a builder-tail regex compile, file.keyvalue's
	// `key_values` is a paramtypes.StringMap with a module-local key/value
	// merge). Each builder is itself a BuildFunc, so no providerBuild adapter.
	{Name: "file.line", Spec: fileLineSpec, Build: NewFileLineBuilder},
	{Name: "file.replace", Spec: fileReplaceSpec, Build: NewFileReplaceBuilder},
	// file.comment / file.uncomment — the N:1 exemplar: one FileComment proto,
	// two Specs (one per registered name), each with its own documentation.
	{Name: "file.comment", Spec: fileCommentSpec, Build: NewFileCommentBuilder},
	{Name: "file.uncomment", Spec: fileUncommentSpec, Build: NewFileUncommentBuilder},
	{Name: "file.keyvalue", Spec: fileKeyValueSpec, Build: NewFileKeyValueBuilder},
	// file.copy / file.touch — self-documenting schema (all-primitives wave;
	// file.copy's source is `required`). Both builders are themselves
	// BuildFuncs, so no adapter.
	{Name: "file.copy", Spec: fileCopySpec, Build: NewFileCopyBuilder},
	{Name: "file.touch", Spec: fileTouchSpec, Build: NewFileTouchBuilder},
	// pkg.latest / pkg.purged — self-documenting schema (all-primitives
	// migration wave). Both builders are themselves BuildFuncs, so no adapter.
	{Name: "pkg.latest", Spec: pkgLatestSpec, Build: NewPkgLatestBuilder},
	{Name: "pkg.purged", Spec: pkgPurgedSpec, Build: NewPkgPurgedBuilder},
	// pkgrepo.managed — self-documenting schema (a parameter-decode-only
	// migration: Check/Apply/Revert and the deferred in-place key-rotation
	// detection are unchanged; humanname is a lazy DERIVED default assigned in
	// the builder tail, enabled/gpgcheck/refresh eager default=true bools).
	// NewPkgrepoManagedBuilder is itself a BuildFunc, so no providerBuild adapter.
	{Name: "pkgrepo.managed", Spec: pkgrepoManagedSpec, Build: NewPkgrepoManagedBuilder},
	// archive.extracted — self-documenting schema (the tooling wave; see the
	// registration comment above locale.present). NewArchiveExtractedBuilder is
	// itself a BuildFunc, so no providerBuild adapter.
	{Name: "archive.extracted", Spec: archiveExtractedSpec, Build: NewArchiveExtractedBuilder},
	// host.present / host.absent — self-documenting schema (the per-field ALIAS
	// exemplar: the hosts-file path binds `config` with a `path` alias and an
	// eager default=/etc/hosts). Both builders are themselves BuildFuncs, so no
	// providerBuild adapter.
	{Name: "host.present", Spec: hostPresentSpec, Build: NewHostPresentBuilder},
	{Name: "host.absent", Spec: hostAbsentSpec, Build: NewHostAbsentBuilder},
	// ssh_auth.present / ssh_auth.absent — self-documenting schema (enc eager
	// default=ssh-rsa; the name-TrimSpace and require-user-OR-config cross-field
	// rule stay in the builder tail — module logic, not schema; key material is
	// PUBLIC, so nothing is sensitive). Both builders are themselves BuildFuncs.
	{Name: "ssh_auth.present", Spec: sshAuthPresentSpec, Build: NewSSHAuthPresentBuilder},
	{Name: "ssh_auth.absent", Spec: sshAuthAbsentSpec, Build: NewSSHAuthAbsentBuilder},
	// test.* — self-documenting schema (the final wave; BuildPlain factories:
	// no exec providers, but they thread the decode policy). test.ping/test.nop
	// declare no parameters; test.fail_without_changes/test.succeed_with_changes
	// carry a `comment` string; test.configurable_test_state carries eager
	// default=true `result`/`changes` bools (the BD-2/BD-7 activation) plus a
	// `comment`.
	{Name: "test.ping", Spec: testPingSpec, BuildPlain: NewTestPingBuilder},
	{Name: "test.nop", Spec: testNopSpec, BuildPlain: NewTestNopBuilder},
	{Name: "test.fail_without_changes", Spec: testFailWithoutChangesSpec, BuildPlain: NewTestFailWithoutChangesBuilder},
	{Name: "test.succeed_with_changes", Spec: testSucceedWithChangesSpec, BuildPlain: NewTestSucceedWithChangesBuilder},
	{Name: "test.configurable_test_state", Spec: testConfigurableTestStateSpec, BuildPlain: NewTestConfigurableTestStateBuilder},
	// module.run — self-documenting schema (OpenParams: a passthrough with no
	// fixed parameters; its dynamic target/key handling is unchanged). It
	// captures the registry so it can invoke any other module by name, and must
	// be registered after the targets it may dispatch to.
	{Name: "module.run", Spec: moduleRunSpec, BuildWithRegistry: NewModuleRunBuilder},
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
		return r.BuildPlain(opts)
	default:
		return r.BuildWithRegistry(reg)
	}
}
