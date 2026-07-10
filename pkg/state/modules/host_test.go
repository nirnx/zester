package modules

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

func testHostMctx(file *exectest.FakeFileExec) *exec.ModuleContext {
	return &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: file}}
}

func TestHostPresentName(t *testing.T) {
	s, err := NewHostPresentBuilder(testHostMctx(exectest.NewFakeFileExec()))("web1", map[string]any{
		"ip": "10.0.0.5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "host.present:web1" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestHostPresentMissingIP(t *testing.T) {
	_, err := NewHostPresentBuilder(testHostMctx(exectest.NewFakeFileExec()))("web1", map[string]any{})
	if err == nil {
		t.Fatal("expected error when ip is missing")
	}
}

func TestHostPresentAddsEntry(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/hosts", []byte("127.0.0.1\tlocalhost\n"), 0644)

	s, err := NewHostPresentBuilder(testHostMctx(fakeFile))("web1", map[string]any{
		"ip": "10.0.0.5",
	})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange for missing entry")
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after adding entry")
	}
	got, _ := fakeFile.GetFile("/etc/hosts")
	want := "127.0.0.1\tlocalhost\n10.0.0.5\tweb1\n"
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

func TestHostPresentAppendsToExistingIPLine(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/hosts", []byte("10.0.0.5\thost-a\n"), 0644)

	s, err := NewHostPresentBuilder(testHostMctx(fakeFile))("web1", map[string]any{
		"ip": "10.0.0.5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := fakeFile.GetFile("/etc/hosts")
	want := "10.0.0.5\thost-a web1\n"
	if string(got) != want {
		t.Errorf("content: got %q want %q", string(got), want)
	}
}

func TestHostPresentMovesHostnameFromOtherIP(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/hosts", []byte("10.0.0.4\tweb1\n10.0.0.9\tother web1\n"), 0644)

	s, err := NewHostPresentBuilder(testHostMctx(fakeFile))("web1", map[string]any{
		"ip": "10.0.0.5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := fakeFile.GetFile("/etc/hosts")
	// The 10.0.0.4 line had only web1, so it is dropped; web1 is removed from
	// the 10.0.0.9 line; a new line maps web1 to 10.0.0.5.
	want := "10.0.0.9\tother\n10.0.0.5\tweb1\n"
	if string(got) != want {
		t.Errorf("content: got %q want %q", string(got), want)
	}
}

func TestHostPresentPathOverride(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()

	s, err := NewHostPresentBuilder(testHostMctx(fakeFile))("web1", map[string]any{
		"ip":   "10.0.0.5",
		"path": "/tmp/hosts",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, ok := fakeFile.GetFile("/tmp/hosts")
	if !ok {
		t.Fatal("expected /tmp/hosts written")
	}
	if string(got) != "10.0.0.5\tweb1\n" {
		t.Errorf("content: got %q", string(got))
	}
}

func TestHostPresentRevert(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/hosts", []byte("127.0.0.1\tlocalhost\n"), 0644)

	s, err := NewHostPresentBuilder(testHostMctx(fakeFile))("web1", map[string]any{
		"ip": "10.0.0.5",
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
	got, _ := fakeFile.GetFile("/etc/hosts")
	if string(got) != "127.0.0.1\tlocalhost\n" {
		t.Errorf("revert content: got %q", string(got))
	}
}

func TestHostPresentRevertAfterCreateRemovesFile(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()

	s, err := NewHostPresentBuilder(testHostMctx(fakeFile))("web1", map[string]any{
		"ip":   "10.0.0.5",
		"path": "/tmp/hosts",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed when revert removes the file Apply created")
	}
	if fakeFile.Exists("/tmp/hosts") {
		t.Error("expected file created by Apply to be removed on revert")
	}
}

func TestHostPresentRevertFreshInstanceNoOp(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/hosts", []byte("127.0.0.1\tlocalhost\n10.0.0.9\tother\n"), 0644)

	// Fresh instance: Apply never ran (the runner's ModeRevert call pattern).
	s, err := NewHostPresentBuilder(testHostMctx(fakeFile))("web1", map[string]any{
		"ip": "10.0.0.5",
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
	got, _ := fakeFile.GetFile("/etc/hosts")
	if string(got) != "127.0.0.1\tlocalhost\n10.0.0.9\tother\n" {
		t.Errorf("fresh-instance revert must not touch /etc/hosts, got %q", string(got))
	}
}

func TestHostPresentReadErrorFailsPhases(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/hosts", []byte("127.0.0.1\tlocalhost\n"), 0644)
	fakeFile.SetReadError("/etc/hosts", errors.New("input/output error"))

	s, err := NewHostPresentBuilder(testHostMctx(fakeFile))("web1", map[string]any{
		"ip": "10.0.0.5",
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
	// The transiently unreadable hosts file must never be overwritten.
	got, _ := fakeFile.GetFile("/etc/hosts")
	if string(got) != "127.0.0.1\tlocalhost\n" {
		t.Errorf("hosts file must not be rewritten on read error, got %q", string(got))
	}
}

func TestHostAbsentName(t *testing.T) {
	s, err := NewHostAbsentBuilder(testHostMctx(exectest.NewFakeFileExec()))("web1", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "host.absent:web1" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestHostAbsentRemovesEntry(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/hosts", []byte("127.0.0.1\tlocalhost\n10.0.0.5\tweb1 web2\n"), 0644)

	s, err := NewHostAbsentBuilder(testHostMctx(fakeFile))("web1", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange when hostname is present")
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after removal")
	}
	got, _ := fakeFile.GetFile("/etc/hosts")
	want := "127.0.0.1\tlocalhost\n10.0.0.5\tweb2\n"
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
}

func TestHostAbsentDropsWholeLine(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/hosts", []byte("10.0.0.5\tweb1\n"), 0644)

	s, err := NewHostAbsentBuilder(testHostMctx(fakeFile))("web1", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := fakeFile.GetFile("/etc/hosts")
	if string(got) != "" {
		t.Errorf("content: got %q want empty", string(got))
	}
}

func TestHostAbsentMissingFile(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()

	s, err := NewHostAbsentBuilder(testHostMctx(fakeFile))("web1", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when hosts file is missing")
	}
	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected Apply no-op when hosts file is missing")
	}
}

func TestHostAbsentRevertFreshInstanceNoOp(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/hosts", []byte("127.0.0.1\tlocalhost\n10.0.0.5\tweb1\n"), 0644)

	s, err := NewHostAbsentBuilder(testHostMctx(fakeFile))("web1", map[string]any{})
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
	if !fakeFile.Exists("/etc/hosts") {
		t.Fatal("fresh-instance revert must not delete /etc/hosts")
	}
}

func TestHostAbsentReadErrorFailsPhases(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/hosts", []byte("10.0.0.5\tweb1\n"), 0644)
	fakeFile.SetReadError("/etc/hosts", errors.New("input/output error"))

	s, err := NewHostAbsentBuilder(testHostMctx(fakeFile))("web1", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Check(ctx); err == nil {
		t.Error("expected Check to fail on a non-not-exist read error")
	}
	if _, err := s.Apply(ctx); err == nil {
		t.Error("expected Apply to fail on a non-not-exist read error")
	}
	got, _ := fakeFile.GetFile("/etc/hosts")
	if string(got) != "10.0.0.5\tweb1\n" {
		t.Errorf("hosts file must not be modified on read error, got %q", string(got))
	}
}

func TestHostAbsentRevertRestoresBackup(t *testing.T) {
	ctx := context.Background()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/hosts", []byte("127.0.0.1\tlocalhost\n10.0.0.5\tweb1\n"), 0644)

	s, err := NewHostAbsentBuilder(testHostMctx(fakeFile))("web1", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revert(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := fakeFile.GetFile("/etc/hosts")
	if string(got) != "127.0.0.1\tlocalhost\n10.0.0.5\tweb1\n" {
		t.Errorf("revert content: got %q", string(got))
	}
}
