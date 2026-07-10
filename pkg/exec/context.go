package exec

import (
	"log/slog"
	osexec "os/exec"
)

// ProviderSet holds the detected execution providers for the current platform.
// Created once at peel startup and stable for the peel's lifetime.
type ProviderSet struct {
	Package PackageExec
	File    FileExec
	Command CommandExec
	Service ServiceExec
	User    UserExec
	Group   GroupExec
	Cron    CronExec
	Sysctl  SysctlExec
	Mount   MountExec
}

// ModuleContext is the execution context passed to state modules.
// It is the Go equivalent of Salt's __salt__ + __grains__ + __pillar__.
// The embedded ProviderSet is stable for the peel's lifetime (field promotion
// keeps mctx.Package-style selectors working); Facts and Settings are
// refreshed per-request.
type ModuleContext struct {
	ProviderSet

	Facts    map[string]any
	Settings map[string]any
	Logger   *slog.Logger

	// RenderTemplate renders a template string with the current facts, settings,
	// and optional extra context variables. Set by the peel after engine creation.
	// Nil if template rendering is not available.
	RenderTemplate func(name, source string, extra map[string]any) (string, error)
}

// NewModuleContext creates a ModuleContext from a ProviderSet.
func NewModuleContext(ps *ProviderSet, facts, settings map[string]any, logger *slog.Logger) *ModuleContext {
	if logger == nil {
		logger = slog.Default()
	}
	return &ModuleContext{
		ProviderSet: *ps,
		Facts:       facts,
		Settings:    settings,
		Logger:      logger,
	}
}

// WithFactsSettings returns a shallow copy of the context with Facts and
// Settings replaced, sharing the embedded ProviderSet (and Logger /
// RenderTemplate) with the receiver. The receiver is not modified.
//
// This is the building block for concurrent READ-ONLY module execution: a
// derived context is never mutated in place, so multiple derived contexts can
// be used concurrently as long as the modules they run only read from the
// shared providers. The peel will use this in a later wave to run
// facts./settings./test.ping queries outside the exec mutex.
func (mctx *ModuleContext) WithFactsSettings(facts, settings map[string]any) *ModuleContext {
	dup := *mctx
	dup.Facts = facts
	dup.Settings = settings
	return &dup
}

// DetectProviders auto-detects the available execution providers based on
// collected facts (os.family) and available binaries on the system.
// CommandExec and FileExec are always available (OS-level operations).
// PackageExec is detected from the OS family; ServiceExec detects systemd.
func DetectProviders(facts map[string]any, logger *slog.Logger) *ProviderSet {
	if logger == nil {
		logger = slog.Default()
	}

	cmdExec := &OSCommandExec{}
	fileExec := &OSFileExec{}
	ps := &ProviderSet{
		Command: cmdExec,
		File:    fileExec,
	}

	osFamily := factString(facts, "os", "family")
	logger.Info("detecting providers", "os_family", osFamily)

	switch osFamily {
	case "debian":
		ps.Package = NewAptProvider(cmdExec)
	case "redhat", "rhel", "fedora", "suse":
		if commandExists("dnf") {
			ps.Package = NewDnfProvider(cmdExec)
		} else {
			ps.Package = NewYumProvider(cmdExec)
		}
	case "darwin":
		if commandExists("brew") {
			ps.Package = NewBrewProvider(cmdExec)
		}
	default:
		// Fallback: probe for common package managers.
		switch {
		case commandExists("apt-get"):
			ps.Package = NewAptProvider(cmdExec)
		case commandExists("dnf"):
			ps.Package = NewDnfProvider(cmdExec)
		case commandExists("yum"):
			ps.Package = NewYumProvider(cmdExec)
		case commandExists("brew"):
			ps.Package = NewBrewProvider(cmdExec)
		}
	}

	if ps.Package != nil {
		logger.Info("package provider detected", "provider", ps.Package.Name())
	} else {
		logger.Warn("no package provider detected")
	}

	// Service provider: detect systemctl on PATH (Linux with systemd).
	if commandExists("systemctl") {
		ps.Service = NewSystemdProvider(cmdExec)
		logger.Info("service provider detected", "provider", "systemd")
	}

	// User/group providers: detect useradd/groupadd on PATH (Linux).
	if commandExists("useradd") {
		ps.User = NewUseraddProvider(cmdExec)
		logger.Info("user provider detected", "provider", "useradd")
	}
	if commandExists("groupadd") {
		ps.Group = NewGroupaddProvider(cmdExec)
		logger.Info("group provider detected", "provider", "groupadd")
	}

	// Cron/sysctl/mount providers. These constructors existed but were
	// never wired here — cron.present, sysctl.present, and mount.mounted
	// could not run on a real peel (builder: "no X provider available").
	if commandExists("crontab") {
		ps.Cron = NewCrontabProvider(cmdExec)
		logger.Info("cron provider detected", "provider", "crontab")
	}
	if commandExists("sysctl") {
		ps.Sysctl = NewProcfsProvider(cmdExec, fileExec)
		logger.Info("sysctl provider detected", "provider", "procfs")
	}
	if commandExists("mount") {
		ps.Mount = NewFstabProvider(cmdExec, fileExec)
		logger.Info("mount provider detected", "provider", "fstab")
	}

	return ps
}

// factString extracts a nested string value from facts using the given keys.
// For example, factString(facts, "os", "family") returns facts["os"]["family"].
func factString(facts map[string]any, keys ...string) string {
	var current any = facts
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current, ok = m[key]
		if !ok {
			return ""
		}
	}
	s, _ := current.(string)
	return s
}

// commandExists checks whether a binary is available on PATH.
func commandExists(name string) bool {
	_, err := osexec.LookPath(name)
	return err == nil
}
