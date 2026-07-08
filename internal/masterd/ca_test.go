package masterd

import (
	"context"
	"crypto/x509"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nirnx/zester/internal/config"
	"github.com/nirnx/zester/internal/health"
	"github.com/nirnx/zester/pkg/ca"
)

func newCATestDaemon(t *testing.T, cfg config.MasterCA, authDir string) *Daemon {
	t.Helper()
	full := config.MasterDaemonDefaults()
	full.CA = cfg
	full.AuthDir = authDir
	d := &Daemon{cfg: &full, logger: slog.New(slog.NewTextHandler(nopWriter{}, nil))}
	return d
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestResolveCAMode(t *testing.T) {
	dir := t.TempDir()

	// auto with no material -> external
	d := newCATestDaemon(t, config.MasterCA{Mode: "auto", Dir: filepath.Join(dir, "ca")}, dir)
	if m, _ := d.resolveCAMode(); m != caModeExternal {
		t.Errorf("auto+absent = %v, want external", m)
	}

	// generate CA material, auto now -> embedded
	authority, err := ca.Generate(ca.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Save(filepath.Join(dir, "ca")); err != nil {
		t.Fatal(err)
	}
	if m, _ := d.resolveCAMode(); m != caModeEmbedded {
		t.Errorf("auto+present = %v, want embedded", m)
	}

	// explicit external ignores present material
	d.cfg.CA.Mode = "external"
	if m, _ := d.resolveCAMode(); m != caModeExternal {
		t.Errorf("explicit external = %v, want external", m)
	}

	// invalid mode errors
	d.cfg.CA.Mode = "bogus"
	if _, err := d.resolveCAMode(); err == nil {
		t.Error("invalid mode did not error")
	}
}

func TestStartCA_EmbeddedIssuesAndServesLeaf(t *testing.T) {
	dir := t.TempDir()
	authority, err := ca.Generate(ca.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Save(filepath.Join(dir, "ca")); err != nil {
		t.Fatal(err)
	}

	d := newCATestDaemon(t, config.MasterCA{
		Mode:               "embedded",
		Dir:                filepath.Join(dir, "ca"),
		EnrollCertValidity: config.Duration(90 * 24 * time.Hour),
		EnrollSANs:         []string{"master.example", "10.0.0.1"},
	}, dir)
	d.checker = health.New("test", time.Second)

	getCert, err := d.startCA(context.Background())
	if err != nil {
		t.Fatalf("startCA: %v", err)
	}
	if getCert == nil {
		t.Fatal("embedded mode returned nil GetCertificate")
	}

	cert, err := getCert(nil)
	if err != nil {
		t.Fatalf("getCertificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}

	// The served leaf carries the hostname/localhost + operator SANs and
	// verifies against the CA root.
	if err := verifyLeafDNS(leaf, "localhost"); err != nil {
		t.Errorf("leaf missing localhost SAN: %v", err)
	}
	if err := verifyLeafDNS(leaf, "master.example"); err != nil {
		t.Errorf("leaf missing operator DNS SAN: %v", err)
	}
	foundIP := false
	for _, ip := range leaf.IPAddresses {
		if ip.String() == "10.0.0.1" {
			foundIP = true
		}
	}
	if !foundIP {
		t.Error("leaf missing operator IP SAN")
	}

	roots := x509.NewCertPool()
	roots.AddCert(authority.Root)
	inters := x509.NewCertPool()
	inters.AddCert(authority.Intermediate)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inters, DNSName: "master.example"}); err != nil {
		t.Errorf("served leaf does not verify against CA root: %v", err)
	}

	// 'ca' readiness is OK with a freshly issued leaf.
	if got := d.caCheck(context.Background()); got.Status != health.StatusOK {
		t.Errorf("caCheck = %v, want OK", got.Status)
	}
}

func TestStartCA_EmbeddedMissingMaterialIsFatal(t *testing.T) {
	dir := t.TempDir()
	d := newCATestDaemon(t, config.MasterCA{Mode: "embedded", Dir: filepath.Join(dir, "ca")}, dir)
	d.checker = health.New("test", time.Second)
	if _, err := d.startCA(context.Background()); err == nil {
		t.Fatal("embedded mode with absent CA material must be fatal")
	}
}

func verifyLeafDNS(leaf *x509.Certificate, name string) error {
	return leaf.VerifyHostname(name)
}
