package modules

// family_host.go — the host.* FAMILY PARAMETER COMPONENT (keystone spec §13).
// Strictly scoped to host.*; see family_file.go for the model.

// hostFileParam selects the managed hosts file. `config` is canonical with a
// Salt-compat `path` alias and the /etc/hosts default.
type hostFileParam struct {
	Path string `zester:"config,aliases=path,default=/etc/hosts" usage:"hosts file path; the path alias is also accepted; defaults to /etc/hosts"`
}
