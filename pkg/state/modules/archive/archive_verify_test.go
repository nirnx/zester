package archivemod

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
)

// archiveBytes is the fake archive content every verification test hashes.
var archiveBytes = []byte("hello archive bytes")

func digestOf(algo string, data []byte) string {
	switch algo {
	case "md5":
		s := md5.Sum(data) //nolint:gosec // test vector for the Salt-compatible form
		return hex.EncodeToString(s[:])
	case "sha1":
		s := sha1.Sum(data) //nolint:gosec // test vector for the Salt-compatible form
		return hex.EncodeToString(s[:])
	case "sha256":
		s := sha256.Sum256(data)
		return hex.EncodeToString(s[:])
	case "sha512":
		s := sha512.Sum512(data)
		return hex.EncodeToString(s[:])
	}
	panic("unknown algo " + algo)
}

func buildArchive(t *testing.T, fakeCmd *exectest.FakeCommandExec, fakeFile *exectest.FakeFileExec, id string, cfg map[string]any) *ArchiveExtracted {
	t.Helper()
	s, err := NewArchiveExtractedBuilder(testArchiveMctx(fakeCmd, fakeFile), modschema.DecodeOptions{})(id, cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return s.(*ArchiveExtracted)
}

func TestParseSourceHash(t *testing.T) {
	a64, b40, c128, d32 := strings.Repeat("a", 64), strings.Repeat("b", 40), strings.Repeat("c", 128), strings.Repeat("d", 32)
	ok := []struct{ in, algo, hex string }{
		{"sha256=" + a64, "sha256", a64},
		{"sha256:" + a64, "sha256", a64},
		{"SHA256=" + strings.ToUpper(a64), "sha256", a64},
		{"sha1=" + b40, "sha1", b40},
		{"sha512=" + c128, "sha512", c128},
		{"md5=" + d32, "md5", d32},
		{a64, "sha256", a64},
		{b40, "sha1", b40},
		{c128, "sha512", c128},
		{d32, "md5", d32},
		{"sha256 = " + a64, "sha256", a64}, // tolerant of spaces around the separator
	}
	for _, tc := range ok {
		algo, h, err := parseSourceHash(tc.in)
		if err != nil {
			t.Errorf("parseSourceHash(%q): unexpected error %v", tc.in, err)
			continue
		}
		if algo != tc.algo || h != tc.hex {
			t.Errorf("parseSourceHash(%q) = %s/%s, want %s/%s", tc.in, algo, h, tc.algo, tc.hex)
		}
	}
	bad := []string{
		"v2.0.0",
		"sha256=",
		"sha256=" + b40,                  // wrong length for the algorithm
		"sha256=" + a64 + "zz",           // non-hex
		"crc32=deadbeef",                 // unsupported algorithm
		"abcde",                          // odd-length bare
		"deadbeef",                       // even-length bare but matches no digest length
		"=" + a64,                        // empty algorithm
		"https://example.com/SHA256SUMS", // Salt's hash-file URL form is not fetched
	}
	for _, in := range bad {
		if _, _, err := parseSourceHash(in); err == nil {
			t.Errorf("parseSourceHash(%q): expected error, got nil", in)
		}
	}
}

func TestArchiveExtractedBuilderInsecureTransportGate(t *testing.T) {
	sha := "sha256=" + digestOf("sha256", archiveBytes)
	cases := []struct {
		name    string
		cfg     map[string]any
		wantErr string // "" = must build
	}{
		{"http without hash is refused", map[string]any{"source": "http://mirror.example.com/app.tar.gz"}, "plain http"},
		{"HTTP uppercase scheme is refused too", map[string]any{"source": "HTTP://mirror.example.com/app.tar.gz"}, "plain http"},
		{"ftp without hash is refused", map[string]any{"source": "ftp://mirror.example.com/app.tar.gz"}, "plain ftp"},
		{"http with skip_verify builds", map[string]any{"source": "http://mirror.example.com/app.tar.gz", "skip_verify": true}, ""},
		{"http with a verifiable hash builds", map[string]any{"source": "http://mirror.example.com/app.tar.gz", "source_hash": sha}, ""},
		{"http with an unparseable hash is refused", map[string]any{"source": "http://mirror.example.com/app.tar.gz", "source_hash": "v2"}, "source_hash"},
		{"https without hash builds", map[string]any{"source": "https://mirror.example.com/app.tar.gz"}, ""},
		{"https with hash builds", map[string]any{"source": "https://mirror.example.com/app.tar.gz", "source_hash": sha}, ""},
		{"local path without hash builds", map[string]any{"source": "/tmp/app.tar.gz"}, ""},
		{"local path with unparseable hash is refused", map[string]any{"source": "/tmp/app.tar.gz", "source_hash": "release-2"}, "source_hash"},
		{"local path with unparseable hash and skip_verify builds", map[string]any{"source": "/tmp/app.tar.gz", "source_hash": "release-2", "skip_verify": true}, ""},
	}
	for _, tc := range cases {
		_, err := NewArchiveExtractedBuilder(testArchiveMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec()), modschema.DecodeOptions{})("/opt/app", tc.cfg)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected builder error %v", tc.name, err)
		case tc.wantErr != "" && err == nil:
			t.Errorf("%s: expected builder error containing %q, got nil", tc.name, tc.wantErr)
		case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestArchiveExtractedUnparseableHashIsTypedValueInvalid(t *testing.T) {
	_, err := NewArchiveExtractedBuilder(testArchiveMctx(exectest.NewFakeCommandExec(), exectest.NewFakeFileExec()), modschema.DecodeOptions{})("/opt/app", map[string]any{
		"source": "/tmp/app.tar.gz", "source_hash": "v2.0.0",
	})
	var fe *modschema.FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("expected a *modschema.FieldError, got %T: %v", err, err)
	}
	if fe.Kind != modschema.ErrValueInvalid || fe.Param != "source_hash" {
		t.Errorf("FieldError = %+v, want value_invalid on source_hash", fe)
	}

	// skip_verify opts the same value down to an opaque marker.
	s := buildArchive(t, exectest.NewFakeCommandExec(), exectest.NewFakeFileExec(), "/opt/app", map[string]any{
		"source": "/tmp/app.tar.gz", "source_hash": "v2.0.0", "skip_verify": true,
	})
	if s.SourceHash != "v2.0.0" || s.HashAlgo != "" || s.HashHex != "" {
		t.Errorf("skip_verify: got SourceHash=%q HashAlgo=%q HashHex=%q, want opaque marker only", s.SourceHash, s.HashAlgo, s.HashHex)
	}
}

func TestArchiveExtractedVerifiesLocalArchive(t *testing.T) {
	ctx := context.Background()
	forms := map[string]string{
		"sha256-equals": "sha256=" + digestOf("sha256", archiveBytes),
		"sha256-colon":  "sha256:" + digestOf("sha256", archiveBytes),
		"sha256-upper":  "SHA256=" + strings.ToUpper(digestOf("sha256", archiveBytes)),
		"bare-sha256":   digestOf("sha256", archiveBytes),
		"sha1":          "sha1=" + digestOf("sha1", archiveBytes),
		"sha512":        "sha512=" + digestOf("sha512", archiveBytes),
		"md5":           "md5=" + digestOf("md5", archiveBytes),
		"bare-md5":      digestOf("md5", archiveBytes),
	}
	for name, declared := range forms {
		t.Run(name, func(t *testing.T) {
			fakeCmd := exectest.NewFakeCommandExec()
			fakeFile := exectest.NewFakeFileExec()
			fakeFile.PreCreate("/tmp/app.tar.gz", archiveBytes, 0644)
			s := buildArchive(t, fakeCmd, fakeFile, "/opt/app", map[string]any{
				"source": "/tmp/app.tar.gz", "source_hash": declared, "makedirs": true,
			})
			ar, err := s.Apply(ctx)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !ar.Changed {
				t.Error("expected Changed after extraction")
			}
			calls := fakeCmd.Calls()
			if len(calls) != 1 || calls[0].Command != "tar" {
				t.Fatalf("expected exactly one tar call, got %v", calls)
			}
			marker, ok := fakeFile.GetFile(s.markerPath())
			if !ok {
				t.Fatal("expected marker after a verified extraction")
			}
			if strings.TrimSpace(string(marker)) != declared {
				t.Errorf("marker records %q, want the declaration verbatim %q", marker, declared)
			}
			cr, err := s.Check(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if cr.NeedsChange {
				t.Errorf("expected convergence, diff: %s", cr.Diff)
			}
		})
	}
}

func TestArchiveExtractedHashMismatchRefusesToExtract(t *testing.T) {
	ctx := context.Background()
	wrong := "sha256=" + strings.Repeat("0", 64)

	t.Run("local source", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeFile := exectest.NewFakeFileExec()
		fakeFile.PreCreate("/tmp/app.tar.gz", archiveBytes, 0644)
		s := buildArchive(t, fakeCmd, fakeFile, "/opt/app", map[string]any{
			"source": "/tmp/app.tar.gz", "source_hash": wrong, "makedirs": true,
		})
		_, err := s.Apply(ctx)
		if err == nil || !strings.Contains(err.Error(), "source_hash mismatch") {
			t.Fatalf("expected a source_hash mismatch error, got %v", err)
		}
		if n := fakeCmd.CallCount(); n != 0 {
			t.Errorf("tar must not run on a mismatch, got %d command calls: %v", n, fakeCmd.Calls())
		}
		if _, ok := fakeFile.GetFile(s.markerPath()); ok {
			t.Error("marker must not be written on a mismatch")
		}
		cr, err := s.Check(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !cr.NeedsChange {
			t.Error("state must still need a change after a refused extraction")
		}
	})

	t.Run("remote source", func(t *testing.T) {
		fakeCmd := exectest.NewFakeCommandExec()
		fakeFile := exectest.NewFakeFileExec()
		// The download is a faked shell call that writes nothing; seed the
		// temp file the module would hash as if curl had fetched it.
		tmp := filepath.Join(os.TempDir(), "zester-archive-app.tar.gz")
		fakeFile.PreCreate(tmp, archiveBytes, 0644)
		s := buildArchive(t, fakeCmd, fakeFile, "/opt/app", map[string]any{
			"source": "https://releases.example.com/app.tar.gz", "source_hash": wrong,
		})
		_, err := s.Apply(ctx)
		if err == nil || !strings.Contains(err.Error(), "source_hash mismatch") {
			t.Fatalf("expected a source_hash mismatch error, got %v", err)
		}
		calls := fakeCmd.Calls()
		if len(calls) != 1 || !calls[0].Shell {
			t.Fatalf("expected only the download call, got %v", calls)
		}
		if _, ok := fakeFile.GetFile(s.markerPath()); ok {
			t.Error("marker must not be written on a mismatch")
		}
	})
}

func TestArchiveExtractedVerifiesDownloadedArchive(t *testing.T) {
	ctx := context.Background()
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	tmp := filepath.Join(os.TempDir(), "zester-archive-app.tar.gz")
	fakeFile.PreCreate(tmp, archiveBytes, 0644)

	// Plain http is acceptable WITH a verifiable hash — the hash is what
	// makes the download trustworthy.
	s := buildArchive(t, fakeCmd, fakeFile, "/opt/app", map[string]any{
		"source":      "http://mirror.example.com/app.tar.gz",
		"source_hash": "sha256=" + digestOf("sha256", archiveBytes),
		"makedirs":    true,
	})
	if _, err := s.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	calls := fakeCmd.Calls()
	if len(calls) != 2 || !calls[0].Shell || calls[1].Command != "tar" {
		t.Fatalf("expected download then tar, got %v", calls)
	}
	if calls[1].Args[1] != tmp {
		t.Errorf("tar must extract the VERIFIED temp file %s, got %v", tmp, calls[1].Args)
	}
	if _, ok := fakeFile.GetFile(s.markerPath()); !ok {
		t.Error("expected marker after a verified extraction")
	}
}

func TestArchiveExtractedMissingSourceFailsVerification(t *testing.T) {
	// A declared hash with no readable archive is a verification failure,
	// not a silent skip: tar never runs.
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	s := buildArchive(t, fakeCmd, fakeFile, "/opt/app", map[string]any{
		"source": "/tmp/missing.tar.gz", "source_hash": "sha256=" + digestOf("sha256", archiveBytes), "makedirs": true,
	})
	if _, err := s.Apply(context.Background()); err == nil {
		t.Fatal("expected Apply to fail when the archive cannot be opened for hashing")
	}
	if n := fakeCmd.CallCount(); n != 0 {
		t.Errorf("tar must not run when verification could not read the archive, got %d calls", n)
	}
}

func TestArchiveExtractedSkipVerifyBypassesByteCheck(t *testing.T) {
	// skip_verify: the archive is NOT hashed (it does not even exist in the
	// fake FS, so a hash attempt would fail), the declaration is recorded as
	// an opaque marker, and extraction proceeds.
	ctx := context.Background()
	fakeCmd := exectest.NewFakeCommandExec()
	fakeFile := exectest.NewFakeFileExec()
	s := buildArchive(t, fakeCmd, fakeFile, "/opt/app", map[string]any{
		"source":      "/tmp/app.tar.gz",
		"source_hash": "sha256=" + strings.Repeat("0", 64), // would mismatch if checked
		"skip_verify": true,
		"makedirs":    true,
	})
	if s.HashAlgo != "" {
		t.Fatalf("skip_verify must leave HashAlgo empty, got %q", s.HashAlgo)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if calls := fakeCmd.Calls(); len(calls) != 1 || calls[0].Command != "tar" {
		t.Fatalf("expected one tar call, got %v", calls)
	}
	marker, ok := fakeFile.GetFile(s.markerPath())
	if !ok || strings.TrimSpace(string(marker)) != "sha256="+strings.Repeat("0", 64) {
		t.Errorf("marker must record the declaration verbatim, got %q (ok=%v)", marker, ok)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected convergence, diff: %s", cr.Diff)
	}
}
