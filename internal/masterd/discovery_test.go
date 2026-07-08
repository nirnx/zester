package masterd

import (
	"log/slog"
	"testing"

	"github.com/nirnx/zester/internal/config"
)

func newDiscoveryDaemon(advertise []string) *Daemon {
	full := config.MasterDaemonDefaults()
	full.NatsAdvertise = advertise
	return &Daemon{cfg: &full, logger: slog.New(slog.NewTextHandler(nopWriter{}, nil))}
}

func TestAdvertiseURLs_RejectsLoopback(t *testing.T) {
	d := newDiscoveryDaemon([]string{
		"tls://nats.example:4222",
		"tls://localhost:4222", // rejected
		"tls://127.0.0.1:4222", // rejected
		"tls://nats2.example:4222",
	})
	got := d.advertiseURLs()
	want := []string{"tls://nats.example:4222", "tls://nats2.example:4222"}
	if len(got) != len(want) {
		t.Fatalf("advertiseURLs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBootstrapDoc_EmptyWhenNothingConfigured(t *testing.T) {
	d := newDiscoveryDaemon(nil)
	if d.bootstrapEnabled() {
		t.Error("bootstrapEnabled with no CA and no advertise should be false")
	}
	doc := d.bootstrapDoc()
	if doc.CABundlePEM != "" || len(doc.NATSURLs) != 0 {
		t.Errorf("empty-config doc should be empty, got %+v", doc)
	}
}

func TestBootstrapDoc_AdvertiseOnly(t *testing.T) {
	d := newDiscoveryDaemon([]string{"tls://nats.example:4222"})
	if !d.bootstrapEnabled() {
		t.Error("bootstrapEnabled should be true with advertise URLs")
	}
	doc := d.bootstrapDoc()
	if len(doc.NATSURLs) != 1 || doc.NATSURLs[0] != "tls://nats.example:4222" {
		t.Errorf("doc NATSURLs = %v", doc.NATSURLs)
	}
	if doc.CABundlePEM != "" {
		t.Error("external-mode doc should carry no CA bundle")
	}
}
