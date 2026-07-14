package sshauthmod

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
)

const sshAuthTestPath = "/home/alice/.ssh/authorized_keys"

func testSSHAuthMctx(file *exectest.FakeFileExec) *exec.ModuleContext {
	return &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: file}}
}

func TestSSHAuthPresentName(t *testing.T) {
	s, err := NewSSHAuthPresentBuilder(testSSHAuthMctx(exectest.NewFakeFileExec()), modschema.DecodeOptions{})("AAAAKEY", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "ssh_auth.present:AAAAKEY" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestSSHAuthPresentRequiresUserOrConfig(t *testing.T) {
	_, err := NewSSHAuthPresentBuilder(testSSHAuthMctx(exectest.NewFakeFileExec()), modschema.DecodeOptions{})("AAAAKEY", map[string]any{})
	if err == nil {
		t.Fatal("expected error when neither user nor config is set")
	}
}

func TestSSHAuthPresentTrimsKey(t *testing.T) {
	// The name-TrimSpace is builder-tail module logic (a decoder never trims):
	// leading/trailing whitespace around the key blob is stripped, while a full
	// key line's internal spaces are preserved.
	s, err := NewSSHAuthPresentBuilder(testSSHAuthMctx(exectest.NewFakeFileExec()), modschema.DecodeOptions{})("  AAAAKEY  ", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.(*SSHAuthPresent).Key; got != "AAAAKEY" {
		t.Errorf("Key = %q, want trimmed %q", got, "AAAAKEY")
	}
}

func TestSSHAuthAbsentTrimsKey(t *testing.T) {
	s, err := NewSSHAuthAbsentBuilder(testSSHAuthMctx(exectest.NewFakeFileExec()), modschema.DecodeOptions{})("  AAAAKEY  ", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.(*SSHAuthAbsent).Key; got != "AAAAKEY" {
		t.Errorf("Key = %q, want trimmed %q", got, "AAAAKEY")
	}
}

func TestSSHAuthPresentEncDefault(t *testing.T) {
	// enc carries an eager default=ssh-rsa.
	s, err := NewSSHAuthPresentBuilder(testSSHAuthMctx(exectest.NewFakeFileExec()), modschema.DecodeOptions{})("AAAAKEY", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.(*SSHAuthPresent).Enc; got != "ssh-rsa" {
		t.Errorf("Enc default = %q, want ssh-rsa", got)
	}
}

func TestSSHAuthPresentAddsKeyLine(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()

	s, err := NewSSHAuthPresentBuilder(testSSHAuthMctx(fakeFile), modschema.DecodeOptions{})("AAAATESTKEY", map[string]any{
		"config":  sshAuthTestPath,
		"comment": "alice@example",
	})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange when key is missing")
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after adding key")
	}
	got, _ := fakeFile.GetFile(sshAuthTestPath)
	want := "ssh-rsa AAAATESTKEY alice@example\n"
	if string(got) != want {
		t.Errorf("content: got %q want %q", string(got), want)
	}

	// Idempotent second run.
	cr2, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr2.NeedsChange {
		t.Error("expected no change on second Check")
	}
	ar2, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ar2.Changed {
		t.Error("expected Apply no-op on second run")
	}
}

func TestSSHAuthPresentModes(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()

	s, err := NewSSHAuthPresentBuilder(testSSHAuthMctx(fakeFile), modschema.DecodeOptions{})("AAAATESTKEY", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}

	fi, err := fakeFile.Stat(ctx, sshAuthTestPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != fs.FileMode(0600) {
		t.Errorf("authorized_keys mode: got %o want 0600", fi.Mode().Perm())
	}
	di, err := fakeFile.Stat(ctx, "/home/alice/.ssh")
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != fs.FileMode(0700) {
		t.Errorf(".ssh dir mode: got %o want 0700", di.Mode().Perm())
	}
}

func TestSSHAuthPresentFullLineVerbatim(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()

	s, err := NewSSHAuthPresentBuilder(testSSHAuthMctx(fakeFile), modschema.DecodeOptions{})("ssh-ed25519 AAAAC3XYZ bob@host", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := fakeFile.GetFile(sshAuthTestPath)
	if string(got) != "ssh-ed25519 AAAAC3XYZ bob@host\n" {
		t.Errorf("content: got %q", string(got))
	}
}

func TestSSHAuthPresentReplacesExistingLineForSameBlob(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate(sshAuthTestPath, []byte("ssh-rsa AAAATESTKEY old-comment\nssh-rsa AAAAOTHER other@host\n"), 0600)

	s, err := NewSSHAuthPresentBuilder(testSSHAuthMctx(fakeFile), modschema.DecodeOptions{})("AAAATESTKEY", map[string]any{
		"config":  sshAuthTestPath,
		"comment": "new-comment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := fakeFile.GetFile(sshAuthTestPath)
	want := "ssh-rsa AAAATESTKEY new-comment\nssh-rsa AAAAOTHER other@host\n"
	if string(got) != want {
		t.Errorf("content: got %q want %q", string(got), want)
	}
}

func TestSSHAuthPresentRevert(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate(sshAuthTestPath, []byte("ssh-rsa AAAAOTHER other@host\n"), 0600)

	s, err := NewSSHAuthPresentBuilder(testSSHAuthMctx(fakeFile), modschema.DecodeOptions{})("AAAATESTKEY", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revert(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := fakeFile.GetFile(sshAuthTestPath)
	if string(got) != "ssh-rsa AAAAOTHER other@host\n" {
		t.Errorf("revert content: got %q", string(got))
	}
}

func TestSSHAuthPresentRevertRestoresMode(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate(sshAuthTestPath, []byte("ssh-rsa AAAAOTHER other@host\n"), 0600)

	s, err := NewSSHAuthPresentBuilder(testSSHAuthMctx(fakeFile), modschema.DecodeOptions{})("AAAATESTKEY", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revert(ctx); err != nil {
		t.Fatal(err)
	}
	fi, err := fakeFile.Stat(ctx, sshAuthTestPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != fs.FileMode(0600) {
		t.Errorf("restored authorized_keys mode: got %o want 0600", fi.Mode().Perm())
	}
}

func TestSSHAuthPresentRevertFreshInstanceNoOp(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	content := "ssh-rsa AAAA1 a@h\nssh-rsa AAAA2 b@h\nssh-rsa AAAA3 c@h\n"
	fakeFile.PreCreate(sshAuthTestPath, []byte(content), 0600)

	// Fresh instance: Apply never ran (the runner's ModeRevert call pattern).
	// Revert must NOT delete the user's authorized_keys.
	s, err := NewSSHAuthPresentBuilder(testSSHAuthMctx(fakeFile), modschema.DecodeOptions{})("AAAATESTKEY", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected clean no-op revert on a fresh instance")
	}
	if !strings.Contains(ar.Diff, "nothing to revert") {
		t.Errorf("expected explicit nothing-to-revert diff, got %q", ar.Diff)
	}
	got, ok := fakeFile.GetFile(sshAuthTestPath)
	if !ok || string(got) != content {
		t.Errorf("fresh-instance revert must not touch authorized_keys, got %q (exists=%v)", string(got), ok)
	}
}

func TestSSHAuthPresentReadErrorFailsPhases(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	content := "ssh-rsa AAAAOTHER other@host\n"
	fakeFile.PreCreate(sshAuthTestPath, []byte(content), 0600)
	fakeFile.SetReadError(sshAuthTestPath, errors.New("stale NFS file handle"))

	s, err := NewSSHAuthPresentBuilder(testSSHAuthMctx(fakeFile), modschema.DecodeOptions{})("AAAATESTKEY", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Check(ctx); err == nil {
		t.Error("expected Check to fail on a non-not-exist read error")
	}
	if _, err := s.Apply(ctx); err == nil {
		t.Error("expected Apply to fail on a non-not-exist read error")
	}
	// The transiently unreadable authorized_keys must never be rewritten to
	// just the managed key.
	got, _ := fakeFile.GetFile(sshAuthTestPath)
	if string(got) != content {
		t.Errorf("authorized_keys must not be rewritten on read error, got %q", string(got))
	}
}

func TestSSHAuthAbsentName(t *testing.T) {
	s, err := NewSSHAuthAbsentBuilder(testSSHAuthMctx(exectest.NewFakeFileExec()), modschema.DecodeOptions{})("AAAAKEY", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "ssh_auth.absent:AAAAKEY" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestSSHAuthAbsentRequiresUserOrConfig(t *testing.T) {
	_, err := NewSSHAuthAbsentBuilder(testSSHAuthMctx(exectest.NewFakeFileExec()), modschema.DecodeOptions{})("AAAAKEY", map[string]any{})
	if err == nil {
		t.Fatal("expected error when neither user nor config is set")
	}
}

func TestSSHAuthAbsentRemovesKey(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate(sshAuthTestPath, []byte("ssh-rsa AAAATESTKEY alice@example\nssh-rsa AAAAOTHER other@host\n"), 0600)

	s, err := NewSSHAuthAbsentBuilder(testSSHAuthMctx(fakeFile), modschema.DecodeOptions{})("AAAATESTKEY", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange when key is present")
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after removal")
	}
	got, _ := fakeFile.GetFile(sshAuthTestPath)
	if string(got) != "ssh-rsa AAAAOTHER other@host\n" {
		t.Errorf("content: got %q", string(got))
	}

	// Idempotent second run.
	cr2, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr2.NeedsChange {
		t.Error("expected no change on second Check")
	}
}

func TestSSHAuthAbsentMissingFile(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()

	s, err := NewSSHAuthAbsentBuilder(testSSHAuthMctx(fakeFile), modschema.DecodeOptions{})("AAAATESTKEY", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when authorized_keys is missing")
	}
	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected Apply no-op when authorized_keys is missing")
	}
}

func TestSSHAuthAbsentRevertFreshInstanceNoOp(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	content := "ssh-rsa AAAA1 a@h\nssh-rsa AAAA2 b@h\nssh-rsa AAAA3 c@h\n"
	fakeFile.PreCreate(sshAuthTestPath, []byte(content), 0600)

	s, err := NewSSHAuthAbsentBuilder(testSSHAuthMctx(fakeFile), modschema.DecodeOptions{})("AAAA2", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected clean no-op revert on a fresh instance")
	}
	if !strings.Contains(ar.Diff, "nothing to revert") {
		t.Errorf("expected explicit nothing-to-revert diff, got %q", ar.Diff)
	}
	got, ok := fakeFile.GetFile(sshAuthTestPath)
	if !ok || string(got) != content {
		t.Errorf("fresh-instance revert must not touch authorized_keys, got %q (exists=%v)", string(got), ok)
	}
}

func TestSSHAuthAbsentReadErrorFailsPhases(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	content := "ssh-rsa AAAATESTKEY alice@example\n"
	fakeFile.PreCreate(sshAuthTestPath, []byte(content), 0600)
	fakeFile.SetReadError(sshAuthTestPath, errors.New("permission denied"))

	s, err := NewSSHAuthAbsentBuilder(testSSHAuthMctx(fakeFile), modschema.DecodeOptions{})("AAAATESTKEY", map[string]any{
		"config": sshAuthTestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Check(ctx); err == nil {
		t.Error("expected Check to fail on a non-not-exist read error")
	}
	if _, err := s.Apply(ctx); err == nil {
		t.Error("expected Apply to fail on a non-not-exist read error")
	}
	got, _ := fakeFile.GetFile(sshAuthTestPath)
	if string(got) != content {
		t.Errorf("authorized_keys must not be modified on read error, got %q", string(got))
	}
}
