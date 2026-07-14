package pkgmod

// family_pkg.go — the pkg.* FAMILY PARAMETER COMPONENT (keystone spec §13).
// Strictly scoped to pkg.*; see family_file.go for the model.

// pkgRefreshParam controls a package-database refresh before the operation.
// The DEFAULT is member-supplied (§13): pkg.installed supplies false (install
// from the current database), pkg.latest true (a staleness check needs fresh
// indexes) — modschema.WithDefault("refresh", ...) is mandatory for embedders.
type pkgRefreshParam struct {
	Refresh bool `zester:"refresh,memberdefault" usage:"refresh the package database before the operation; the default is member-specific; a boolean that also accepts the integers 1 (true) and 0 (false)"`
}
