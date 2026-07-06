package modules

import (
	"context"
	"errors"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

func testGitMctx(fakeCmd *exectest.FakeCommandExec, fakeFile *exectest.FakeFileExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Command: fakeCmd,
			File:    fakeFile,
			Package: exectest.NewFakePackageExec("apt"),
		},
	}
}

func TestGitClonedName(t *testing.T) {
	mctx := testGitMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	builder := NewGitClonedBuilder(mctx)
	s, err := builder("/opt/repo", map[string]any{"url": "https://github.com/example/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "git.cloned:/opt/repo" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestGitClonedPrimaryParamDefault(t *testing.T) {
	mctx := testGitMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	builder := NewGitClonedBuilder(mctx)
	s, err := builder("/opt/myrepo", map[string]any{"url": "https://github.com/example/repo"})
	if err != nil {
		t.Fatal(err)
	}
	g := s.(*GitCloned)
	if g.Path != "/opt/myrepo" {
		t.Errorf("Path: got %q, want /opt/myrepo", g.Path)
	}
}

func TestGitClonedRequisites(t *testing.T) {
	mctx := testGitMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	builder := NewGitClonedBuilder(mctx)
	s, err := builder("/opt/repo", map[string]any{
		"url":       "https://github.com/example/repo",
		"require":   []any{"pkg.installed:git"},
		"onchanges": []any{"cmd.run:build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:git" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:build" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
}

func TestGitClonedCheckNeedsChange_DirMissing(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	// Directory does not exist in fakeFile.
	mctx := testGitMctx(fakeCmd, fakeFile)
	builder := NewGitClonedBuilder(mctx)
	s, err := builder("/opt/repo", map[string]any{"url": "https://github.com/example/repo"})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when dir is missing")
	}
}

func TestGitClonedCheckNoChange_CorrectURL(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	// Simulate directory existing.
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)
	// Simulate git remote get-url returning the correct URL.
	fakeCmd.SetResult("git", &exec.CommandResult{Stdout: "https://github.com/example/repo\n", ExitCode: 0}, nil)

	mctx := testGitMctx(fakeCmd, fakeFile)
	builder := NewGitClonedBuilder(mctx)
	s, err := builder("/opt/repo", map[string]any{"url": "https://github.com/example/repo"})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change when URL matches, diff: %s", cr.Diff)
	}
}

func TestGitClonedApply_Clone(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	// Directory does not exist.
	mctx := testGitMctx(fakeCmd, fakeFile)
	builder := NewGitClonedBuilder(mctx)
	s, err := builder("/opt/repo", map[string]any{"url": "https://github.com/example/repo"})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after clone")
	}

	// Verify git clone was called.
	calls := fakeCmd.Calls()
	if len(calls) == 0 {
		t.Fatal("expected git command calls")
	}
	if calls[0].Command != "git" {
		t.Errorf("first call command: got %q, want %q", calls[0].Command, "git")
	}
	if len(calls[0].Args) < 2 || calls[0].Args[0] != "clone" {
		t.Errorf("first call args: got %v, expected clone subcommand", calls[0].Args)
	}
}

func TestGitClonedApplyError(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	fakeCmd.SetError("git", errors.New("repository not found"))

	mctx := testGitMctx(fakeCmd, fakeFile)
	builder := NewGitClonedBuilder(mctx)
	s, err := builder("/opt/repo", map[string]any{"url": "https://github.com/example/repo"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error when clone fails")
	}
}

func TestGitClonedRevert_CreatedByApply(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	mctx := testGitMctx(fakeCmd, fakeFile)
	builder := NewGitClonedBuilder(mctx)
	s, err := builder("/opt/repo", map[string]any{"url": "https://github.com/example/repo"})
	if err != nil {
		t.Fatal(err)
	}

	// Apply first so createdByApply is set.
	if _, applyErr := s.Apply(context.Background()); applyErr != nil {
		t.Fatal(applyErr)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after revert")
	}
}

func TestGitClonedRevert_NotCreatedByApply(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	// Directory exists before Apply (not created by us).
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)
	fakeCmd.SetResult("git", &exec.CommandResult{Stdout: "https://github.com/example/repo\n", ExitCode: 0}, nil)

	mctx := testGitMctx(fakeCmd, fakeFile)
	builder := NewGitClonedBuilder(mctx)
	s, err := builder("/opt/repo", map[string]any{"url": "https://github.com/example/repo"})
	if err != nil {
		t.Fatal(err)
	}

	// Do NOT call Apply — revert without prior apply.
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected no change on revert when not created by apply")
	}
}

func TestGitClonedNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File: exectest.NewFakeFileExec(),
		},
	}
	builder := NewGitClonedBuilder(mctx)
	_, err := builder("/opt/repo", map[string]any{"url": "https://example.com/repo"})
	if err == nil {
		t.Error("expected error when no command provider is set")
	}
}

func TestGitClonedMissingURL(t *testing.T) {
	mctx := testGitMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	builder := NewGitClonedBuilder(mctx)
	_, err := builder("/opt/repo", map[string]any{})
	if err == nil {
		t.Error("expected error when url is missing")
	}
}
