package cmd

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/ca"
)

// TestCACommands exercises the offline `zester ca` verbs end to end on a
// temp directory: init → refuse re-init → issue nats-server → the issued
// chain verifies against the root anchor only.
func TestCACommands(t *testing.T) {
	oldDir := caDir
	t.Cleanup(func() { caDir = oldDir })
	caDir = filepath.Join(t.TempDir(), "ca")

	// init
	if err := caInitCmd.RunE(caInitCmd, nil); err != nil {
		t.Fatalf("ca init: %v", err)
	}
	if !ca.Exists(caDir) {
		t.Fatal("ca init left no CA material")
	}

	// init must refuse to overwrite.
	if err := caInitCmd.RunE(caInitCmd, nil); err == nil {
		t.Fatal("second ca init succeeded, want refusal")
	}

	// The pin printed by fingerprint is the loaded authority's root pin.
	authority, err := ca.Load(caDir)
	if err != nil {
		t.Fatalf("load generated CA: %v", err)
	}
	if _, err := ca.NormalizePin(authority.RootSPKIPin()); err != nil {
		t.Fatalf("generated pin is not canonical: %v", err)
	}

	// issue nats-server without SANs is refused.
	out := t.TempDir()
	if err := caIssueCmd.Flags().Set("out", out); err != nil {
		t.Fatal(err)
	}
	if err := caIssueCmd.RunE(caIssueCmd, []string{"nats-server"}); err == nil {
		t.Fatal("issue nats-server without SANs succeeded, want error")
	}

	// issue nats-server with SANs writes a chain that verifies against the
	// root only (the trust model peels use).
	if err := caIssueCmd.Flags().Set("dns", "nats,localhost"); err != nil {
		t.Fatal(err)
	}
	if err := caIssueCmd.RunE(caIssueCmd, []string{"nats-server"}); err != nil {
		t.Fatalf("issue nats-server: %v", err)
	}
	certPEM, err := os.ReadFile(filepath.Join(out, "nats-server.crt"))
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(filepath.Join(out, "nats-server.key"))
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("issued pair does not load: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(authority.Root)
	inters := x509.NewCertPool()
	for _, der := range pair.Certificate[1:] {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		inters.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inters,
		DNSName:       "nats",
	}); err != nil {
		t.Fatalf("issued chain does not verify against the root anchor: %v", err)
	}

	// unknown profile is refused.
	if err := caIssueCmd.RunE(caIssueCmd, []string{"bogus"}); err == nil {
		t.Fatal("unknown profile succeeded, want error")
	}
}
