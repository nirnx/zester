package exec

import "context"

// SysctlExec is the interface for reading and writing kernel parameters.
type SysctlExec interface {
	// Get returns the current value of a sysctl key.
	Get(ctx context.Context, key string) (string, error)

	// Set applies a sysctl key=value at runtime (not persisted across reboots).
	Set(ctx context.Context, key, value string) error

	// Persist writes the key=value to /etc/sysctl.d/99-zester.conf so it
	// survives reboots.
	Persist(ctx context.Context, key, value string) error
}
