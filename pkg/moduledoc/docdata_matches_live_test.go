package moduledoc_test

// TestDocdataMatchesLive pins the offline path (keystone spec §7): `zester
// doc`'s embedded pkg/moduledoc projection must render IDENTICALLY to a live
// Spec.Info() from the real, connected module registry, for every module
// docdata.json carries. This is what makes the embed trustworthy — a stale
// docdata.json (regenerate forgotten after editing a module's Doc) fails this
// test rather than silently drifting from the live registry.
//
// This file lives in the external moduledoc_test package specifically so it
// can import pkg/exec, pkg/exec/exectest, and pkg/state/modules to build the
// live registry — moduledoc's own (non-test) code stays a leaf importing only
// modschema (§1); a _test.go import never reaches the production binary.

import (
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/moduledoc"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules"
)

func liveRegistry() *state.Registry {
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

func TestDocdataMatchesLive(t *testing.T) {
	reg := liveRegistry()
	embedded := moduledoc.All()
	if len(embedded) == 0 {
		t.Fatal("moduledoc.All() is empty — docdata.json was never generated")
	}

	for _, want := range embedded {
		live, ok := reg.Describe(want.Module)
		if !ok {
			t.Errorf("docdata carries %q but the live registry has no spec for it (stale embed?)", want.Module)
			continue
		}
		gotText := modschema.RenderText(live)
		wantText := modschema.RenderText(want)
		if gotText != wantText {
			t.Errorf("module %s: embedded docdata renders differently from the live registry\n--- live ---\n%s\n--- embedded ---\n%s",
				want.Module, gotText, wantText)
		}
	}

	// The converse: every live spec-registered module must be represented in
	// docdata.json too — nothing spec-registered may be silently absent from
	// the embed (docgen forgot to regenerate, or hand-edited the file).
	for _, name := range reg.SpecNames() {
		if _, ok := moduledoc.Lookup(name); !ok {
			t.Errorf("live registry has a spec for %q but docdata.json has no entry for it", name)
		}
	}
}
