package bus

import "testing"

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
