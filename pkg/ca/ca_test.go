package ca

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testAuthority(t *testing.T) *Authority {
	t.Helper()
	a, err := Generate(Config{Organization: "TestOrg"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return a
}

func TestGenerate_Hierarchy(t *testing.T) {
	a := testAuthority(t)

	if !a.Root.IsCA || a.Root.MaxPathLen != 1 {
		t.Errorf("root: IsCA=%v MaxPathLen=%d, want CA with pathlen 1", a.Root.IsCA, a.Root.MaxPathLen)
	}
	if !a.Intermediate.IsCA || !a.Intermediate.MaxPathLenZero {
		t.Errorf("intermediate: IsCA=%v MaxPathLenZero=%v, want signing-only CA", a.Intermediate.IsCA, a.Intermediate.MaxPathLenZero)
	}
	// Intermediate chains to root.
	roots := x509.NewCertPool()
	roots.AddCert(a.Root)
	if _, err := a.Intermediate.Verify(x509.VerifyOptions{Roots: roots}); err != nil {
		t.Errorf("intermediate does not verify against root: %v", err)
	}
	// Clock-skew backdating.
	if a.Root.NotBefore.After(time.Now().Add(-time.Hour)) {
		t.Errorf("root NotBefore %v not backdated", a.Root.NotBefore)
	}
	// Distinct random serials.
	if a.Root.SerialNumber.Cmp(a.Intermediate.SerialNumber) == 0 {
		t.Error("root and intermediate share a serial")
	}
}

func TestIssueServer_ChainVerifiesAgainstRootOnly(t *testing.T) {
	a := testAuthority(t)

	leaf, err := a.IssueServer("nats", []string{"nats", "localhost"}, []net.IP{net.ParseIP("10.0.0.5")}, time.Hour)
	if err != nil {
		t.Fatalf("IssueServer: %v", err)
	}

	// The served chain must satisfy a client that trusts ONLY the root
	// (peels never receive the intermediate as an anchor).
	cert, err := tls.X509KeyPair(leaf.CertPEM, leaf.KeyPEM)
	if err != nil {
		t.Fatalf("issued pair does not load as a TLS keypair: %v", err)
	}
	if len(cert.Certificate) != 2 {
		t.Fatalf("served chain has %d certs, want leaf+intermediate", len(cert.Certificate))
	}

	roots := x509.NewCertPool()
	roots.AddCert(a.Root)
	inters := x509.NewCertPool()
	inters.AddCert(a.Intermediate)
	if _, err := leaf.Cert.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inters,
		DNSName:       "nats",
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("leaf does not verify against root anchor: %v", err)
	}

	// Both EKUs present (routes/mTLS reuse the same profile).
	hasClient := false
	for _, eku := range leaf.Cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			hasClient = true
		}
	}
	if !hasClient {
		t.Error("issued leaf lacks ClientAuth EKU")
	}
	if len(leaf.Cert.SubjectKeyId) == 0 {
		t.Error("issued leaf lacks a SubjectKeyId")
	}
}

func TestSaveLoad_RoundTripAndPerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	a := testAuthority(t)
	if err := a.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Keys are private, certs public.
	for name, want := range map[string]os.FileMode{
		RootKeyFile:          0600,
		IntermediateKeyFile:  0600,
		RootCertFile:         0644,
		IntermediateCertFile: 0644,
	} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", name, fi.Mode().Perm(), want)
		}
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.RootSPKIPin() != a.RootSPKIPin() {
		t.Error("loaded root pin differs from generated")
	}
	if loaded.IntermediateKey == nil {
		t.Fatal("intermediate key not loaded")
	}
	if _, err := loaded.IssueServer("x", []string{"x"}, nil, time.Hour); err != nil {
		t.Fatalf("issuance with loaded authority: %v", err)
	}

	// Overwrite protection.
	if err := a.Save(dir); !errors.Is(err, ErrExists) {
		t.Errorf("second Save = %v, want ErrExists", err)
	}
}

func TestLoad_RootOfflineMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	a := testAuthority(t)
	if err := a.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, RootKeyFile)); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load in root-offline mode: %v", err)
	}
	if loaded.RootKey != nil {
		t.Error("RootKey should be nil in root-offline mode")
	}
	// Issuance still works — leaves are signed by the intermediate.
	if _, err := loaded.IssueServer("x", []string{"x"}, nil, time.Hour); err != nil {
		t.Fatalf("issuance in root-offline mode: %v", err)
	}
}

func TestLoad_NotFound(t *testing.T) {
	if _, err := Load(t.TempDir()); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load(empty) = %v, want ErrNotFound", err)
	}
}

func TestLoadOrGenerate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")

	a, generated, err := LoadOrGenerate(dir, Config{})
	if err != nil || !generated {
		t.Fatalf("first LoadOrGenerate: generated=%v err=%v", generated, err)
	}
	b, generated, err := LoadOrGenerate(dir, Config{})
	if err != nil || generated {
		t.Fatalf("second LoadOrGenerate: generated=%v err=%v", generated, err)
	}
	if a.RootSPKIPin() != b.RootSPKIPin() {
		t.Error("second LoadOrGenerate returned a different CA")
	}
}

// parseChainPEM splits a served CertPEM (leaf + intermediate) into the leaf
// and an Intermediates pool, exactly as a TLS client sees the presented chain.
func parseChainPEM(t *testing.T, chainPEM []byte) (*x509.Certificate, *x509.CertPool) {
	t.Helper()
	var certs []*x509.Certificate
	rest := chainPEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("parse chain cert: %v", err)
		}
		certs = append(certs, c)
	}
	if len(certs) < 2 {
		t.Fatalf("served chain has %d certs, want leaf + intermediate", len(certs))
	}
	inters := x509.NewCertPool()
	for _, c := range certs[1:] {
		inters.AddCert(c)
	}
	return certs[0], inters
}

// verifyAgainstRoot verifies a served chain against a pool holding ONLY the
// given root — the peel's trust anchor.
func verifyAgainstRoot(t *testing.T, root *x509.Certificate, chainPEM []byte, dnsName string) error {
	t.Helper()
	leaf, inters := parseChainPEM(t, chainPEM)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inters, DNSName: dnsName})
	return err
}

// TestRotateIntermediate_InvisibleToRootAnchors pins the two properties that
// make the runbook's "intermediate rotation (routine)" fleet-invisible:
// leaves issued by BOTH the old and the new intermediate verify against the
// ORIGINAL root (the anchor every peel persisted / pinned), and the root
// SPKI pin is unchanged.
func TestRotateIntermediate_InvisibleToRootAnchors(t *testing.T) {
	a1 := testAuthority(t)
	oldLeaf, err := a1.IssueServer("srv", []string{"srv.example"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("issue pre-rotation leaf: %v", err)
	}

	a2, err := a1.RotateIntermediate(Config{Organization: "TestOrg"})
	if err != nil {
		t.Fatalf("RotateIntermediate: %v", err)
	}
	if a2.Intermediate.SerialNumber.Cmp(a1.Intermediate.SerialNumber) == 0 {
		t.Fatal("rotation did not mint a new intermediate")
	}
	newLeaf, err := a2.IssueServer("srv", []string{"srv.example"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("issue post-rotation leaf: %v", err)
	}

	// Both chains verify against the ORIGINAL root anchor — no peel-side
	// action, and in-flight old chains stay valid during the rollover.
	if err := verifyAgainstRoot(t, a1.Root, oldLeaf.CertPEM, "srv.example"); err != nil {
		t.Errorf("pre-rotation chain no longer verifies: %v", err)
	}
	if err := verifyAgainstRoot(t, a1.Root, newLeaf.CertPEM, "srv.example"); err != nil {
		t.Errorf("post-rotation chain does not verify against the original root: %v", err)
	}

	// The pin (and therefore every enroll_ca_pin and persisted anchor in the
	// fleet) is untouched.
	if a1.RootSPKIPin() != a2.RootSPKIPin() {
		t.Errorf("root pin changed across intermediate rotation: %s -> %s", a1.RootSPKIPin(), a2.RootSPKIPin())
	}
	if !a2.Root.Equal(a1.Root) {
		t.Error("rotation replaced the root certificate")
	}
}

// TestRotateIntermediate_FileDropRoundTrip simulates the runbook's on-disk
// procedure: SaveIntermediate over an existing ca.dir (Save refuses dirs
// holding a root key), then a fresh Load — the reloaded master issues leaves
// through the NEW intermediate that still verify against the original root.
func TestRotateIntermediate_FileDropRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	a1 := testAuthority(t)
	if err := a1.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	a2, err := a1.RotateIntermediate(Config{})
	if err != nil {
		t.Fatalf("RotateIntermediate: %v", err)
	}
	// Save on a dir with a root key is refused — SaveIntermediate is the
	// rotation write path.
	if err := a2.Save(dir); !errors.Is(err, ErrExists) {
		t.Errorf("Save over existing root key = %v, want ErrExists", err)
	}
	if err := a2.SaveIntermediate(dir); err != nil {
		t.Fatalf("SaveIntermediate: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load after rotation: %v", err)
	}
	if loaded.Intermediate.SerialNumber.Cmp(a2.Intermediate.SerialNumber) != 0 {
		t.Fatal("Load did not pick up the rotated intermediate")
	}
	leaf, err := loaded.IssueServer("srv", []string{"srv.example"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("issue after reload: %v", err)
	}
	if err := verifyAgainstRoot(t, a1.Root, leaf.CertPEM, "srv.example"); err != nil {
		t.Errorf("reloaded-hierarchy chain does not verify against the original root: %v", err)
	}
	if loaded.RootSPKIPin() != a1.RootSPKIPin() {
		t.Error("pin changed across the on-disk rotation")
	}
}

// TestRotateIntermediate_RootOfflineRefused: rotation needs the root key.
func TestRotateIntermediate_RootOfflineRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	a := testAuthority(t)
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, RootKeyFile)); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.RotateIntermediate(Config{}); err == nil {
		t.Error("RotateIntermediate without the root key must be refused")
	}
}
