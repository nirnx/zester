// Package version holds build-time version info injected via ldflags.
package version

import "fmt"

var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildDate = "unknown"
)

// String returns the one-line --version banner for a binary.
func String(component string) string {
	return fmt.Sprintf("%s %s (commit %s, built %s)", component, Version, GitCommit, BuildDate)
}
