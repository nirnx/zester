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
	// The migrated set as of the file.managed wave: pilot #1 (pkg.removed), the
	// semantic-type pilot (service.running + service.dead on TriState),
	// pkg.installed/pkg.latest/pkg.purged (all primitives, no semantic types),
	// user.present (gid on GroupRef, groups/optional_groups on StringList, a
	// sensitive password), and file.managed (template on TemplateFlag, mode on a
	// lazy FileMode — the BD-1 flagship). This pins that no accidental extra Spec
	// has been attached before its own tranche; the order is registration order,
	// so file.managed (the first registration row) leads.
	var withSpec []string
	for _, r := range registrations {
		if r.Spec != nil {
			withSpec = append(withSpec, r.Name)
			if r.Spec.Module != r.Name {
				t.Errorf("registration %q carries a spec for module %q", r.Name, r.Spec.Module)
			}
		}
	}
	want := []string{"file.managed", "pkg.installed", "user.present", "pkg.removed", "service.running", "service.dead", "pkg.latest", "pkg.purged"}
	if !reflect.DeepEqual(withSpec, want) {
		t.Errorf("spec-carrying rows = %v, want %v", withSpec, want)
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

	// The migrated set (0C pilot + 0E semantic-type pilot + the all-primitives
	// pkg-family wave + user.present + file.managed) is spec-registered;
	// SpecNames is sorted.
	wantSpecNames := []string{"file.managed", "pkg.installed", "pkg.latest", "pkg.purged", "pkg.removed", "service.dead", "service.running", "user.present"}
	if names := reg.SpecNames(); !reflect.DeepEqual(names, wantSpecNames) {
		t.Errorf("SpecNames = %v, want %v", names, wantSpecNames)
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
	// A legacy module has no spec description.
	if _, ok := reg.Describe("file.directory"); ok {
		t.Error("file.directory should not be spec-described")
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
