package exec

import "context"

// MountEntry represents a filesystem mount (fstab record or active mount).
type MountEntry struct {
	Device     string
	MountPoint string
	FSType     string
	Options    string
	Dump       int
	Pass       int
}

// MountExec is the interface for filesystem mount management.
type MountExec interface {
	// IsMounted reports whether mountPoint is currently mounted.
	IsMounted(ctx context.Context, mountPoint string) (bool, error)

	// Mount mounts the given entry.
	Mount(ctx context.Context, entry MountEntry) error

	// Unmount unmounts the given mount point.
	Unmount(ctx context.Context, mountPoint string) error

	// GetFstab returns the fstab entry for the mount point, or nil if absent.
	GetFstab(ctx context.Context, mountPoint string) (*MountEntry, error)

	// SetFstab adds or updates the fstab entry for the mount point.
	SetFstab(ctx context.Context, entry MountEntry) error

	// RemoveFstab removes the fstab entry for the mount point.
	RemoveFstab(ctx context.Context, mountPoint string) error
}
