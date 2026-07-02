package peeld

import (
	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
	"github.com/ptorbus/zester/pkg/state/modules"
)

// registerStateModules registers every built-in state module with injected
// execution providers. Adding a module means adding a line here (future work:
// relocate this list to a RegisterAll in pkg/state/modules so it lives next
// to the modules themselves).
func registerStateModules(registry *state.Registry, mctx *exec.ModuleContext) {
	registry.Register("file.managed", modules.NewFileManagedBuilder(mctx))
	registry.Register("file.directory", modules.NewFileDirectoryBuilder(mctx))
	registry.Register("file.absent", modules.NewFileAbsentBuilder(mctx))
	registry.Register("file.append", modules.NewFileAppendBuilder(mctx))
	registry.Register("cmd.run", modules.NewCmdRunBuilder(mctx))
	registry.Register("pkg.installed", modules.NewPkgInstalledBuilder(mctx))
	registry.Register("user.present", modules.NewUserPresentBuilder(mctx))
	registry.Register("user.absent", modules.NewUserAbsentBuilder(mctx))
	registry.Register("group.present", modules.NewGroupPresentBuilder(mctx))
	registry.Register("group.absent", modules.NewGroupAbsentBuilder(mctx))
	registry.Register("file.symlink", modules.NewFileSymlinkBuilder(mctx))
	registry.Register("file.blockreplace", modules.NewFileBlockReplaceBuilder(mctx))
	registry.Register("file.recurse", modules.NewFileRecurseBuilder(mctx))
	registry.Register("pkg.removed", modules.NewPkgRemovedBuilder(mctx))
	registry.Register("service.running", modules.NewSvcRunningBuilder(mctx))
	registry.Register("service.dead", modules.NewSvcDeadBuilder(mctx))
	registry.Register("service.enabled", modules.NewSvcEnabledBuilder(mctx))
	registry.Register("cron.present", modules.NewCronPresentBuilder(mctx))
	registry.Register("cron.absent", modules.NewCronAbsentBuilder(mctx))
	registry.Register("mount.mounted", modules.NewMountMountedBuilder(mctx))
	registry.Register("sysctl.present", modules.NewSysctlPresentBuilder(mctx))
	registry.Register("locale.present", modules.NewLocalePresentBuilder(mctx))
	registry.Register("timezone.system", modules.NewTimezoneSystemBuilder(mctx))
	registry.Register("pip.installed", modules.NewPipInstalledBuilder(mctx))
	registry.Register("git.cloned", modules.NewGitClonedBuilder(mctx))
	registry.Register("git.latest", modules.NewGitLatestBuilder(mctx))
	registry.Register("file.line", modules.NewFileLineBuilder(mctx))
	registry.Register("file.replace", modules.NewFileReplaceBuilder(mctx))
	registry.Register("file.comment", modules.NewFileCommentBuilder(mctx))
	registry.Register("file.uncomment", modules.NewFileUncommentBuilder(mctx))
	registry.Register("file.keyvalue", modules.NewFileKeyValueBuilder(mctx))
	registry.Register("file.copy", modules.NewFileCopyBuilder(mctx))
	registry.Register("file.touch", modules.NewFileTouchBuilder(mctx))
	registry.Register("pkg.latest", modules.NewPkgLatestBuilder(mctx))
	registry.Register("pkg.purged", modules.NewPkgPurgedBuilder(mctx))
	registry.Register("pkgrepo.managed", modules.NewPkgrepoManagedBuilder(mctx))
	registry.Register("archive.extracted", modules.NewArchiveExtractedBuilder(mctx))
	registry.Register("host.present", modules.NewHostPresentBuilder(mctx))
	registry.Register("host.absent", modules.NewHostAbsentBuilder(mctx))
	registry.Register("ssh_auth.present", modules.NewSSHAuthPresentBuilder(mctx))
	registry.Register("ssh_auth.absent", modules.NewSSHAuthAbsentBuilder(mctx))
	registry.Register("test.ping", modules.NewTestPing)
	registry.Register("test.nop", modules.NewTestNop)
	registry.Register("test.fail_without_changes", modules.NewTestFailWithoutChanges)
	registry.Register("test.succeed_with_changes", modules.NewTestSucceedWithChanges)
	registry.Register("test.configurable_test_state", modules.NewTestConfigurableTestState)
	// module.run captures the registry so it can invoke any other module by
	// name; it must be registered after the targets it may dispatch to.
	registry.Register("module.run", modules.NewModuleRunBuilder(registry))
}
