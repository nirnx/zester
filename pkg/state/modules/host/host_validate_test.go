package hostmod

import (
	"context"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
)

// TestHostPresentRejectsInvalidIP pins the builder-tail `ip` validation: the
// value lands verbatim in the first column of a hosts line, so anything that
// is not a parseable address — including a payload with an embedded newline
// that would inject a whole extra mapping — must fail the build.
func TestHostPresentRejectsInvalidIP(t *testing.T) {
	build := NewHostPresentBuilder(testHostMctx(exectest.NewFakeFileExec()), modschema.DecodeOptions{})
	bad := []string{
		"not-an-ip",
		"10.0.0.5\n10.0.0.9\tevil.example.com",
		"10.0.0.5 evil",
		"10.0.0",
		"300.1.1.1",
		"123", // the BD-6 numeric coercion decodes to "123" — not an address
		" 10.0.0.5",
	}
	for _, ip := range bad {
		_, err := build("web1", map[string]any{"ip": ip})
		if err == nil {
			t.Errorf("ip %q: expected builder error, got nil", ip)
			continue
		}
		if !strings.Contains(err.Error(), "not a valid IP address") {
			t.Errorf("ip %q: error %q should say the IP is invalid", ip, err)
		}
	}
	for _, ip := range []string{"10.0.0.5", "127.0.0.1", "::1", "fe80::1", "2001:db8::42"} {
		if _, err := build("web1", map[string]any{"ip": ip}); err != nil {
			t.Errorf("ip %q: unexpected builder error %v", ip, err)
		}
	}
}

// TestHostFamilyRejectsWhitespaceHostname pins the hostname validation on
// BOTH members: a hostname is one whitespace-delimited token of the hosts
// file; a space would split it into two names and a newline would inject a
// line.
func TestHostFamilyRejectsWhitespaceHostname(t *testing.T) {
	present := NewHostPresentBuilder(testHostMctx(exectest.NewFakeFileExec()), modschema.DecodeOptions{})
	absent := NewHostAbsentBuilder(testHostMctx(exectest.NewFakeFileExec()), modschema.DecodeOptions{})

	bad := []string{"web 01", "web01\n10.0.0.9 evil", "web01\t", " web01", "web\x0001"}
	for _, name := range bad {
		if _, err := present("x", map[string]any{"name": name, "ip": "10.0.0.5"}); err == nil {
			t.Errorf("host.present name %q: expected builder error, got nil", name)
		}
		if _, err := present(name, map[string]any{"ip": "10.0.0.5"}); err == nil {
			t.Errorf("host.present id %q: expected builder error, got nil", name)
		}
		if _, err := absent("x", map[string]any{"name": name}); err == nil {
			t.Errorf("host.absent name %q: expected builder error, got nil", name)
		}
		if _, err := absent(name, map[string]any{}); err == nil {
			t.Errorf("host.absent id %q: expected builder error, got nil", name)
		}
	}
	for _, name := range []string{"web01", "web01.example.com", "db-primary", "host_1"} {
		if _, err := present(name, map[string]any{"ip": "10.0.0.5"}); err != nil {
			t.Errorf("host.present %q: unexpected error %v", name, err)
		}
		if _, err := absent(name, map[string]any{}); err != nil {
			t.Errorf("host.absent %q: unexpected error %v", name, err)
		}
	}
}

// TestHostPresentInjectionNeverReachesFile is the end-to-end guard: with a
// hosts file in place, an injecting value must never be written.
func TestHostPresentInjectionNeverReachesFile(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	orig := []byte("127.0.0.1\tlocalhost\n")
	fakeFile.PreCreate("/etc/hosts", orig, 0644)
	build := NewHostPresentBuilder(testHostMctx(fakeFile), modschema.DecodeOptions{})

	s, err := build("web1\n10.0.0.9\tevil.example.com", map[string]any{"ip": "10.0.0.5"})
	if err == nil {
		// Belt and braces: even if a build slipped through, prove nothing ran.
		if _, applyErr := s.Apply(context.Background()); applyErr == nil {
			t.Fatal("expected the injecting hostname to be refused")
		}
	}
	got, _ := fakeFile.GetFile("/etc/hosts")
	if string(got) != string(orig) {
		t.Fatalf("hosts file was modified:\n%s", got)
	}
}
