package main

import (
	"sort"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/execmod"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules"
)

// buildStateRegistry wires every built-in state module (spec-carrying or
// legacy) the same way the peel does (modules.RegisterAll), so docgen reads
// registration truth from the same table the runtime uses — never a
// hand-duplicated module list. Docgen's example validation (§8) actually
// invokes Registry.Build for self-contained examples, so every provider is a
// fully-functional in-memory exectest fake (never a real OS side effect) —
// the same fakes pkg/state/modules' own tests use, not a zero-value context.
func buildStateRegistry() *state.Registry {
	reg := state.NewRegistry()
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{
		Package: exectest.NewFakePackageExec("apt"),
		File:    exectest.NewFakeFileExec(),
		Command: exectest.NewFakeCommandExec(),
		Service: exectest.NewFakeServiceExec("systemd"),
		User:    exectest.NewFakeUserExec(),
		Group:   exectest.NewFakeGroupExec(),
		Cron:    exectest.NewFakeCronExec(),
		Sysctl:  exectest.NewFakeSysctlExec(),
		Mount:   exectest.NewFakeMountExec(),
	}}
	modules.RegisterAll(reg, mctx, modschema.DecodeOptions{})
	return reg
}

// stateModuleNames returns every built-in state module name, sorted.
func stateModuleNames(reg *state.Registry) []string {
	names := reg.Modules()
	sort.Strings(names)
	return names
}

// buildExecmodRegistry wires every built-in execution-module function
// (execmod.DefaultRegistry) — §1's import graph names execmod as a docgen
// dependency alongside state/modules, for the exec-kind Specs Phase 1+ waves
// will register (RegisterSpec exists today; all 16 built-in functions carry specs
// (registerBuiltinSpecs)). Registration never invokes a function, so
// no ModuleContext/providers are needed here at all.
func buildExecmodRegistry() *execmod.Registry {
	return execmod.DefaultRegistry()
}

// execmodSpecNames returns every spec-registered execmod function name, sorted.
func execmodSpecNames(reg *execmod.Registry) []string {
	names := reg.SpecNames()
	sort.Strings(names)
	return names
}
