package exec

import "context"

// UserInfo holds information about a system user account.
type UserInfo struct {
	Name     string
	Home     string
	Shell    string
	FullName string
	UID      int
	GID      int
	Groups   []string
}

// UserCreateOpts configures user account creation.
type UserCreateOpts struct {
	Name         string
	UID          int    // 0 = auto-assign
	GID          int    // 0 = auto-assign
	PrimaryGroup string // name-based, overrides GID
	Groups       []string
	Home         string
	Shell        string
	CreateHome   bool
	System       bool
	Password     string // hashed password
	FullName     string
}

// UserModifyOpts configures user account modification.
// Only non-nil pointer fields are applied.
type UserModifyOpts struct {
	UID      *int
	GID      *int
	Groups   *[]string // replaces all supplementary groups
	Home     *string
	Shell    *string
	Password *string
	FullName *string
	// PrimaryGroup sets the primary group BY NAME (usermod -g <name>);
	// use GID for numeric ids. Mirrors UserCreateOpts.PrimaryGroup.
	PrimaryGroup *string
}

// UserExec is the interface for user account management operations.
type UserExec interface {
	// Lookup returns information about a user account, or nil if not found.
	Lookup(ctx context.Context, name string) (*UserInfo, error)

	// Create creates a new user account.
	Create(ctx context.Context, opts UserCreateOpts) error

	// Modify modifies an existing user account.
	Modify(ctx context.Context, name string, opts UserModifyOpts) error

	// Delete deletes a user account. If removeHome is true, the home
	// directory is also removed.
	Delete(ctx context.Context, name string, removeHome bool) error

	// PasswordHash returns the account's shadow password hash ("" when the
	// account has none or shadow data is unavailable) — the read-side
	// counterpart of Create/Modify's Password, backing password-drift
	// convergence checks.
	PasswordHash(ctx context.Context, name string) (string, error)
}
