package modules

// family_ssh_auth.go — the ssh_auth.* FAMILY PARAMETER COMPONENTS (keystone
// spec §13). Strictly scoped to ssh_auth.*; see family_file.go for the model.
// The primary (`name`, the key blob) stays member-declared: its usage is each
// member's own action. The user-or-config cross-field requirement is builder-
// tail module logic in both members, part of this component's contract.

// sshAuthTargetParam selects WHICH authorized_keys file is managed: the
// account's (user) or an explicit path (config; takes precedence).
type sshAuthTargetParam struct {
	User   string `zester:"user" usage:"account whose authorized_keys is managed; user or config is required"`
	Config string `zester:"config" usage:"explicit authorized_keys path; user or config is required (config takes precedence over the user's home)"`
}
