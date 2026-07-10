package modules_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
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
	s, err := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{})
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
	s, err := modules.NewSvcRunningBuilder(mctx)("myservice", map[string]any{})
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
	s, err := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{
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
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{})
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
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{})
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
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{})
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
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{})
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
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{"enable": true})
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
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{})
	s.Apply(context.Background())
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
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{"enable": true})
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
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{})
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
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{"enable": false})
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
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{"enable": true})
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
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{"enable": true})
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
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{"enable": false})
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
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{"enable": true})
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
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{"enable": true})
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
	_, err := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{})
	if err == nil {
		t.Error("expected error when Service provider is nil")
	}
}

func TestSvcRunning_ApplyError(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", false, false)
	fake.StartErr = errors.New("systemctl failed")
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcRunningBuilder(mctx)("nginx", map[string]any{})
	_, err := s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from Apply when Start fails")
	}
}

// --- service.enabled ---

func TestSvcEnabled_Name(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	mctx := testSvcMctx(fake)
	s, err := modules.NewSvcEnabledBuilder(mctx)("nginx", map[string]any{})
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
	s, err := modules.NewSvcEnabledBuilder(mctx)("sshd", map[string]any{})
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
	s, _ := modules.NewSvcEnabledBuilder(mctx)("nginx", map[string]any{})
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
	s, _ := modules.NewSvcEnabledBuilder(mctx)("nginx", map[string]any{})
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
	s, _ := modules.NewSvcEnabledBuilder(mctx)("nginx", map[string]any{})
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
	s, _ := modules.NewSvcEnabledBuilder(mctx)("nginx", map[string]any{})
	s.Apply(context.Background())
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

func TestSvcEnabled_NilProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Service: nil}}
	_, err := modules.NewSvcEnabledBuilder(mctx)("nginx", map[string]any{})
	if err == nil {
		t.Error("expected error when Service provider is nil")
	}
}

func TestSvcEnabled_ApplyError(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.EnableErr = errors.New("dbus error")
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcEnabledBuilder(mctx)("nginx", map[string]any{})
	_, err := s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from Apply when Enable fails")
	}
}

// --- service.dead ---

func TestSvcDead_Name(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	mctx := testSvcMctx(fake)
	s, err := modules.NewSvcDeadBuilder(mctx)("nginx", map[string]any{})
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
	s, err := modules.NewSvcDeadBuilder(mctx)("mysql", map[string]any{})
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
	s, _ := modules.NewSvcDeadBuilder(mctx)("nginx", map[string]any{})
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
	s, _ := modules.NewSvcDeadBuilder(mctx)("nginx", map[string]any{})
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
	s, _ := modules.NewSvcDeadBuilder(mctx)("nginx", map[string]any{})
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
	s, _ := modules.NewSvcDeadBuilder(mctx)("nginx", map[string]any{"enable": false})
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
	s, _ := modules.NewSvcDeadBuilder(mctx)("nginx", map[string]any{})
	s.Apply(context.Background())
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
	s, _ := modules.NewSvcDeadBuilder(mctx)("nginx", map[string]any{"enable": false})
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
	s, _ := modules.NewSvcDeadBuilder(mctx)("nginx", map[string]any{})
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
	s, _ := modules.NewSvcDeadBuilder(mctx)("nginx", map[string]any{"enable": false})
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
	s, _ := modules.NewSvcDeadBuilder(mctx)("nginx", map[string]any{"enable": false})
	result, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Error("expected Changed=false when already converged")
	}
}

func TestSvcDead_NilProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Service: nil}}
	_, err := modules.NewSvcDeadBuilder(mctx)("nginx", map[string]any{})
	if err == nil {
		t.Error("expected error when Service provider is nil")
	}
}

func TestSvcDead_ApplyError(t *testing.T) {
	fake := exectest.NewFakeServiceExec("systemd")
	fake.PreAdd("nginx", true, false)
	fake.StopErr = errors.New("cannot stop")
	mctx := testSvcMctx(fake)
	s, _ := modules.NewSvcDeadBuilder(mctx)("nginx", map[string]any{})
	_, err := s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from Apply when Stop fails")
	}
}
