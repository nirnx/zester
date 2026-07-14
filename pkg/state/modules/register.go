// Package modules aggregates the built-in state-module families
// (pkg/state/modules/<family>) into the single registration table the peel
// and docgen consume, and owns the peel dispatch-surface documentation table
// (dispatch_docs.go). Each family package exports its slice of the table as
// `Rows []regdef.Registration`; module implementations, family parameter
// components, member tests, and contract fixtures all live in the family
// packages.
package modules

import (
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	archivemod "github.com/nirnx/zester/pkg/state/modules/archive"
	cmdmod "github.com/nirnx/zester/pkg/state/modules/cmd"
	cronmod "github.com/nirnx/zester/pkg/state/modules/cron"
	filemod "github.com/nirnx/zester/pkg/state/modules/file"
	gitmod "github.com/nirnx/zester/pkg/state/modules/git"
	groupmod "github.com/nirnx/zester/pkg/state/modules/group"
	hostmod "github.com/nirnx/zester/pkg/state/modules/host"
	localemod "github.com/nirnx/zester/pkg/state/modules/locale"
	modulemod "github.com/nirnx/zester/pkg/state/modules/module"
	mountmod "github.com/nirnx/zester/pkg/state/modules/mount"
	pipmod "github.com/nirnx/zester/pkg/state/modules/pip"
	pkgmod "github.com/nirnx/zester/pkg/state/modules/pkg"
	pkgrepomod "github.com/nirnx/zester/pkg/state/modules/pkgrepo"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
	servicemod "github.com/nirnx/zester/pkg/state/modules/service"
	sshauthmod "github.com/nirnx/zester/pkg/state/modules/ssh_auth"
	sysctlmod "github.com/nirnx/zester/pkg/state/modules/sysctl"
	testmod "github.com/nirnx/zester/pkg/state/modules/test"
	timezonemod "github.com/nirnx/zester/pkg/state/modules/timezone"
	usermod "github.com/nirnx/zester/pkg/state/modules/user"
)

// registrations is the ordered table of every built-in state module,
// concatenated from the per-family Rows in historical family order (the order
// each family first appeared in the pre-split central table; rows keep their
// relative order within each family), with module.run LAST so it registers
// after every target it may dispatch to. Every module carries a Spec (the
// migration ratchet reached zero — keystone spec §9 gate 3); the doc-coverage
// conformance test asserts no exemptions remain.
var registrations = concatRows(
	filemod.Rows,
	cmdmod.Rows,
	pkgmod.Rows,
	usermod.Rows,
	groupmod.Rows,
	servicemod.Rows,
	cronmod.Rows,
	mountmod.Rows,
	sysctlmod.Rows,
	localemod.Rows,
	timezonemod.Rows,
	pipmod.Rows,
	gitmod.Rows,
	pkgrepomod.Rows,
	archivemod.Rows,
	hostmod.Rows,
	sshauthmod.Rows,
	testmod.Rows,
	modulemod.Rows, // module.run stays LAST
)

// concatRows flattens the per-family row slices into the single table.
func concatRows(families ...[]regdef.Registration) []regdef.Registration {
	var out []regdef.Registration
	for _, rows := range families {
		out = append(out, rows...)
	}
	return out
}

// RegisterAll registers every built-in state module into reg, threading the
// decode policy opts to the migrated (Spec-carrying) modules. Spec rows are
// RegisterSpec'd (so Describe/SpecNames/Parse see them); the rest are plain
// Register'd. A malformed registration (no builder, or a Spec whose module name
// disagrees with the row name, or a RegisterSpec failure) is a programming error
// and panics at startup.
func RegisterAll(reg *state.Registry, mctx *exec.ModuleContext, opts modschema.DecodeOptions) {
	for _, r := range registrations {
		b := buildOne(r, reg, mctx, opts)
		if r.Spec != nil {
			if r.Spec.Module != r.Name {
				panic(fmt.Sprintf("modules: registration %q has spec for module %q", r.Name, r.Spec.Module))
			}
			if err := reg.RegisterSpec(r.Spec, b); err != nil {
				panic(fmt.Sprintf("modules: register spec %s: %v", r.Name, err))
			}
			continue
		}
		reg.Register(r.Name, b)
	}
}

// buildOne resolves the single configured builder shape of a registration.
func buildOne(r regdef.Registration, reg *state.Registry, mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	n := 0
	if r.Build != nil {
		n++
	}
	if r.BuildPlain != nil {
		n++
	}
	if r.BuildWithRegistry != nil {
		n++
	}
	if n != 1 {
		panic(fmt.Sprintf("modules: registration %q must set exactly one builder shape, has %d", r.Name, n))
	}
	switch {
	case r.Build != nil:
		return r.Build(mctx, opts)
	case r.BuildPlain != nil:
		return r.BuildPlain(opts)
	default:
		return r.BuildWithRegistry(reg)
	}
}
