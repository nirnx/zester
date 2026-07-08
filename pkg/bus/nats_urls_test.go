package bus

import (
	"crypto/tls"
	"testing"
)

func TestNormalizeNATSURLs(t *testing.T) {
	in := []string{" tls://a:4222, tls://b:4222 ", "tls://c:4222", "", " , "}
	got := NormalizeNATSURLs(in)
	want := []string{"tls://a:4222", "tls://b:4222", "tls://c:4222"}

	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestValidateTLSNATSURLs(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		wantErr bool
	}{
		{name: "valid single", in: []string{"tls://nats:4222"}},
		{name: "valid csv", in: []string{"tls://nats:4222,tls://nats-2:4222"}},
		{name: "valid mixed entries", in: []string{"tls://nats:4222", "tls://nats-2:4222"}},
		{name: "empty", in: nil, wantErr: true},
		{name: "missing scheme", in: []string{"nats:4222"}, wantErr: true},
		{name: "plaintext nats scheme", in: []string{"nats://nats:4222"}, wantErr: true},
		{name: "http scheme", in: []string{"http://nats:4222"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTLSNATSURLs(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestResolveNATSCAFile(t *testing.T) {
	t.Setenv("NATS_CA_FILE", "")

	// Explicit path wins and is strict.
	p, optional := ResolveNATSCAFile("/explicit/ca.crt", "/auth/dir")
	if p != "/explicit/ca.crt" || optional {
		t.Errorf("explicit: got (%q, %v), want (/explicit/ca.crt, strict)", p, optional)
	}

	// Env var beats the conventional candidate and is strict.
	t.Setenv("NATS_CA_FILE", "/env/ca.crt")
	p, optional = ResolveNATSCAFile("", "/auth/dir")
	if p != "/env/ca.crt" || optional {
		t.Errorf("env: got (%q, %v), want (/env/ca.crt, strict)", p, optional)
	}

	// authDir supplies the conventional candidate, optional semantics.
	t.Setenv("NATS_CA_FILE", "")
	p, optional = ResolveNATSCAFile("", "/auth/dir")
	if p != "/auth/dir/nats-ca.crt" || !optional {
		t.Errorf("authDir: got (%q, %v), want (/auth/dir/nats-ca.crt, optional)", p, optional)
	}

	// No authDir: the conventional default path, optional semantics.
	p, optional = ResolveNATSCAFile("", "")
	if p != DefaultNATSCAPath || !optional {
		t.Errorf("default: got (%q, %v), want (%q, optional)", p, optional, DefaultNATSCAPath)
	}
}

func TestNATSClientTLS(t *testing.T) {
	t.Setenv("NATS_CA_FILE", "")

	// Non-TLS URLs yield no TLS config at all.
	cfg, caFile, optional := NATSClientTLS([]string{"nats://host:4222"}, "", "")
	if cfg != nil || caFile != "" || optional {
		t.Errorf("non-tls: got (%v, %q, %v), want (nil, \"\", false)", cfg, caFile, optional)
	}

	// TLS URLs get the 1.3 floor plus the resolved CA path; the file is
	// deliberately NOT read or stat'd here (per-connect callback does that).
	cfg, caFile, optional = NATSClientTLS([]string{"tls://host:4222"}, "/does/not/exist.crt", "")
	if cfg == nil || cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("tls: config = %+v, want MinVersion TLS1.3", cfg)
	}
	if caFile != "/does/not/exist.crt" || optional {
		t.Errorf("tls: got (%q, %v), want (/does/not/exist.crt, strict)", caFile, optional)
	}
}

func TestValidateAdvertisableNATSURLs(t *testing.T) {
	accepted, rejected := ValidateAdvertisableNATSURLs([]string{
		"tls://nats1.example:4222",
		"tls://nats2.example:4222",
		"tls://nats1.example:4222", // dup
		"tls://localhost:4222",
		"tls://127.0.0.1:4222",
		"tls://[::1]:4222",
		"tls://[::ffff:127.0.0.1]:4222",
		"tls://0.0.0.0:4222",
		"tls://0.1.2.3:4222",
		"tls://:4222", // empty host
		"tls://169.254.1.1:4222",
		"tls://127.1:4222",          // numeric loopback shorthand
		"tls://localhost.:4222",     // FQDN trailing dot
		"tls://sub.localhost.:4222", // FQDN trailing dot subdomain
		"tls://0x7f000001:4222",     // hex inet_aton
		"nats://nats3.example:4222", // not tls
	})

	want := []string{"tls://nats1.example:4222", "tls://nats2.example:4222"}
	if len(accepted) != len(want) {
		t.Fatalf("accepted = %v, want %v", accepted, want)
	}
	for i := range want {
		if accepted[i] != want[i] {
			t.Errorf("accepted[%d] = %q, want %q", i, accepted[i], want[i])
		}
	}
	for _, bad := range []string{
		"tls://localhost:4222", "tls://127.0.0.1:4222", "tls://[::1]:4222",
		"tls://[::ffff:127.0.0.1]:4222", "tls://0.0.0.0:4222", "tls://0.1.2.3:4222",
		"tls://:4222", "tls://169.254.1.1:4222", "tls://127.1:4222",
		"tls://localhost.:4222", "tls://sub.localhost.:4222", "tls://0x7f000001:4222",
		"nats://nats3.example:4222",
	} {
		if _, ok := rejected[bad]; !ok {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}
