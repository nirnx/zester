package sysctlmod

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
)

// testSysctlMctx wires the sysctl fake plus a file fake the module verifies
// the persist drop-in (exec.SysctlConfPath) from.
func testSysctlMctx(sysctl *exectest.FakeSysctlExec, file *exectest.FakeFileExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Sysctl:  sysctl,
			Package: exectest.NewFakePackageExec("apt"),
			File:    file,
			Command: exectest.NewFakeCommandExec(),
		},
	}
}

// seedSysctlConf writes the persist drop-in into the fake filesystem.
func seedSysctlConf(file *exectest.FakeFileExec, content string) {
	file.PreCreate(exec.SysctlConfPath, []byte(content), 0644)
}

func TestSysctlPresentName(t *testing.T) {
	mctx := testSysctlMctx(exectest.NewFakeSysctlExec(), exectest.NewFakeFileExec())
	s, err := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value": "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "sysctl.present:net.ipv4.ip_forward" {
		t.Errorf("Name() = %q", s.Name())
	}
}

func TestSysctlPresentDefaultKey(t *testing.T) {
	mctx := testSysctlMctx(exectest.NewFakeSysctlExec(), exectest.NewFakeFileExec())
	s, err := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value": "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	sp := s.(*SysctlPresent)
	if sp.Key != "net.ipv4.ip_forward" {
		t.Errorf("Key = %q, want net.ipv4.ip_forward", sp.Key)
	}
}

func TestSysctlPresentValueRequired(t *testing.T) {
	mctx := testSysctlMctx(exectest.NewFakeSysctlExec(), exectest.NewFakeFileExec())
	_, err := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{})
	if err == nil {
		t.Fatal("expected error when value is missing")
	}
}

func TestSysctlPresentDecodeCoercions(t *testing.T) {
	// Module-level BD activation through the builder (the decoder contract pins
	// the same across all three universes). A numeric value coerces to its
	// string form (BD-6, an ERROR->ACCEPT flip on the required param) and an
	// integer persist is 0=false (BD-7).
	mctx := testSysctlMctx(exectest.NewFakeSysctlExec(), exectest.NewFakeFileExec())
	s, err := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("vm.swappiness", map[string]any{
		"value":   10,
		"persist": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	sp := s.(*SysctlPresent)
	if sp.Value != "10" {
		t.Errorf("Value = %q, want \"10\" (BD-6 numeric coercion on the required param)", sp.Value)
	}
	if sp.Persist {
		t.Error("Persist = true, want false (BD-7 integer 0 = false)")
	}
}

func TestSysctlPresentPersistInvalidInt(t *testing.T) {
	// BD-7 rejection arm at the module level: an integer persist other than 0/1
	// is a typed value error, not a silent drop.
	mctx := testSysctlMctx(exectest.NewFakeSysctlExec(), exectest.NewFakeFileExec())
	_, err := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("vm.swappiness", map[string]any{
		"value":   "10",
		"persist": 2,
	})
	if err == nil {
		t.Fatal("expected an error for persist=2 (BD-7 only 0/1 are booleans)")
	}
}

func TestSysctlPresentCheckNeedsChange(t *testing.T) {
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "0")
	mctx := testSysctlMctx(fake, exectest.NewFakeFileExec())
	s, _ := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value": "1",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange=true when value differs")
	}
}

func TestSysctlPresentCheckNoChange(t *testing.T) {
	// persist defaults to true, so the no-change case needs BOTH the runtime
	// value and the drop-in entry in place.
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "1")
	file := exectest.NewFakeFileExec()
	seedSysctlConf(file, "net.ipv4.ip_forward = 1\n")
	mctx := testSysctlMctx(fake, file)
	s, _ := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value": "1",
	})
	cr, _ := s.Check(context.Background())
	if cr.NeedsChange {
		t.Errorf("expected NeedsChange=false when value and persist entry match, diff: %s", cr.Diff)
	}
}

func TestSysctlPresentCheckPersistMissing(t *testing.T) {
	// The finding: a manual `sysctl -w` matches at runtime, but with
	// persist:true (the default) and no drop-in entry the value dies at the
	// next reboot — that is drift, not compliance.
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "1")
	mctx := testSysctlMctx(fake, exectest.NewFakeFileExec()) // no conf file
	s, _ := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value": "1",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange=true when runtime matches but nothing is persisted")
	}
}

func TestSysctlPresentCheckPersistValueDrift(t *testing.T) {
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "1")
	file := exectest.NewFakeFileExec()
	seedSysctlConf(file, "net.ipv4.ip_forward = 0\n")
	mctx := testSysctlMctx(fake, file)
	s, _ := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value": "1",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange=true when the persisted value differs")
	}
}

func TestSysctlPresentCheckNoPersistIgnoresFile(t *testing.T) {
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "1")
	mctx := testSysctlMctx(fake, exectest.NewFakeFileExec())
	s, _ := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value":   "1",
		"persist": false,
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected NeedsChange=false with persist:false and matching runtime value")
	}
}

func TestSysctlPresentCheckPersistReadErrorFails(t *testing.T) {
	// A non-not-exist read error on the drop-in fails the phase — it is
	// never treated as "no entry".
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "1")
	file := exectest.NewFakeFileExec()
	file.SetReadError(exec.SysctlConfPath, errors.New("permission denied"))
	mctx := testSysctlMctx(fake, file)
	s, _ := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value": "1",
	})
	if _, err := s.Check(context.Background()); err == nil {
		t.Error("expected Check error when the drop-in is unreadable")
	}
	if _, err := s.Apply(context.Background()); err == nil {
		t.Error("expected Apply error when the drop-in is unreadable")
	}
}

func TestSysctlPresentApply(t *testing.T) {
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "0")
	mctx := testSysctlMctx(fake, exectest.NewFakeFileExec())
	s, _ := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value":   "1",
		"persist": true,
	})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true")
	}
	if fake.GetSync("net.ipv4.ip_forward") != "1" {
		t.Error("expected value set to 1")
	}
	if !fake.IsPersisted("net.ipv4.ip_forward") {
		t.Error("expected value to be persisted")
	}
}

func TestSysctlPresentApplyNoPersist(t *testing.T) {
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "0")
	mctx := testSysctlMctx(fake, exectest.NewFakeFileExec())
	s, _ := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value":   "1",
		"persist": false,
	})
	_, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fake.IsPersisted("net.ipv4.ip_forward") {
		t.Error("expected value NOT to be persisted when persist=false")
	}
}

func TestSysctlPresentApplyPersistsRuntimeMatch(t *testing.T) {
	// Runtime already matches but nothing is persisted: Apply must write the
	// drop-in entry (and may skip the redundant runtime Set).
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "1")
	mctx := testSysctlMctx(fake, exectest.NewFakeFileExec())
	s, _ := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value": "1",
	})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true (persist entry written)")
	}
	if !fake.IsPersisted("net.ipv4.ip_forward") {
		t.Error("expected value persisted")
	}
}

func TestSysctlPresentApplyConvergedNoOp(t *testing.T) {
	// Watch-forced Apply on a converged key: clean no-op.
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "1")
	file := exectest.NewFakeFileExec()
	seedSysctlConf(file, "net.ipv4.ip_forward = 1\n")
	mctx := testSysctlMctx(fake, file)
	s, _ := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value": "1",
	})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Errorf("expected Changed=false when converged, diff: %s", ar.Diff)
	}
}

func TestSysctlPresentRevert(t *testing.T) {
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "0")
	mctx := testSysctlMctx(fake, exectest.NewFakeFileExec())
	s, _ := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value": "1",
	})
	_, _ = s.Apply(context.Background())
	_, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fake.GetSync("net.ipv4.ip_forward") != "0" {
		t.Errorf("expected reverted to 0, got %q", fake.GetSync("net.ipv4.ip_forward"))
	}
}

func TestSysctlPresentRevertFreshInstanceNoOp(t *testing.T) {
	// A fresh instance (standalone ModeRevert) has no captured original: the
	// old code Set (and Persisted!) the empty string — kernel-parameter
	// corruption. Revert must be an explicit clean no-op.
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "1")
	mctx := testSysctlMctx(fake, exectest.NewFakeFileExec())
	s, _ := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value": "1",
	})
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected Changed=false on fresh-instance Revert")
	}
	if ar.Diff != "nothing to revert (no apply recorded in this run)" {
		t.Errorf("Diff = %q, want the explicit no-op explanation", ar.Diff)
	}
	if fake.GetSync("net.ipv4.ip_forward") != "1" {
		t.Errorf("fresh-instance Revert must not touch the value, got %q", fake.GetSync("net.ipv4.ip_forward"))
	}
	if fake.IsPersisted("net.ipv4.ip_forward") {
		t.Error("fresh-instance Revert must not persist anything")
	}
}

// testSysctlProcfsMctx wires the REAL ProcfsProvider over command/file fakes
// so the persist drop-in the provider writes is the same file the module
// verifies — full Check/Apply/Revert coherence on the persist facet.
func testSysctlProcfsMctx(cmd *exectest.FakeCommandExec, file *exectest.FakeFileExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Sysctl:  exec.NewProcfsProvider(cmd, file),
			File:    file,
			Command: cmd,
			Package: exectest.NewFakePackageExec("apt"),
		},
	}
}

// sysctlWriteCalls returns the recorded `sysctl -w key=value` argument
// strings, in order.
func sysctlWriteCalls(cmd *exectest.FakeCommandExec) []string {
	var writes []string
	for _, call := range cmd.Calls() {
		if call.Command == "sysctl" && len(call.Args) == 2 && call.Args[0] == "-w" {
			writes = append(writes, call.Args[1])
		}
	}
	return writes
}

func TestSysctlPresentRevertRemovesAddedPersistEntry(t *testing.T) {
	// Round-2 regression: a persist-only Apply (runtime already matched,
	// drop-in entry added) was "reverted" by re-persisting the same runtime
	// value — the entry Apply introduced survived. Revert must REMOVE it,
	// and must not touch the runtime facet Apply never changed.
	cmd := exectest.NewFakeCommandExec()
	cmd.SetResult("sysctl", &exec.CommandResult{Stdout: "10\n"}, nil) // runtime already 10
	file := exectest.NewFakeFileExec()
	mctx := testSysctlProcfsMctx(cmd, file)
	s, err := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("vm.swappiness", map[string]any{
		"value": "10",
	})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Fatal("expected Changed=true (persist entry was added)")
	}
	data, _ := file.GetFile(exec.SysctlConfPath)
	if !strings.Contains(string(data), "vm.swappiness = 10") {
		t.Fatalf("expected the drop-in entry after Apply, got: %q", string(data))
	}

	rr, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rr.Changed {
		t.Error("expected Changed=true on Revert (entry removed)")
	}
	data, _ = file.GetFile(exec.SysctlConfPath)
	if strings.Contains(string(data), "vm.swappiness") {
		t.Errorf("the persist entry Apply added survived the revert: %q", string(data))
	}
	if writes := sysctlWriteCalls(cmd); len(writes) != 0 {
		t.Errorf("Revert ran sysctl -w %v, but Apply never changed the runtime value", writes)
	}
}

func TestSysctlPresentRevertRestoresPriorPersistEntry(t *testing.T) {
	// A pre-existing drop-in entry is restored to its PRIOR value on revert
	// (not to the pre-Apply runtime value, which may differ).
	cmd := exectest.NewFakeCommandExec()
	cmd.SetResult("sysctl", &exec.CommandResult{Stdout: "35\n"}, nil) // runtime drifted to 35
	file := exectest.NewFakeFileExec()
	file.PreCreate(exec.SysctlConfPath, []byte("vm.swappiness = 60\n"), 0644)
	mctx := testSysctlProcfsMctx(cmd, file)
	s, err := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("vm.swappiness", map[string]any{
		"value": "10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ := file.GetFile(exec.SysctlConfPath)
	if !strings.Contains(string(data), "vm.swappiness = 10") {
		t.Fatalf("expected the drop-in updated to 10 after Apply, got: %q", string(data))
	}

	if _, err := s.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ = file.GetFile(exec.SysctlConfPath)
	content := string(data)
	if !strings.Contains(content, "vm.swappiness = 60") {
		t.Errorf("expected the PRIOR persisted value 60 restored, got: %q", content)
	}
	if strings.Count(content, "vm.swappiness") != 1 {
		t.Errorf("expected exactly one drop-in entry after revert, got: %q", content)
	}
	// The runtime facet is restored to the pre-Apply runtime value (35).
	writes := sysctlWriteCalls(cmd)
	if len(writes) != 2 || writes[0] != "vm.swappiness=10" || writes[1] != "vm.swappiness=35" {
		t.Errorf("sysctl -w calls = %v, want [vm.swappiness=10 vm.swappiness=35]", writes)
	}
}

func TestSysctlPresentConvergencePersistFacet(t *testing.T) {
	// Check → Apply → Check over the real provider: what Check compares (the
	// drop-in file) is exactly what Apply's Persist produces.
	cmd := exectest.NewFakeCommandExec()
	cmd.SetResult("sysctl", &exec.CommandResult{Stdout: "10\n"}, nil) // runtime already 10
	file := exectest.NewFakeFileExec()
	mctx := testSysctlProcfsMctx(cmd, file)
	s, err := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("vm.swappiness", map[string]any{
		"value": "10",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange=true before Apply (not persisted)")
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	cr, err = s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected converged after Apply, diff: %s", cr.Diff)
	}
}

func TestSysctlPresentNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Sysctl: nil}}
	_, err := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{"value": "1"})
	if err == nil {
		t.Fatal("expected error when sysctl provider is nil")
	}
}

func TestSysctlPresentPersistRequiresFileProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Sysctl: exectest.NewFakeSysctlExec()}}
	_, err := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{"value": "1"})
	if err == nil {
		t.Fatal("expected error when persist is enabled but no file provider is available")
	}
	// persist:false does not need the file provider.
	if _, err := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value":   "1",
		"persist": false,
	}); err != nil {
		t.Fatalf("persist:false should not require a file provider: %v", err)
	}
}

func TestSysctlPresentRequisites(t *testing.T) {
	mctx := testSysctlMctx(exectest.NewFakeSysctlExec(), exectest.NewFakeFileExec())
	s, err := NewSysctlPresentBuilder(mctx, modschema.DecodeOptions{})("net.ipv4.ip_forward", map[string]any{
		"value":   "1",
		"require": []any{"pkg.installed:procps"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:procps" {
		t.Errorf("Require = %v", reqs.Require)
	}
}
