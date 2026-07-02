package modules

import (
	"context"
	"testing"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/exec/exectest"
)

func testSysctlMctx(sysctl *exectest.FakeSysctlExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Sysctl:  sysctl,
			Package: exectest.NewFakePackageExec("apt"),
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
	}
}

func TestSysctlPresentName(t *testing.T) {
	mctx := testSysctlMctx(exectest.NewFakeSysctlExec())
	s, err := NewSysctlPresentBuilder(mctx)("net.ipv4.ip_forward", map[string]any{
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
	mctx := testSysctlMctx(exectest.NewFakeSysctlExec())
	s, err := NewSysctlPresentBuilder(mctx)("net.ipv4.ip_forward", map[string]any{
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
	mctx := testSysctlMctx(exectest.NewFakeSysctlExec())
	_, err := NewSysctlPresentBuilder(mctx)("net.ipv4.ip_forward", map[string]any{})
	if err == nil {
		t.Fatal("expected error when value is missing")
	}
}

func TestSysctlPresentCheckNeedsChange(t *testing.T) {
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "0")
	mctx := testSysctlMctx(fake)
	s, _ := NewSysctlPresentBuilder(mctx)("net.ipv4.ip_forward", map[string]any{
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
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "1")
	mctx := testSysctlMctx(fake)
	s, _ := NewSysctlPresentBuilder(mctx)("net.ipv4.ip_forward", map[string]any{
		"value": "1",
	})
	cr, _ := s.Check(context.Background())
	if cr.NeedsChange {
		t.Error("expected NeedsChange=false when value matches")
	}
}

func TestSysctlPresentApply(t *testing.T) {
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "0")
	mctx := testSysctlMctx(fake)
	s, _ := NewSysctlPresentBuilder(mctx)("net.ipv4.ip_forward", map[string]any{
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
	mctx := testSysctlMctx(fake)
	s, _ := NewSysctlPresentBuilder(mctx)("net.ipv4.ip_forward", map[string]any{
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

func TestSysctlPresentRevert(t *testing.T) {
	fake := exectest.NewFakeSysctlExec()
	fake.PreSet("net.ipv4.ip_forward", "0")
	mctx := testSysctlMctx(fake)
	s, _ := NewSysctlPresentBuilder(mctx)("net.ipv4.ip_forward", map[string]any{
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

func TestSysctlPresentNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Sysctl: nil}}
	_, err := NewSysctlPresentBuilder(mctx)("net.ipv4.ip_forward", map[string]any{"value": "1"})
	if err == nil {
		t.Fatal("expected error when sysctl provider is nil")
	}
}

func TestSysctlPresentRequisites(t *testing.T) {
	mctx := testSysctlMctx(exectest.NewFakeSysctlExec())
	s, err := NewSysctlPresentBuilder(mctx)("net.ipv4.ip_forward", map[string]any{
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
