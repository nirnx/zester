package modules

import (
	"context"
	"errors"
	"strings"
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

func TestArchiveExtractedSourceHash(t *testing.T) {
	ctx := context.Background()

	build := func(fakeCmd *exectest.FakeCommandExec, fakeFile *exectest.FakeFileExec, hash string) *ArchiveExtracted {
		t.Helper()
		s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
			"source":      "/tmp/app-" + hash + ".tar.gz",
			"source_hash": hash,
			"makedirs":    true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return s.(*ArchiveExtracted)
	}

	t.Run("apply writes marker and converges", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeFile := exectest.NewFakeFileExec()
		s := build(fakeCmd, fakeFile, "sha256=aaa111")

		cr, err := s.Check(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !cr.NeedsChange {
			t.Fatal("expected NeedsChange before first extraction")
		}
		if _, err := s.Apply(ctx); err != nil {
			t.Fatal(err)
		}
		marker, ok := fakeFile.GetFile(s.markerPath())
		if !ok {
			t.Fatalf("expected marker file at %s", s.markerPath())
		}
		if strings.TrimSpace(string(marker)) != "sha256=aaa111" {
			t.Errorf("marker content: got %q", string(marker))
		}
		cr2, err := s.Check(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if cr2.NeedsChange {
			t.Errorf("expected no change after extraction, diff: %s", cr2.Diff)
		}
	})

	t.Run("changed declaration re-extracts", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeFile := exectest.NewFakeFileExec()
		// Simulate the 1.2.3 extraction: dir + marker recorded by a prior run.
		old := build(fakeCmd, fakeFile, "sha256=aaa111")
		if _, err := old.Apply(ctx); err != nil {
			t.Fatal(err)
		}

		// Version bump: same state id, new source_hash — a FRESH instance
		// (states are rebuilt per execution).
		bumped := build(exectest.NewFakeCommandExec(), fakeFile, "sha256=bbb222")
		cr, err := bumped.Check(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !cr.NeedsChange {
			t.Error("expected NeedsChange when the declared source_hash differs from the marker")
		}
		if !strings.Contains(cr.Diff, "source_hash changed") {
			t.Errorf("expected source_hash-changed diff, got %q", cr.Diff)
		}
		if _, err := bumped.Apply(ctx); err != nil {
			t.Fatal(err)
		}
		cr2, err := bumped.Check(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if cr2.NeedsChange {
			t.Errorf("expected convergence after re-extraction, diff: %s", cr2.Diff)
		}
	})

	t.Run("pre-existing dir without marker re-extracts", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeFile := exectest.NewFakeFileExec()
		fakeFile.PreCreate("/opt/app", []byte{}, 0755)
		s := build(fakeCmd, fakeFile, "sha256=aaa111")

		cr, err := s.Check(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !cr.NeedsChange {
			t.Error("expected NeedsChange when source_hash is declared but no marker exists")
		}
	})

	t.Run("if_missing combines with source_hash", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeFile := exectest.NewFakeFileExec()
		fakeFile.PreCreate("/opt/app/bin/app", []byte("binary"), 0755)
		s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
			"source":      "/tmp/app.tar.gz",
			"source_hash": "sha256=bbb222",
			"if_missing":  "/opt/app/bin/app",
		})
		if err != nil {
			t.Fatal(err)
		}
		// if_missing exists but the marker is absent → re-extract.
		cr, err := s.Check(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !cr.NeedsChange {
			t.Error("expected NeedsChange when if_missing exists but marker is missing")
		}
	})

	t.Run("marker read error fails check", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeFile := exectest.NewFakeFileExec()
		s := build(fakeCmd, fakeFile, "sha256=aaa111")
		if _, err := s.Apply(ctx); err != nil {
			t.Fatal(err)
		}
		fakeFile.SetReadError(s.markerPath(), errors.New("input/output error"))
		if _, err := s.Check(ctx); err == nil {
			t.Error("expected Check to fail on a non-not-exist marker read error")
		}
	})

	t.Run("failed extraction does not latch the marker", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeCmd.SetResult("tar", &exec.CommandResult{ExitCode: 2, Stderr: "corrupt archive"}, nil)
		fakeFile := exectest.NewFakeFileExec()
		s := build(fakeCmd, fakeFile, "sha256=aaa111")

		if _, err := s.Apply(ctx); err == nil {
			t.Fatal("expected Apply error on failed extraction")
		}
		if _, ok := fakeFile.GetFile(s.markerPath()); ok {
			t.Error("marker must not be written after a failed extraction")
		}
		cr, err := s.Check(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !cr.NeedsChange {
			t.Error("expected NeedsChange after a failed extraction (makedirs dir must not latch)")
		}
	})
}

func TestArchiveExtractedWatchForcedApplyNoOp(t *testing.T) {
	// A watch-forced apply bypasses Check and calls Apply directly on a
	// FRESH instance. Apply must re-evaluate the extraction guard itself: a
	// converged archive is a clean Changed:false no-op — never a
	// re-download/re-extract over post-extraction local modifications.
	ctx := context.Background()

	t.Run("if_missing exists", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeFile := exectest.NewFakeFileExec()
		fakeFile.PreCreate("/opt/app/bin/app", []byte("binary"), 0755)

		s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
			"source":     "https://example.com/app.tar.gz",
			"if_missing": "/opt/app/bin/app",
		})
		if err != nil {
			t.Fatal(err)
		}
		ar, err := s.Apply(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ar.Changed {
			t.Error("expected Changed: false when if_missing path exists")
		}
		if !strings.Contains(ar.Diff, "nothing to do") {
			t.Errorf("expected clean no-op diff, got %q", ar.Diff)
		}
		if n := fakeCmd.CallCount(); n != 0 {
			t.Errorf("no download/extract may run for a converged archive, got %d calls: %v", n, fakeCmd.Calls())
		}
	})

	t.Run("weak target-dir fallback", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeFile := exectest.NewFakeFileExec()
		fakeFile.PreCreate("/opt/app", []byte{}, 0755)

		s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
			"source": "/tmp/app.tar.gz",
		})
		if err != nil {
			t.Fatal(err)
		}
		ar, err := s.Apply(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ar.Changed {
			t.Error("expected Changed: false when the target dir exists")
		}
		if fakeCmd.CallCount() != 0 {
			t.Errorf("no extract may run, got %v", fakeCmd.Calls())
		}
	})

	t.Run("matching source_hash marker", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeFile := exectest.NewFakeFileExec()
		// Prior run: extracted and recorded the marker.
		prior, err := NewArchiveExtractedBuilder(testArchiveMctx(exectest.NewFakeCommandExec(), fakeFile))("/opt/app", map[string]any{
			"source":      "/tmp/app.tar.gz",
			"source_hash": "sha256=aaa111",
			"makedirs":    true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := prior.Apply(ctx); err != nil {
			t.Fatal(err)
		}

		// Fresh instance, watch-forced: marker matches → no-op.
		s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
			"source":      "/tmp/app.tar.gz",
			"source_hash": "sha256=aaa111",
			"makedirs":    true,
		})
		if err != nil {
			t.Fatal(err)
		}
		ar, err := s.Apply(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ar.Changed {
			t.Error("expected Changed: false when the source_hash marker matches")
		}
		if fakeCmd.CallCount() != 0 {
			t.Errorf("no extract may run, got %v", fakeCmd.Calls())
		}
	})

	t.Run("changed source_hash still re-extracts", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeFile := exectest.NewFakeFileExec()
		fakeFile.PreCreate("/opt/app", []byte{}, 0755)
		prior, err := NewArchiveExtractedBuilder(testArchiveMctx(exectest.NewFakeCommandExec(), fakeFile))("/opt/app", map[string]any{
			"source":      "/tmp/app.tar.gz",
			"source_hash": "sha256=aaa111",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := prior.Apply(ctx); err != nil {
			t.Fatal(err)
		}

		// Version bump on a fresh watch-forced instance: guard unsatisfied.
		s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
			"source":      "/tmp/app.tar.gz",
			"source_hash": "sha256=bbb222",
		})
		if err != nil {
			t.Fatal(err)
		}
		ar, err := s.Apply(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ar.Changed {
			t.Error("expected re-extraction on a source_hash bump")
		}
		if fakeCmd.CallCount() == 0 {
			t.Error("expected the extract command to run")
		}
	})

	t.Run("guard stat error fails apply", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		file := &statErrFileExec{
			FakeFileExec: exectest.NewFakeFileExec(),
			path:         "/opt/app/bin/app",
			err:          errors.New("input/output error"),
		}
		mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: fakeCmd, File: file}}
		s, err := NewArchiveExtractedBuilder(mctx)("/opt/app", map[string]any{
			"source":     "/tmp/app.tar.gz",
			"if_missing": "/opt/app/bin/app",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Apply(ctx); err == nil {
			t.Error("expected Apply to fail on a non-not-exist guard stat error")
		}
		if fakeCmd.CallCount() != 0 {
			t.Errorf("no extract may run on an unverifiable guard, got %v", fakeCmd.Calls())
		}
	})
}

func TestArchiveExtractedRetryAfterFailedApplyProceeds(t *testing.T) {
	// retry: re-invokes Apply on the SAME instance after a failure. The
	// Apply-side guard must not latch on the directory this instance's own
	// makedirs created before the failed extraction.
	ctx := context.Background()
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("tar", &exec.CommandResult{ExitCode: 2, Stderr: "corrupt archive"}, nil)
	fakeFile := exectest.NewFakeFileExec()

	s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile))("/opt/app", map[string]any{
		"source":   "/tmp/app.tar.gz",
		"makedirs": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err == nil {
		t.Fatal("expected first Apply to fail")
	}

	// The archive is fixed; the retried Apply must actually extract.
	fakeCmd.SetResult("tar", &exec.CommandResult{ExitCode: 0}, nil)
	before := fakeCmd.CallCount()
	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("retried Apply must extract, not no-op on its own makedirs directory")
	}
	if fakeCmd.CallCount() != before+1 {
		t.Errorf("expected exactly one more extract call, got %v", fakeCmd.Calls())
	}

	// Converged after the successful retry — same-instance Check agrees.
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected convergence after a successful retry, diff: %s", cr.Diff)
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
