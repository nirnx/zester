package modules

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

// gitLatestScriptCmd is a CommandExec fake that dispatches results per git
// subcommand. FakeCommandExec keys results by command name only, so it cannot
// distinguish "git rev-parse" from "git ls-remote"; this fake can.
type gitLatestScriptCmd struct {
	mu      sync.Mutex
	calls   []exec.CommandOpts
	respond func(args []string) *exec.CommandResult
}

func (g *gitLatestScriptCmd) Run(_ context.Context, opts exec.CommandOpts) (*exec.CommandResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, opts)
	if g.respond != nil {
		if res := g.respond(opts.Args); res != nil {
			return res, nil
		}
	}
	return &exec.CommandResult{ExitCode: 0}, nil
}

func (g *gitLatestScriptCmd) Calls() []exec.CommandOpts {
	g.mu.Lock()
	defer g.mu.Unlock()
	cp := make([]exec.CommandOpts, len(g.calls))
	copy(cp, g.calls)
	return cp
}

// gitLatestRespond returns a respond func for the read-only git queries used
// by GitLatest.Check and update: remote get-url, rev-parse HEAD, ls-remote.
func gitLatestRespond(url, head, remoteTip string) func([]string) *exec.CommandResult {
	return func(args []string) *exec.CommandResult {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "get-url"):
			return &exec.CommandResult{Stdout: url + "\n"}
		case strings.Contains(joined, "rev-parse"):
			return &exec.CommandResult{Stdout: head + "\n"}
		case strings.Contains(joined, "ls-remote"):
			return &exec.CommandResult{Stdout: remoteTip + "\tHEAD\n"}
		}
		return nil
	}
}

// gitLatestHasCall reports whether any recorded call's args contain sub.
func gitLatestHasCall(calls []exec.CommandOpts, sub string) bool {
	for _, c := range calls {
		for _, a := range c.Args {
			if a == sub {
				return true
			}
		}
	}
	return false
}

func TestGitLatestName(t *testing.T) {
	mctx := testGitMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	s, err := NewGitLatestBuilder(mctx)("https://example.com/repo.git", map[string]any{
		"target": "/opt/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "git.latest:https://example.com/repo.git" {
		t.Errorf("Name: got %q", s.Name())
	}
	if s.(*GitLatest).URL != "https://example.com/repo.git" {
		t.Errorf("URL default from id: got %q", s.(*GitLatest).URL)
	}
}

func TestGitLatestMissingTarget(t *testing.T) {
	mctx := testGitMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	_, err := NewGitLatestBuilder(mctx)("https://example.com/repo.git", map[string]any{})
	if err == nil {
		t.Fatal("expected error when target is missing")
	}
}

func TestGitLatestMissingProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: exectest.NewFakeFileExec()}}
	_, err := NewGitLatestBuilder(mctx)("https://example.com/repo.git", map[string]any{
		"target": "/opt/repo",
	})
	if err == nil {
		t.Fatal("expected error when Command provider is nil")
	}
}

func TestGitLatestCheckNeedsChangeWhenMissing(t *testing.T) {
	mctx := testGitMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	s, err := NewGitLatestBuilder(mctx)("https://example.com/repo.git", map[string]any{
		"target": "/opt/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when target is missing")
	}
}

func TestGitLatestCloneWhenMissing(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	mctx := testGitMctx(fakeCmd, fakeFile)
	s, err := NewGitLatestBuilder(mctx)("https://example.com/repo.git", map[string]any{
		"target": "/opt/repo",
		"branch": "main",
	})
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

	calls := fakeCmd.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 git call, got %d: %v", len(calls), calls)
	}
	args := calls[0].Args
	if calls[0].Command != "git" || len(args) == 0 || args[0] != "clone" {
		t.Fatalf("expected git clone, got %q %v", calls[0].Command, args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--branch main") {
		t.Errorf("expected --branch main in clone args: %v", args)
	}
	if args[len(args)-2] != "https://example.com/repo.git" || args[len(args)-1] != "/opt/repo" {
		t.Errorf("expected url and target as final args: %v", args)
	}
}

func TestGitLatestCloneWithRevChecksOut(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testGitMctx(fakeCmd, exectest.NewFakeFileExec())
	s, err := NewGitLatestBuilder(mctx)("https://example.com/repo.git", map[string]any{
		"target": "/opt/repo",
		"rev":    "abc123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := fakeCmd.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected clone + checkout, got %v", calls)
	}
	if !gitLatestHasCall(calls[1:], "checkout") || !gitLatestHasCall(calls[1:], "abc123") {
		t.Errorf("expected checkout abc123, got %v", calls[1].Args)
	}
}

func TestGitLatestCheckIdempotentWhenUpToDate(t *testing.T) {
	url := "https://example.com/repo.git"
	head := "abc123def456"
	cmd := &gitLatestScriptCmd{respond: gitLatestRespond(url, head, head)}
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)

	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd, File: fakeFile}}
	s, err := NewGitLatestBuilder(mctx)(url, map[string]any{"target": "/opt/repo"})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change when HEAD matches remote tip, diff: %s", cr.Diff)
	}
	// Check must not mutate the repo: only read-only queries allowed.
	for _, c := range cmd.Calls() {
		joined := strings.Join(c.Args, " ")
		if strings.Contains(joined, "fetch") || strings.Contains(joined, "reset") || strings.Contains(joined, "merge") {
			t.Errorf("Check ran mutating git command: %v", c.Args)
		}
	}
}

func TestGitLatestCheckNeedsChangeWhenBehind(t *testing.T) {
	url := "https://example.com/repo.git"
	cmd := &gitLatestScriptCmd{respond: gitLatestRespond(url, "oldsha", "newsha")}
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)

	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd, File: fakeFile}}
	s, err := NewGitLatestBuilder(mctx)(url, map[string]any{"target": "/opt/repo"})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when local HEAD is behind remote tip")
	}
}

func TestGitLatestCheckPinnedRev(t *testing.T) {
	url := "https://example.com/repo.git"
	cmd := &gitLatestScriptCmd{respond: gitLatestRespond(url, "abc123def456", "abc123def456")}
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)

	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd, File: fakeFile}}
	s, err := NewGitLatestBuilder(mctx)(url, map[string]any{
		"target": "/opt/repo",
		"rev":    "abc123",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change when HEAD is at pinned rev, diff: %s", cr.Diff)
	}
	// A pinned-rev check must not query the network.
	for _, c := range cmd.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), "ls-remote") {
			t.Errorf("pinned rev check must not run ls-remote: %v", c.Args)
		}
	}
}

func TestGitLatestUpdateFetchResetWhenBehind(t *testing.T) {
	url := "https://example.com/repo.git"
	cmd := &gitLatestScriptCmd{respond: gitLatestRespond(url, "oldsha", "newsha")}
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)

	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd, File: fakeFile}}
	s, err := NewGitLatestBuilder(mctx)(url, map[string]any{
		"target": "/opt/repo",
		"branch": "main",
		"force":  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after update")
	}

	calls := cmd.Calls()
	if !gitLatestHasCall(calls, "fetch") {
		t.Error("expected git fetch during update")
	}
	if !gitLatestHasCall(calls, "reset") || !gitLatestHasCall(calls, "origin/main") {
		t.Errorf("expected reset --hard origin/main, got %v", calls)
	}
	// URL matched, so no set-url must be issued.
	if gitLatestHasCall(calls, "set-url") {
		t.Error("unexpected set-url when remote URL already matches")
	}
}

func TestGitLatestUpdateFastForwardDefault(t *testing.T) {
	url := "https://example.com/repo.git"
	cmd := &gitLatestScriptCmd{respond: gitLatestRespond(url, "oldsha", "newsha")}
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)

	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd, File: fakeFile}}
	s, err := NewGitLatestBuilder(mctx)(url, map[string]any{"target": "/opt/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := cmd.Calls()
	if !gitLatestHasCall(calls, "fetch") {
		t.Error("expected git fetch during update")
	}
	if !gitLatestHasCall(calls, "merge") || !gitLatestHasCall(calls, "--ff-only") {
		t.Errorf("expected merge --ff-only, got %v", calls)
	}
}

func TestGitLatestUpdateFixesRemoteURL(t *testing.T) {
	cmd := &gitLatestScriptCmd{respond: gitLatestRespond("https://old.example.com/repo.git", "oldsha", "newsha")}
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)

	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd, File: fakeFile}}
	s, err := NewGitLatestBuilder(mctx)("https://new.example.com/repo.git", map[string]any{
		"target": "/opt/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !gitLatestHasCall(cmd.Calls(), "set-url") {
		t.Error("expected set-url when remote URL drifted")
	}
}

func TestGitLatestApplyCloneError(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetError("git", errors.New("repository not found"))
	mctx := testGitMctx(fakeCmd, exectest.NewFakeFileExec())
	s, err := NewGitLatestBuilder(mctx)("https://example.com/repo.git", map[string]any{
		"target": "/opt/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err == nil {
		t.Error("expected error when clone fails")
	}
}

func TestGitLatestRevertRemovesClone(t *testing.T) {
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	mctx := testGitMctx(fakeCmd, fakeFile)
	s, err := NewGitLatestBuilder(mctx)("https://example.com/repo.git", map[string]any{
		"target": "/opt/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after removing cloned dir")
	}
}

func TestGitLatestRevertResetsToPrevHead(t *testing.T) {
	url := "https://example.com/repo.git"
	cmd := &gitLatestScriptCmd{respond: gitLatestRespond(url, "oldsha", "newsha")}
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)

	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd, File: fakeFile}}
	s, err := NewGitLatestBuilder(mctx)(url, map[string]any{
		"target": "/opt/repo",
		"force":  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed on revert")
	}
	calls := cmd.Calls()
	last := calls[len(calls)-1]
	joined := strings.Join(last.Args, " ")
	if !strings.Contains(joined, "reset") || !strings.Contains(joined, "oldsha") {
		t.Errorf("expected reset --hard to previous HEAD, got %v", last.Args)
	}
}
