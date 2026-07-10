// Package exec provides the execution layer for Zester.
// Execution modules are stateless, imperative wrappers around system
// operations (package managers, file I/O, commands, services).
// State modules in pkg/state/modules call into these interfaces to
// perform actual work while keeping idempotency logic separate.
package exec

import (
	"context"
	"io/fs"
)

// PackageExec is the interface for package management operations.
// Each implementation wraps a specific package manager (apt, dnf, yum, brew).
type PackageExec interface {
	// Name returns the package manager name (e.g., "apt", "dnf").
	Name() string

	// IsInstalled reports whether a package is currently installed.
	// "Installed" means fully installed: a Debian package in 'rc' state
	// (removed, conffiles remain) is NOT installed.
	IsInstalled(ctx context.Context, pkg string) (bool, error)

	// InstalledVersion returns the installed version of a package, or ""
	// (with nil error) when the package is not installed. Backs
	// version-pinned convergence checks (pkg.installed with version:).
	InstalledVersion(ctx context.Context, pkg string) (string, error)

	// Install installs a package, optionally at a specific version.
	// An empty version string installs the latest available version.
	Install(ctx context.Context, pkg string, version string) error

	// Remove uninstalls a package.
	Remove(ctx context.Context, pkg string) error

	// Refresh updates the package manager's cache/index.
	Refresh(ctx context.Context) error
}

// FileExec is the interface for file system operations.
type FileExec interface {
	// ReadFile reads the contents of a file.
	ReadFile(ctx context.Context, path string) ([]byte, error)

	// WriteFile writes data to a file with the given permissions.
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error

	// Stat returns file information.
	Stat(ctx context.Context, path string) (fs.FileInfo, error)

	// Remove deletes a file.
	Remove(ctx context.Context, path string) error

	// MkdirAll creates a directory path and all parents.
	MkdirAll(ctx context.Context, path string, perm fs.FileMode) error

	// Chown changes the numeric owner and group of a file.
	Chown(ctx context.Context, path string, uid, gid int) error

	// Chmod changes the permissions of a file.
	Chmod(ctx context.Context, path string, mode fs.FileMode) error

	// RemoveAll removes a path and any children it contains.
	RemoveAll(ctx context.Context, path string) error

	// Symlink creates a symbolic link at linkPath pointing to target.
	Symlink(ctx context.Context, target, linkPath string) error

	// Readlink returns the destination of a symbolic link.
	Readlink(ctx context.Context, path string) (string, error)

	// Owner returns the numeric owner and group of a file — the read-side
	// counterpart of Chown, backing ownership convergence checks.
	Owner(ctx context.Context, path string) (uid, gid int, err error)

	// Walk walks the file tree rooted at root, calling fn for each entry
	// (fs.WalkDir semantics). Lets modules that manage whole trees
	// (file.recurse) stay on the injected provider instead of the real FS.
	Walk(ctx context.Context, root string, fn fs.WalkDirFunc) error
}

// CommandOpts configures a command execution.
type CommandOpts struct {
	// Command is the command string or binary name.
	Command string

	// Args are the command arguments. When empty and Shell is true,
	// Command is executed via "sh -c".
	Args []string

	// Shell runs the command through "sh -c" when true and Args is empty.
	Shell bool

	// Dir is the working directory.
	Dir string

	// Env is a map of additional environment variables.
	Env map[string]string
}

// CommandResult holds the output of a command execution.
type CommandResult struct {
	// Stdout is the captured standard output.
	Stdout string

	// Stderr is the captured standard error.
	Stderr string

	// ExitCode is the process exit code.
	ExitCode int
}

// CommandExec is the interface for running system commands.
type CommandExec interface {
	// Run executes a command and returns its result.
	Run(ctx context.Context, opts CommandOpts) (*CommandResult, error)
}

// ServiceExec is the interface for service management operations.
// Implementations wrap init systems like systemd or launchd.
type ServiceExec interface {
	// Name returns the service manager name (e.g., "systemd", "launchd").
	Name() string

	// IsRunning reports whether a service is currently running.
	IsRunning(ctx context.Context, service string) (bool, error)

	// IsEnabled reports whether a service is enabled at boot.
	IsEnabled(ctx context.Context, service string) (bool, error)

	// Start starts a service.
	Start(ctx context.Context, service string) error

	// Stop stops a service.
	Stop(ctx context.Context, service string) error

	// Restart restarts a service.
	Restart(ctx context.Context, service string) error

	// Enable enables a service at boot.
	Enable(ctx context.Context, service string) error

	// Disable disables a service at boot.
	Disable(ctx context.Context, service string) error
}

// CronExec is the interface for managing crontab entries.
type CronExec interface {
	// List returns all cron entries for the given user.
	// An empty user string returns entries for the current user.
	List(ctx context.Context, user string) ([]CronEntry, error)

	// Set adds or replaces a cron entry identified by its Command field.
	Set(ctx context.Context, user string, entry CronEntry) error

	// Remove deletes the cron entry matching the given command string.
	Remove(ctx context.Context, user string, command string) error
}
