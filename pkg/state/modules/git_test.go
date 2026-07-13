package modules

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
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
	builder := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("/opt/repo", map[string]any{"url": "https://github.com/example/repo"})
	if err != nil {
		t.Fatal(err)
	}

	// Do NOT call Apply — revert without prior apply (fresh-instance
	// ModeRevert pattern). Must be an explicit clean no-op.
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected no change on revert when not created by apply")
	}
	if !strings.Contains(ar.Diff, "nothing to revert") {
		t.Errorf("expected explicit nothing-to-revert diff, got %q", ar.Diff)
	}
	if !fakeFile.Exists("/opt/repo") {
		t.Error("fresh-instance revert must not remove the repo directory")
	}
}

const gitTestHead = "9fceb02d0ae598e95dc970b74767f19372d61af8"

// gitRevRespond scripts the read-only git queries used by the Check paths:
// remote get-url, rev-parse HEAD, rev-parse of specific revs (the resolve
// map keys, e.g. "v1.2.3^{commit}" or "refs/heads/main"), and ls-remote.
// Unresolvable revs answer exit 128 like real git.
func gitRevRespond(url, head string, resolve map[string]string) func([]string) *exec.CommandResult {
	return func(args []string) *exec.CommandResult {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "get-url"):
			return &exec.CommandResult{Stdout: url + "\n"}
		case strings.Contains(joined, "rev-parse"):
			last := args[len(args)-1]
			if last == "HEAD" {
				return &exec.CommandResult{Stdout: head + "\n"}
			}
			if sha, ok := resolve[last]; ok {
				return &exec.CommandResult{Stdout: sha + "\n"}
			}
			return &exec.CommandResult{ExitCode: 128, Stderr: "unknown revision"}
		case strings.Contains(joined, "ls-remote"):
			return &exec.CommandResult{Stdout: head + "\tHEAD\n"}
		}
		return nil
	}
}

func TestGitClonedCheckTagRevConverges(t *testing.T) {
	// rev: v1.2.3 checked out (detached HEAD at the tag's commit): the state
	// must converge via rev-parse <rev>^{commit}, not churn forever because
	// HasPrefix(sha, "v1.2.3") can never match.
	url := "https://example.com/repo.git"
	cmd := &gitLatestScriptCmd{respond: gitRevRespond(url, gitTestHead, map[string]string{
		"v1.2.3^{commit}": gitTestHead,
	})}
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)

	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd, File: fakeFile}}
	s, err := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})("/opt/repo", map[string]any{
		"url": url,
		"rev": "v1.2.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change when HEAD is at the tag's commit, diff: %s", cr.Diff)
	}
	// Check must stay read-only: no fetch/checkout.
	for _, c := range cmd.Calls() {
		joined := strings.Join(c.Args, " ")
		if strings.Contains(joined, "fetch") || strings.Contains(joined, "checkout") {
			t.Errorf("Check ran mutating git command: %v", c.Args)
		}
	}
}

func TestGitClonedCheckTagRevNeedsChangeWhenDifferent(t *testing.T) {
	url := "https://example.com/repo.git"
	cmd := &gitLatestScriptCmd{respond: gitRevRespond(url, gitTestHead, map[string]string{
		"v1.2.3^{commit}": "1111111111111111111111111111111111111111",
	})}
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)

	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd, File: fakeFile}}
	s, err := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})("/opt/repo", map[string]any{
		"url": url,
		"rev": "v1.2.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when the tag resolves to a different commit")
	}
}

func TestGitClonedCheckUnresolvableRevNeedsChange(t *testing.T) {
	// A tag not fetched yet does not resolve locally: NeedsChange, and
	// Apply's fetch+checkout converges it.
	url := "https://example.com/repo.git"
	cmd := &gitLatestScriptCmd{respond: gitRevRespond(url, gitTestHead, nil)}
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)

	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd, File: fakeFile}}
	s, err := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})("/opt/repo", map[string]any{
		"url": url,
		"rev": "v9.9.9",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when the rev cannot be resolved locally")
	}
}

func TestGitClonedCheckShaPrefixRev(t *testing.T) {
	url := "https://example.com/repo.git"
	cmd := &gitLatestScriptCmd{respond: gitRevRespond(url, gitTestHead, nil)}
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)

	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd, File: fakeFile}}
	s, err := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})("/opt/repo", map[string]any{
		"url": url,
		"rev": gitTestHead[:7],
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change for a sha-prefix rev at HEAD, diff: %s", cr.Diff)
	}
	// The sha fast path needs no rev resolution subprocess.
	for _, c := range cmd.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), "--verify") {
			t.Errorf("sha-prefix rev must not invoke rev-parse --verify: %v", c.Args)
		}
	}
}

func TestGitClonedCheckTagInBranchConverges(t *testing.T) {
	// The docs sanction `branch: v1.2.3` for tags; `git clone --branch <tag>`
	// leaves a detached HEAD with no local branch, so refs/heads fails and
	// the symbolic-rev fallback must converge the state.
	url := "https://example.com/repo.git"
	cmd := &gitLatestScriptCmd{respond: gitRevRespond(url, gitTestHead, map[string]string{
		"v1.2.3^{commit}": gitTestHead,
	})}
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)

	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd, File: fakeFile}}
	s, err := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})("/opt/repo", map[string]any{
		"url":    url,
		"branch": "v1.2.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change when detached HEAD is at the branch-declared tag, diff: %s", cr.Diff)
	}
}

func TestGitClonedCheckBranchStillMatchesLocalRef(t *testing.T) {
	url := "https://example.com/repo.git"
	cmd := &gitLatestScriptCmd{respond: gitRevRespond(url, gitTestHead, map[string]string{
		"refs/heads/main": gitTestHead,
	})}
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/opt/repo", []byte{}, 0755)

	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd, File: fakeFile}}
	s, err := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})("/opt/repo", map[string]any{
		"url":    url,
		"branch": "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change when HEAD equals the local branch tip, diff: %s", cr.Diff)
	}
}

func TestGitClonedStatErrorFailsPhases(t *testing.T) {
	// A transient non-not-exist stat error (ESTALE/EACCES/EIO) on an
	// EXISTING checkout must fail both phases — never report a bogus
	// pending clone in --test, and never run `git clone` over the checkout.
	fakeCmd := exectest.NewFakeCommandExec()
	file := &statErrFileExec{
		FakeFileExec: exectest.NewFakeFileExec(),
		path:         "/opt/repo",
		err:          errors.New("stale NFS file handle"),
	}
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: fakeCmd, File: file}}
	s, err := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})("/opt/repo", map[string]any{"url": "https://github.com/example/repo"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Check(context.Background()); err == nil {
		t.Error("expected Check to fail on a non-not-exist stat error")
	}
	if _, err := s.Apply(context.Background()); err == nil {
		t.Error("expected Apply to fail on a non-not-exist stat error")
	}
	if n := fakeCmd.CallCount(); n != 0 {
		t.Errorf("no git command may run when the stat failed, got %d calls: %v", n, fakeCmd.Calls())
	}

	// And Revert on the same instance stays a clean no-op (nothing applied).
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("Revert must be a no-op after a failed Apply")
	}
}

func TestGitClonedNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File: exectest.NewFakeFileExec(),
		},
	}
	builder := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})
	_, err := builder("/opt/repo", map[string]any{"url": "https://example.com/repo"})
	if err == nil {
		t.Error("expected error when no command provider is set")
	}
}

func TestGitClonedMissingURL(t *testing.T) {
	mctx := testGitMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	builder := NewGitClonedBuilder(mctx, modschema.DecodeOptions{})
	_, err := builder("/opt/repo", map[string]any{})
	if err == nil {
		t.Error("expected error when url is missing")
	}
}

var _ state.State = (*GitCloned)(nil)

// TestGitClonedContract replays the permanent differential contract fixtures
// against the migrated git.cloned decoder. The cases were approved by the
// legacy-vs-new equivalence comparison while the legacy constructor still
// existed (see the migration changelog); after its deletion this replay is
// the permanent regression guard for git.cloned's decode behavior, including
// the flagged BD-1/BD-2/BD-6/BD-7 divergences.
func TestGitClonedContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var g GitCloned
		if _, err := gitClonedSpec.Decode(id, config, &g, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &g, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/git.cloned.yaml")
}
