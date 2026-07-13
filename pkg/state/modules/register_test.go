package modules

import (
	"reflect"
	"sort"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// wantModuleNames is the pinned, sorted set of every built-in state module. A
// module added or removed without updating this list fails the pin.
var wantModuleNames = []string{
	"archive.extracted",
	"cmd.run",
	"cron.absent",
	"cron.present",
	"file.absent",
	"file.append",
	"file.blockreplace",
	"file.comment",
	"file.copy",
	"file.directory",
	"file.keyvalue",
	"file.line",
	"file.managed",
	"file.recurse",
	"file.replace",
	"file.symlink",
	"file.touch",
	"file.uncomment",
	"git.cloned",
	"git.latest",
	"group.absent",
	"group.present",
	"host.absent",
	"host.present",
	"locale.present",
	"module.run",
	"mount.mounted",
	"pip.installed",
	"pkg.installed",
	"pkg.latest",
	"pkg.purged",
	"pkg.removed",
	"pkgrepo.managed",
	"service.dead",
	"service.enabled",
	"service.running",
	"ssh_auth.absent",
	"ssh_auth.present",
	"sysctl.present",
	"test.configurable_test_state",
	"test.fail_without_changes",
	"test.nop",
	"test.ping",
	"test.succeed_with_changes",
	"timezone.system",
	"user.absent",
	"user.present",
}

func TestRegistrations_PinnedSortedNames(t *testing.T) {
	if len(registrations) != 47 {
		t.Fatalf("registration count = %d, want 47", len(registrations))
	}

	names := make([]string, len(registrations))
	for i, r := range registrations {
		names[i] = r.Name
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(sorted, wantModuleNames) {
		t.Errorf("sorted module names mismatch:\n got %v\nwant %v", sorted, wantModuleNames)
	}
}

func TestRegistrations_ModuleRunLast(t *testing.T) {
	last := registrations[len(registrations)-1]
	if last.Name != "module.run" {
		t.Errorf("last registration = %q, want module.run", last.Name)
	}
	if last.BuildWithRegistry == nil {
		t.Error("module.run must use the BuildWithRegistry shape")
	}
}

func TestRegistrations_ExactlyOneBuilderShape(t *testing.T) {
	for _, r := range registrations {
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
			t.Errorf("registration %q sets %d builder shapes, want exactly 1", r.Name, n)
		}
	}
}

func TestRegistrations_SpecCarryingRows(t *testing.T) {
	// The migration ratchet reached ZERO (keystone spec §9 gate 3): EVERY
	// registration now carries a Spec, so the spec-carrying set is the full
	// registration table in registration order. The FINAL gate-close wave added
	// cmd.run (command primary; args StringList, env StringMap) after file.append
	// and service.enabled (name primary only) after service.dead. This pins the
	// exact registration order (file.managed leads, module.run last) and that no
	// row lost its Spec.
	var withSpec []string
	for _, r := range registrations {
		if r.Spec != nil {
			withSpec = append(withSpec, r.Name)
			if r.Spec.Module != r.Name {
				t.Errorf("registration %q carries a spec for module %q", r.Name, r.Spec.Module)
			}
		}
	}
	want := []string{
		"file.managed", "file.directory", "file.absent", "file.append", "cmd.run",
		"pkg.installed", "user.present", "user.absent", "group.present", "group.absent",
		"file.symlink", "file.blockreplace", "file.recurse", "pkg.removed", "service.running",
		"service.dead", "service.enabled", "cron.present", "cron.absent", "mount.mounted",
		"sysctl.present", "locale.present", "timezone.system", "pip.installed", "git.cloned",
		"git.latest", "file.line", "file.replace", "file.comment", "file.uncomment",
		"file.keyvalue", "file.copy", "file.touch", "pkg.latest", "pkg.purged",
		"pkgrepo.managed", "archive.extracted", "host.present", "host.absent", "ssh_auth.present",
		"ssh_auth.absent",
		// the FINAL test.*/module.run wave (registration order; module.run last)
		"test.ping", "test.nop", "test.fail_without_changes", "test.succeed_with_changes",
		"test.configurable_test_state", "module.run",
	}
	if !reflect.DeepEqual(withSpec, want) {
		t.Errorf("spec-carrying rows = %v, want %v", withSpec, want)
	}
	// Gate-close pin: every registered module carries a Spec — no exemptions.
	if len(withSpec) != len(registrations) {
		t.Errorf("spec-carrying rows = %d, want all %d registrations (ratchet is at ZERO)",
			len(withSpec), len(registrations))
	}
}

func TestRegisterAll_WiresBuildersAndSpec(t *testing.T) {
	reg := state.NewRegistry()
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{Package: exectest.NewFakePackageExec("apt")},
	}
	RegisterAll(reg, mctx, modschema.DecodeOptions{})

	// Every module is build-registered.
	got := reg.Modules()
	sort.Strings(got)
	if !reflect.DeepEqual(got, wantModuleNames) {
		t.Errorf("registered modules mismatch:\n got %v\nwant %v", got, wantModuleNames)
	}

	// The migration ratchet reached ZERO: EVERY built-in state module is
	// spec-registered, so SpecNames() (sorted) is the full module set — identical
	// to wantModuleNames.
	if names := reg.SpecNames(); !reflect.DeepEqual(names, wantModuleNames) {
		t.Errorf("SpecNames = %v, want %v", names, wantModuleNames)
	}
	mi, ok := reg.Describe("pkg.removed")
	if !ok || mi.Module != "pkg.removed" || mi.Kind != modschema.KindState {
		t.Errorf("Describe(pkg.removed) = %+v ok=%v", mi, ok)
	}
	if mi.Doc.Summary == "" {
		t.Error("Describe(pkg.removed) missing Doc.Summary")
	}
	// The service pilot modules describe through the same path.
	for _, name := range []string{"service.running", "service.dead"} {
		smi, ok := reg.Describe(name)
		if !ok || smi.Module != name || smi.Kind != modschema.KindState {
			t.Errorf("Describe(%s) = %+v ok=%v", name, smi, ok)
		}
		if len(smi.SemTypes) != 1 || smi.SemTypes[0].Name != "TriState" {
			t.Errorf("Describe(%s) SemTypes = %+v, want one TriState", name, smi.SemTypes)
		}
	}
	// The gate-close migrated cmd.run and service.enabled too, so they now
	// describe through the spec path; an unknown module still does not.
	if cmi, ok := reg.Describe("cmd.run"); !ok || cmi.Module != "cmd.run" || cmi.Kind != modschema.KindState {
		t.Errorf("Describe(cmd.run) = %+v ok=%v, want a state spec", cmi, ok)
	}
	if _, ok := reg.Describe("no.such_module"); ok {
		t.Error("Describe(no.such_module) should be false")
	}

	// Parse executes the migrated plan.
	rep, err := reg.Parse("pkg.removed", "nginx", map[string]any{})
	if err != nil {
		t.Fatalf("Parse(pkg.removed): %v", err)
	}
	if rep == nil {
		t.Fatal("nil parse report")
	}

	// The built state works end-to-end through the spec-decoding builder.
	st, err := reg.Build("pkg.removed", "nginx", map[string]any{})
	if err != nil {
		t.Fatalf("Build(pkg.removed): %v", err)
	}
	if st.Name() != "pkg.removed:nginx" {
		t.Errorf("built state Name = %q", st.Name())
	}
}
