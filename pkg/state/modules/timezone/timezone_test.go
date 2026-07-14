package timezonemod

import (
	"context"
	"errors"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

func testTZMctx(fakeCmd *exectest.FakeCommandExec, fakeFile *exectest.FakeFileExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Command: fakeCmd,
			File:    fakeFile,
			Package: exectest.NewFakePackageExec("apt"),
		},
	}
}

func TestTimezoneSystemName(t *testing.T) {
	mctx := testTZMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	builder := NewTimezoneSystemBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("America/New_York", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "timezone.system:America/New_York" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestTimezoneSystemPrimaryParamDefault(t *testing.T) {
	mctx := testTZMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	builder := NewTimezoneSystemBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("UTC", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	tz := s.(*TimezoneSystem)
	if tz.Timezone != "UTC" {
		t.Errorf("Timezone: got %q, want UTC", tz.Timezone)
	}
}

func TestTimezoneSystemRequisites(t *testing.T) {
	mctx := testTZMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	builder := NewTimezoneSystemBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("UTC", map[string]any{
		"require": []any{"pkg.installed:tzdata"},
		"onfail":  []any{"cmd.run:fallback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:tzdata" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:fallback" {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}

func TestTimezoneSystemCheckNoChange(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("timedatectl", &exec.CommandResult{
		Stdout:   "America/New_York\n",
		ExitCode: 0,
	}, nil)
	mctx := testTZMctx(fakeCmd, exectest.NewFakeFileExec())
	builder := NewTimezoneSystemBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("America/New_York", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when timezone matches")
	}
}

func TestTimezoneSystemCheckNeedsChange(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("timedatectl", &exec.CommandResult{
		Stdout:   "UTC\n",
		ExitCode: 0,
	}, nil)
	mctx := testTZMctx(fakeCmd, exectest.NewFakeFileExec())
	builder := NewTimezoneSystemBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("America/New_York", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when timezone differs")
	}
}

func TestTimezoneSystemCheckFallbackFile(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetError("timedatectl", errors.New("not found"))
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/timezone", []byte("UTC\n"), 0644)

	mctx := testTZMctx(fakeCmd, fakeFile)
	builder := NewTimezoneSystemBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("UTC", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when file timezone matches")
	}
}

func TestTimezoneSystemApply(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	// First call (Check inside Apply for previousTZ): timedatectl show
	fakeCmd.SetResult("timedatectl", &exec.CommandResult{Stdout: "UTC\n", ExitCode: 0}, nil)
	mctx := testTZMctx(fakeCmd, exectest.NewFakeFileExec())
	builder := NewTimezoneSystemBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("America/New_York", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after set-timezone")
	}
	if ar.Details["timezone"] != "America/New_York" {
		t.Errorf("timezone detail: got %q", ar.Details["timezone"])
	}
}

func TestTimezoneSystemApplyError(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetError("timedatectl", errors.New("permission denied"))
	fakeFile := exectest.NewFakeFileExec()
	// Also make dpkg-reconfigure fail so Apply returns an error.
	fakeCmd.SetError("dpkg-reconfigure", errors.New("not found"))

	mctx := testTZMctx(fakeCmd, fakeFile)
	builder := NewTimezoneSystemBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("America/New_York", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error when both timedatectl and dpkg-reconfigure fail")
	}
}

func TestTimezoneSystemRevert(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("timedatectl", &exec.CommandResult{Stdout: "UTC\n", ExitCode: 0}, nil)
	mctx := testTZMctx(fakeCmd, exectest.NewFakeFileExec())
	builder := NewTimezoneSystemBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("America/New_York", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	// Apply to capture previousTZ.
	if _, err2 := s.Apply(context.Background()); err2 != nil {
		t.Fatal(err2)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after revert")
	}
}

func TestTimezoneSystemRevert_NoPreviousTZ(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testTZMctx(fakeCmd, exectest.NewFakeFileExec())
	builder := NewTimezoneSystemBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("America/New_York", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected no change when previous TZ is unknown")
	}
}

func TestTimezoneSystemNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File: exectest.NewFakeFileExec(),
		},
	}
	builder := NewTimezoneSystemBuilder(mctx, modschema.DecodeOptions{})
	_, err := builder("UTC", map[string]any{})
	if err == nil {
		t.Error("expected error when no command provider is set")
	}
}

var _ state.State = (*TimezoneSystem)(nil)

// TestTimezoneSystemContract replays the permanent differential contract
// fixtures against the migrated timezone.system decoder. The cases were
// approved by the legacy-vs-new equivalence comparison while the legacy
// constructor still existed (see the migration changelog); after its deletion
// this replay is the permanent regression guard for timezone.system's decode
// behavior, including the flagged BD-2/BD-6/BD-7 divergences.
func TestTimezoneSystemContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var tz TimezoneSystem
		if _, err := timezoneSystemSpec.Decode(id, config, &tz, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &tz, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/timezone.system.yaml")
}
