package modules

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

func testLocaleMctx(fakeCmd *exectest.FakeCommandExec, fakeFile *exectest.FakeFileExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Command: fakeCmd,
			File:    fakeFile,
			Package: exectest.NewFakePackageExec("apt"),
		},
	}
}

func TestLocalePresentName(t *testing.T) {
	mctx := testLocaleMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	builder := NewLocalePresentBuilder(mctx)
	s, err := builder("en_US.UTF-8", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "locale.present:en_US.UTF-8" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestLocalePresentPrimaryParamDefault(t *testing.T) {
	mctx := testLocaleMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	builder := NewLocalePresentBuilder(mctx)
	s, err := builder("en_US.UTF-8", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	l := s.(*LocalePresent)
	if l.Locale != "en_US.UTF-8" {
		t.Errorf("Locale: got %q, want en_US.UTF-8", l.Locale)
	}
}

func TestLocalePresentRequisites(t *testing.T) {
	mctx := testLocaleMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	builder := NewLocalePresentBuilder(mctx)
	s, err := builder("en_US.UTF-8", map[string]any{
		"require": []any{"pkg.installed:locales"},
		"watch":   []any{"file.managed:/etc/locale.gen"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:locales" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/locale.gen" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
}

func TestLocalePresentCheckNoChange(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("locale", &exec.CommandResult{
		Stdout:   "C\nC.UTF-8\nen_US.utf8\n",
		ExitCode: 0,
	}, nil)
	mctx := testLocaleMctx(fakeCmd, exectest.NewFakeFileExec())
	builder := NewLocalePresentBuilder(mctx)

	s, err := builder("en_US.UTF-8", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when locale is already generated")
	}
}

func TestLocalePresentCheckNeedsChange(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("locale", &exec.CommandResult{
		Stdout:   "C\nC.UTF-8\n",
		ExitCode: 0,
	}, nil)
	mctx := testLocaleMctx(fakeCmd, exectest.NewFakeFileExec())
	builder := NewLocalePresentBuilder(mctx)

	s, err := builder("en_US.UTF-8", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when locale is missing")
	}
}

func TestLocalePresentCheckError(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetError("locale", errors.New("command not found"))
	mctx := testLocaleMctx(fakeCmd, exectest.NewFakeFileExec())
	builder := NewLocalePresentBuilder(mctx)

	s, err := builder("en_US.UTF-8", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Check(context.Background())
	if err == nil {
		t.Error("expected error when locale command fails")
	}
}

func TestLocalePresentApply(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate(localeGenPath, []byte("# en_US.UTF-8 UTF-8\n"), 0644)

	mctx := testLocaleMctx(fakeCmd, fakeFile)
	builder := NewLocalePresentBuilder(mctx)

	s, err := builder("en_US.UTF-8", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after locale-gen")
	}
	if ar.Details["locale"] != "en_US.UTF-8" {
		t.Errorf("locale detail: got %q", ar.Details["locale"])
	}

	// Verify locale line was uncommented.
	data, ok := fakeFile.GetFile(localeGenPath)
	if !ok {
		t.Fatal("locale.gen not written")
	}
	if strings.Contains(string(data), "# en_US.UTF-8") {
		t.Error("locale line should be uncommented after Apply")
	}
}

func TestLocalePresentApplyError(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetError("locale-gen", errors.New("permission denied"))
	fakeFile := exectest.NewFakeFileExec()

	mctx := testLocaleMctx(fakeCmd, fakeFile)
	builder := NewLocalePresentBuilder(mctx)

	s, err := builder("en_US.UTF-8", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error when locale-gen fails")
	}
}

func TestLocalePresentRevert(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate(localeGenPath, []byte("en_US.UTF-8 UTF-8\n"), 0644)

	mctx := testLocaleMctx(fakeCmd, fakeFile)
	builder := NewLocalePresentBuilder(mctx)

	s, err := builder("en_US.UTF-8", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after revert")
	}

	// Verify locale line was commented.
	data, ok := fakeFile.GetFile(localeGenPath)
	if !ok {
		t.Fatal("locale.gen not written")
	}
	if !strings.Contains(string(data), "# en_US.UTF-8") {
		t.Errorf("expected locale line to be commented after Revert, got: %s", string(data))
	}
}

func TestLocalePresentNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File: exectest.NewFakeFileExec(),
		},
	}
	builder := NewLocalePresentBuilder(mctx)
	_, err := builder("en_US.UTF-8", map[string]any{})
	if err == nil {
		t.Error("expected error when no command provider is set")
	}
}
