// Package gitmod implements the git.* state-module family.
package gitmod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the git.* family's slice of the built-in state-module table, in the
// family's historical registration order. The aggregator (pkg/state/modules)
// concatenates every family's Rows into the full table.
var Rows = []regdef.Registration{
	// git.cloned / git.latest — self-documenting schema (the tooling wave:
	// all-primitives, no semantic types needed). git.go/git_latest.go
	// were split into git_cloned.go (the GitCloned module) plus git.go kept as
	// the shared rev-comparison helpers (isHexRevPrefix/isFullHexSHA/
	// resolveRevCommit/revAtHead) used by both git.cloned and git.latest. Each
	// builder is itself a BuildFunc, so no providerBuild adapter.
	{Name: "git.cloned", Spec: gitClonedSpec, Build: NewGitClonedBuilder},
	{Name: "git.latest", Spec: gitLatestSpec, Build: NewGitLatestBuilder},
}
