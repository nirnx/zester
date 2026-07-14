package filemod

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
)

func TestParseFileModeSpecialBits(t *testing.T) {
	for in, want := range map[string]fs.FileMode{
		"0644": 0o644,
		"755":  0o755,
		"4755": 0o755 | fs.ModeSetuid,
		"2755": 0o755 | fs.ModeSetgid,
		"1777": 0o777 | fs.ModeSticky,
		"6750": 0o750 | fs.ModeSetuid | fs.ModeSetgid,
	} {
		got, err := parseFileMode(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got != want {
			t.Errorf("%s: got %v, want %v", in, got, want)
		}
	}
	if _, err := parseFileMode("77777"); err == nil {
		t.Error("out-of-range mode must error")
	}
	if _, err := parseFileMode("banana"); err == nil {
		t.Error("non-octal mode must error")
	}
}

// TestFileManagedSetuidConverges pins the spot-review defect: mode "4755"
// used to parse as raw 0o4755 (not ModeSetuid), which os.Chmod silently
// drops — Check reported drift forever, Apply rewrote the file every
// highstate, and the setuid bit was never actually set.
func TestFileManagedSetuidConverges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "suid-bin")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: &exec.OSFileExec{}}}
	builder := NewFileManagedBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder(path, map[string]any{"content": "#!/bin/sh\n", "mode": "4755"})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("setuid drift not detected")
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&fs.ModeSetuid == 0 {
		t.Fatal("setuid bit was not applied")
	}
	// The classic-trap walk: the second Check must be converged.
	cr, err = s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Fatalf("setuid mode did not converge: %s", cr.Diff)
	}
}
