package exec

import "context"

// GroupInfo holds information about a system group.
type GroupInfo struct {
	Name    string
	GID     int
	Members []string
}

// GroupCreateOpts configures group creation.
type GroupCreateOpts struct {
	Name   string
	GID    int // 0 = auto-assign
	System bool
}

// GroupModifyOpts configures group modification.
// Only non-nil/non-empty fields are applied.
type GroupModifyOpts struct {
	GID        *int
	AddMembers []string
	DelMembers []string
}

// GroupExec is the interface for group management operations.
type GroupExec interface {
	// Lookup returns information about a group, or nil if not found.
	Lookup(ctx context.Context, name string) (*GroupInfo, error)

	// Create creates a new group.
	Create(ctx context.Context, opts GroupCreateOpts) error

	// Modify modifies an existing group.
	Modify(ctx context.Context, name string, opts GroupModifyOpts) error

	// Delete deletes a group.
	Delete(ctx context.Context, name string) error
}
