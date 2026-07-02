// Package version holds build-time version info injected via ldflags.
package version

var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildDate = "unknown"
)
