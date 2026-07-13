package modules_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state/modules"
)

// testSvcMctx builds a ModuleContext wired with the given FakeServiceExec.
func testSvcMctx(fakeSvc *exectest.FakeServiceExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Service: fakeSvc,
			Package: exectest.NewFakePackageExec("apt"),
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
	}
}

// --- service.running ---

func TestSvcRunning_Name(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	mctx := testSvcMctx(fake)
	s, err := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "service.running:nginx" {
		t.Errorf("Name() = %q, want %q", s.Name(), "service.running:nginx")
	}
}

func TestSvcRunning_PrimaryParamDefault(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	mctx := testSvcMctx(fake)
	s, err := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("myservice", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	sr := s.(*modules.SvcRunning)
	if sr.Service != "myservice" {
		t.Errorf("Service = %q, want %q", sr.Service, "myservice")
	}
}

func TestSvcRunning_Requisites(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	mctx := testSvcMctx(fake)
	s, err := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{
		"require":   []any{"pkg.installed:nginx"},
		"watch":     []any{"file.managed:/etc/nginx/nginx.conf"},
		"onchanges": []any{"cmd.run:build"},
		"onfail":    []any{"cmd.run:primary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:nginx" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/nginx/nginx.conf" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:build" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:primary" {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}

func TestSvcRunning_CheckNeedsChange(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsChange {
		t.Error("expected NeedsChange=true when service is not running")
	}
}

func TestSvcRunning_CheckNoChange(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.NeedsChange {
		t.Error("expected NeedsChange=false when service is running")
	}
}

func TestSvcRunning_ApplyStart(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	result, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("expected Changed=true")
	}
	if !fake.IsRunningSync("nginx") {
		t.Error("expected nginx to be running after Apply")
	}
}

func TestSvcRunning_ApplyRestart(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	result, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("expected Changed=true")
	}
	if result.Details["action"] != "restarted" {
		t.Errorf("action = %q, want %q", result.Details["action"], "restarted")
	}
}

func TestSvcRunning_ApplyEnable(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{"enable": true})
	_, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !fake.IsEnabledSync("nginx") {
		t.Error("expected nginx to be enabled after Apply with enable:true")
	}
}

func TestSvcRunning_Revert(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	result, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("expected Changed=true on Revert")
	}
	if fake.IsRunningSync("nginx") {
		t.Error("expected nginx to be stopped after Revert")
	}
}

func TestSvcRunning_CheckEnableDrift(t *testing.T) {
	// Running but disabled at boot with enable:true declared — the classic
	// RHEL case: reported compliant before, so the unit never got enabled
	// and the service stayed down after every reboot.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{"enable": true})
	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsChange {
		t.Error("expected NeedsChange=true when running but not enabled with enable:true")
	}
}

func TestSvcRunning_CheckEnableUndeclaredNoChurn(t *testing.T) {
	// No enable declared: the enable facet must not be compared at all.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.NeedsChange {
		t.Error("expected NeedsChange=false when enable is undeclared")
	}
}

func TestSvcRunning_CheckEnableFalseDrift(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{"enable": false})
	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsChange {
		t.Error("expected NeedsChange=true when enabled but enable:false declared")
	}
}

func TestSvcRunning_CheckEnableSatisfiedNoChange(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{"enable": true})
	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.NeedsChange {
		t.Error("expected NeedsChange=false when running and enabled")
	}
}

func TestSvcRunning_ApplyEnableOnlyDoesNotRestart(t *testing.T) {
	// Apply reached because only the enable facet drifted: it must enable
	// the unit without restarting the (healthy, running) service.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{"enable": true})
	result, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("expected Changed=true")
	}
	if result.Details["action"] != "enabled" {
		t.Errorf("action = %q, want %q (no restart for enable-only drift)", result.Details["action"], "enabled")
	}
	if !fake.IsEnabledSync("nginx") {
		t.Error("expected nginx enabled after Apply")
	}
}

func TestSvcRunning_ApplyDisableWhenEnableFalse(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{"enable": false})
	result, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("expected Changed=true")
	}
	if fake.IsEnabledSync("nginx") {
		t.Error("expected nginx disabled after Apply with enable:false")
	}
	if !fake.IsRunningSync("nginx") {
		t.Error("expected nginx still running")
	}
}

func TestSvcRunning_RevertFreshInstanceNoOp(t *testing.T) {
	// A fresh instance (standalone ModeRevert) has no apply memo: Revert
	// must be an explicit clean no-op, never touching the service.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{"enable": true})
	result, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Error("expected Changed=false on fresh-instance Revert")
	}
	if result.Diff != "nothing to revert (no apply recorded in this run)" {
		t.Errorf("Diff = %q, want the explicit no-op explanation", result.Diff)
	}
	if !fake.IsRunningSync("nginx") || !fake.IsEnabledSync("nginx") {
		t.Error("fresh-instance Revert must not touch the service")
	}
}

func TestSvcRunning_RevertEnableOnlyReportsChanged(t *testing.T) {
	// Apply that only enabled (service already running) must revert the
	// enable and report Changed=true — not the old lying Changed=false.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{"enable": true})
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("expected Changed=true when Revert disabled the service")
	}
	if fake.IsEnabledSync("nginx") {
		t.Error("expected nginx disabled again after Revert")
	}
}

func TestSvcRunning_NilProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Service: nil}}
	_, err := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	if err == nil {
		t.Error("expected error when Service provider is nil")
	}
}

func TestSvcRunning_ApplyError(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, false)
	fake.StartErr = errors.New("systemctl failed")
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	_, err := s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from Apply when Start fails")
	}
}

// --- service.enabled ---

func TestSvcEnabled_Name(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	mctx := testSvcMctx(fake)
	s, err := modules.NewSvcEnabledBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "service.enabled:nginx" {
		t.Errorf("Name() = %q, want %q", s.Name(), "service.enabled:nginx")
	}
}

func TestSvcEnabled_PrimaryParamDefault(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	mctx := testSvcMctx(fake)
	s, err := modules.NewSvcEnabledBuilder(mctx, modschema.DecodeOptions{})("sshd", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	se := s.(*modules.SvcEnabled)
	if se.Service != "sshd" {
		t.Errorf("Service = %q, want %q", se.Service, "sshd")
	}
}

func TestSvcEnabled_CheckNeedsChange(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcEnabledBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsChange {
		t.Error("expected NeedsChange=true when service is not enabled")
	}
}

func TestSvcEnabled_CheckNoChange(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcEnabledBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.NeedsChange {
		t.Error("expected NeedsChange=false when service is enabled")
	}
}

func TestSvcEnabled_Apply(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcEnabledBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	result, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("expected Changed=true")
	}
	if !fake.IsEnabledSync("nginx") {
		t.Error("expected nginx to be enabled after Apply")
	}
}

func TestSvcEnabled_Revert(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcEnabledBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	result, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("expected Changed=true on Revert")
	}
	if fake.IsEnabledSync("nginx") {
		t.Error("expected nginx to be disabled after Revert")
	}
}

func TestSvcEnabled_ApplyAlreadyEnabledNoOp(t *testing.T) {
	// Watch-forced Apply on an already-enabled service: clean no-op, and the
	// Enable call must not even be attempted.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, true)
	fake.EnableErr = errors.New("Enable must not be called when already enabled")
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcEnabledBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	result, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Errorf("expected Changed=false when already enabled, diff: %s", result.Diff)
	}
}

func TestSvcEnabled_RevertFreshInstanceNoOp(t *testing.T) {
	// A fresh instance (standalone ModeRevert) never enabled anything this
	// run: Revert must NOT disable the service — the old code unconditionally
	// disabled sshd-class services at boot.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("sshd", true, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcEnabledBuilder(mctx, modschema.DecodeOptions{})("sshd", map[string]any{})
	result, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Error("expected Changed=false on fresh-instance Revert")
	}
	if result.Diff != "nothing to revert (no apply recorded in this run)" {
		t.Errorf("Diff = %q, want the explicit no-op explanation", result.Diff)
	}
	if !fake.IsEnabledSync("sshd") {
		t.Error("fresh-instance Revert must not disable the service")
	}
}

func TestSvcEnabled_RevertAfterNoOpApplyNoOp(t *testing.T) {
	// Same-instance: a converged Apply (Changed=false) arms no memo, so
	// Revert stays a no-op instead of disabling the service.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcEnabledBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Fatal("expected converged Apply to be a no-op")
	}
	rr, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rr.Changed {
		t.Error("expected Changed=false on Revert after a no-op Apply")
	}
	if !fake.IsEnabledSync("nginx") {
		t.Error("Revert after a no-op Apply must not disable the service")
	}
}

func TestSvcEnabled_Convergence(t *testing.T) {
	// Check → Apply → Check must report converged.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcEnabledBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange=true before Apply")
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

func TestSvcEnabled_NilProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Service: nil}}
	_, err := modules.NewSvcEnabledBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	if err == nil {
		t.Error("expected error when Service provider is nil")
	}
}

func TestSvcEnabled_ApplyError(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.EnableErr = errors.New("dbus error")
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcEnabledBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	_, err := s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from Apply when Enable fails")
	}
}

// --- service.dead ---

func TestSvcDead_Name(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	mctx := testSvcMctx(fake)
	s, err := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "service.dead:nginx" {
		t.Errorf("Name() = %q, want %q", s.Name(), "service.dead:nginx")
	}
}

func TestSvcDead_PrimaryParamDefault(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	mctx := testSvcMctx(fake)
	s, err := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("mysql", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	sd := s.(*modules.SvcDead)
	if sd.Service != "mysql" {
		t.Errorf("Service = %q, want %q", sd.Service, "mysql")
	}
}

func TestSvcDead_CheckNeedsChange(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsChange {
		t.Error("expected NeedsChange=true when service is running")
	}
}

func TestSvcDead_CheckNoChange(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.NeedsChange {
		t.Error("expected NeedsChange=false when service is not running")
	}
}

func TestSvcDead_ApplyStop(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	result, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("expected Changed=true")
	}
	if fake.IsRunningSync("nginx") {
		t.Error("expected nginx to be stopped after Apply")
	}
	// Default: does not disable
	if !fake.IsEnabledSync("nginx") {
		t.Error("expected nginx to remain enabled (enable not set to false)")
	}
}

func TestSvcDead_ApplyDisable(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{"enable": false})
	_, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fake.IsEnabledSync("nginx") {
		t.Error("expected nginx to be disabled after Apply with enable:false")
	}
}

func TestSvcDead_Revert(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	result, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("expected Changed=true on Revert")
	}
	if !fake.IsRunningSync("nginx") {
		t.Error("expected nginx to be running after Revert")
	}
}

func TestSvcDead_CheckDisableDrift(t *testing.T) {
	// Stopped but still enabled at boot with enable:false declared: the unit
	// resurrects at the next reboot — must be flagged as drift, not compliant.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{"enable": false})
	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsChange {
		t.Error("expected NeedsChange=true when stopped-but-enabled with enable:false")
	}
}

func TestSvcDead_CheckStoppedEnableUndeclaredNoChurn(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	result, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.NeedsChange {
		t.Error("expected NeedsChange=false when stopped and enable undeclared")
	}
}

func TestSvcDead_ApplyAlreadyStoppedDisables(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{"enable": false})
	result, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("expected Changed=true (disable performed)")
	}
	if fake.IsEnabledSync("nginx") {
		t.Error("expected nginx disabled")
	}
}

func TestSvcDead_ApplyConvergedNoOp(t *testing.T) {
	// Watch-forced Apply on an already stopped+disabled service: clean no-op.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{"enable": false})
	result, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Error("expected Changed=false when already converged")
	}
}

func TestSvcDead_RevertFreshInstanceNoOp(t *testing.T) {
	// A fresh instance (standalone ModeRevert) has no apply memo: Revert must
	// NOT start the very service this state declares dead — the old code
	// unconditionally Started it with a lying "started X (revert stop)" diff.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("telemetry-agent", false, false) // stopped weeks ago, not by this run
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("telemetry-agent", map[string]any{})
	result, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Error("expected Changed=false on fresh-instance Revert")
	}
	if result.Diff != "nothing to revert (no apply recorded in this run)" {
		t.Errorf("Diff = %q, want the explicit no-op explanation", result.Diff)
	}
	if fake.IsRunningSync("telemetry-agent") {
		t.Error("fresh-instance Revert must not start the service")
	}
}

func TestSvcDead_RevertAfterNoOpApplyNoOp(t *testing.T) {
	// Same-instance: Apply on an already-stopped service is a no-op and arms
	// no memo — Revert must not start it.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, false)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Fatal("expected converged Apply to be a no-op")
	}
	rr, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rr.Changed {
		t.Error("expected Changed=false on Revert after a no-op Apply")
	}
	if fake.IsRunningSync("nginx") {
		t.Error("Revert after a no-op Apply must not start the service")
	}
}

func TestSvcDead_RevertRestoresOnlyAppliedActs(t *testing.T) {
	// Apply only disabled (service was already stopped): Revert re-enables
	// but must NOT start the service — and the diff reflects only that.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{"enable": false})
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Error("expected Changed=true (disable was reverted)")
	}
	if !fake.IsEnabledSync("nginx") {
		t.Error("expected nginx re-enabled after Revert")
	}
	if fake.IsRunningSync("nginx") {
		t.Error("Revert must not start a service this Apply never stopped")
	}
}

func TestSvcDead_Convergence(t *testing.T) {
	// Check → Apply → Check must report converged for both facets.
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, true)
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{"enable": false})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange=true before Apply")
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

func TestSvcDead_NilProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Service: nil}}
	_, err := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	if err == nil {
		t.Error("expected error when Service provider is nil")
	}
}

func TestSvcDead_ApplyError(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, false)
	fake.StopErr = errors.New("cannot stop")
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx, modschema.DecodeOptions{})("nginx", map[string]any{})
	_, err := s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from Apply when Stop fails")
	}
}
