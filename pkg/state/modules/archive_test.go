package modules

import (
	"context"
	"errors"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

func testArchiveMctx(cmd *exectest.FakeCommandExec, file *exectest.FakeFileExec) *exec.ModuleContext {
	return &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: cmd, File: file}}
}

func TestArchiveExtractedName(t *testing.T) {
	mctx := testArchiveMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	s, err := NewArchiveExtractedBuilder(mctx)("/opt/app", map[string]any{
		"source": "/tmp/app.tar.gz",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "archive.extracted:/opt/app" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestArchiveExtractedMissingSource(t *testing.T) {
	mctx := testArchiveMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec())
	_, err := NewArchiveExtractedBuilder(mctx)("/opt/app", map[string]any{})
	if err == nil {
		t.Fatal("expected error when source is missing")
	}
}

func TestArchiveExtractedMissingProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: exectest.NewFakeFileExec()}}
	_, err := NewArchiveExtractedBuilder(mctx)("/opt/app", map[string]any{
		"source": "/tmp/app.tar.gz",
	})
	if err == nil {
		t.Fatal("expected error when Command provider is nil")
	}
}

func TestArchiveExtractedIfMissingShortCircuit(t *testing.T) {
	ctx := context.Background()
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/opt/app/bin/app", []byte("binary"), 0755)

	s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
		"source":     "/tmp/app.tar.gz",
		"if_missing": "/opt/app/bin/app",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change when if_missing path exists")
	}
	if fakeCmd.CallCount() != 0 {
		t.Errorf("expected no command calls during Check, got %d", fakeCmd.CallCount())
	}
}

func TestArchiveExtractedIfMissingAbsentNeedsChange(t *testing.T) {
	ctx := context.Background()
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	// if_missing set but the path does not exist, even though the target
	// directory does: if_missing is the sole idempotency check.
	fakeFile.PreCreate("/opt/app", []byte{}, 0755)

	s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
		"source":     "/tmp/app.tar.gz",
		"if_missing": "/opt/app/bin/app",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when if_missing path is absent")
	}
}

func TestArchiveExtractedApplyTar(t *testing.T) {
	ctx := context.Background()
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()

	s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
		"source":   "/tmp/app.tar.gz",
		"makedirs": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after extraction")
	}
	if ar.Details["format"] != "tar" {
		t.Errorf("Details format: got %v", ar.Details)
	}

	calls := fakeCmd.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 command call, got %d: %v", len(calls), calls)
	}
	if calls[0].Command != "tar" {
		t.Errorf("command: got %q, want tar", calls[0].Command)
	}
	wantArgs := []string{"-xf", "/tmp/app.tar.gz", "-C", "/opt/app"}
	if len(calls[0].Args) != len(wantArgs) {
		t.Fatalf("args: got %v, want %v", calls[0].Args, wantArgs)
	}
	for i, a := range wantArgs {
		if calls[0].Args[i] != a {
			t.Errorf("args[%d]: got %q, want %q", i, calls[0].Args[i], a)
		}
	}
	if !fakeFile.Exists("/opt/app") {
		t.Error("expected target directory created via makedirs")
	}
}

func TestArchiveExtractedApplyZip(t *testing.T) {
	ctx := context.Background()
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()

	s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
		"source": "/tmp/app.zip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	calls := fakeCmd.Calls()
	if len(calls) != 1 || calls[0].Command != "unzip" {
		t.Fatalf("expected unzip call, got %v", calls)
	}
	wantArgs := []string{"-o", "/tmp/app.zip", "-d", "/opt/app"}
	for i, a := range wantArgs {
		if calls[0].Args[i] != a {
			t.Errorf("args[%d]: got %q, want %q", i, calls[0].Args[i], a)
		}
	}
}

func TestArchiveExtractedRemoteSourceDownloads(t *testing.T) {
	ctx := context.Background()
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()

	s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
		"source": "https://example.com/app.tar.gz",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	calls := fakeCmd.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected download + extract calls, got %v", calls)
	}
	if !calls[0].Shell {
		t.Error("expected shell download command first")
	}
	if calls[1].Command != "tar" {
		t.Errorf("second call: got %q, want tar", calls[1].Command)
	}
}

func TestArchiveExtractedIdempotentAfterApply(t *testing.T) {
	ctx := context.Background()
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()

	s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
		"source":   "/tmp/app.tar.gz",
		"makedirs": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange before apply")
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	// Weak fallback idempotency: the target dir now exists.
	cr2, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr2.NeedsChange {
		t.Error("expected no change on second Check after apply")
	}
}

func TestArchiveExtractedExtractFailure(t *testing.T) {
	ctx := context.Background()

	t.Run("command error", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeCmd.SetError("tar", errors.New("tar: not found"))
		s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, exectest.NewFakeFileExec()))("/opt/app", map[string]any{
			"source": "/tmp/app.tar.gz",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Apply(ctx); err == nil {
			t.Error("expected error when tar fails")
		}
	})

	t.Run("nonzero exit", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeCmd.SetResult("tar", &exec.CommandResult{ExitCode: 2, Stderr: "corrupt archive"}, nil)
		s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, exectest.NewFakeFileExec()))("/opt/app", map[string]any{
			"source": "/tmp/app.tar.gz",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Apply(ctx); err == nil {
			t.Error("expected error on nonzero exit code")
		}
	})
}

func TestArchiveExtractedRevert(t *testing.T) {
	ctx := context.Background()

	t.Run("removes dir created by apply", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeFile := exectest.NewFakeFileExec()
		s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
			"source":   "/tmp/app.tar.gz",
			"makedirs": true,
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
			t.Error("expected Changed on revert")
		}
		if fakeFile.Exists("/opt/app") {
			t.Error("expected target directory removed")
		}
	})

	t.Run("keeps pre-existing dir", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeFile := exectest.NewFakeFileExec()
		fakeFile.PreCreate("/opt/app", []byte{}, 0755)
		s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
			"source":   "/tmp/app.tar.gz",
			"makedirs": true,
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
		if ar.Changed {
			t.Error("expected no change when dir was not created by apply")
		}
		if !fakeFile.Exists("/opt/app") {
			t.Error("pre-existing directory must not be removed")
		}
	})
}
