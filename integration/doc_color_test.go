//go:build integration

package integration

import (
	"regexp"
	"strings"
	"testing"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// TestSysDocColor_ForceColor pins the colored sys.doc experience end to end:
// `zester --force-color '<peel>' sys.doc pkg.installed` colorizes the doc
// client-side (the container exec has no TTY, so --force-color is the only
// path to color here — exactly the CI/pipe use case it exists for), and
// stripping the ANSI codes reproduces the plain --no-color run byte-for-byte:
// color is presentation only, never content.
func TestSysDocColor_ForceColor(t *testing.T) {
	colored := execInContainer(t, "admin",
		[]string{"zester", "--force-color", "--direct", "web-01", "sys.doc", "pkg.installed"})
	if !strings.Contains(colored, "\x1b[") {
		t.Fatalf("--force-color sys.doc output has no ANSI codes:\n%s", colored)
	}
	// The module header is colored via the RenderText grammar (bold cyan).
	if !strings.Contains(colored, "\x1b[1;36mpkg.installed\x1b[0m") {
		t.Errorf("colored sys.doc missing the colored module header:\n%.400s", colored)
	}

	plain := execInContainer(t, "admin",
		[]string{"zester", "--no-color", "--direct", "web-01", "sys.doc", "pkg.installed"})
	if strings.Contains(plain, "\x1b[") {
		t.Fatalf("--no-color sys.doc output contains ANSI codes:\n%q", plain)
	}
	if got := stripANSI(colored); got != plain {
		t.Errorf("stripping ANSI from the colored run does not reproduce the plain run\n got: %q\nwant: %q", got, plain)
	}
}

// TestZesterDocColor_Offline pins the same for the offline `zester doc`
// surface: --force-color colorizes the embedded render, stripping reproduces
// the plain run.
func TestZesterDocColor_Offline(t *testing.T) {
	colored := execInContainer(t, "admin", []string{"zester", "doc", "file.managed", "--force-color"})
	if !strings.Contains(colored, "\x1b[") {
		t.Fatalf("--force-color zester doc output has no ANSI codes:\n%s", colored)
	}

	plain := execInContainer(t, "admin", []string{"zester", "doc", "file.managed", "--no-color"})
	if strings.Contains(plain, "\x1b[") {
		t.Fatalf("--no-color zester doc output contains ANSI codes:\n%q", plain)
	}
	if got := stripANSI(colored); got != plain {
		t.Errorf("stripping ANSI from the colored run does not reproduce the plain run\n got: %q\nwant: %q", got, plain)
	}
}
