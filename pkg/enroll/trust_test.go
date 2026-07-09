package enroll

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/ca"
)

// caServer starts an httptest TLS server presenting a leaf issued by the given
// authority and serving its bootstrap doc at /api/v1/enroll/ca.
func caServer(t *testing.T, authority *ca.Authority, bundleOverride []byte) *httptest.Server {
	t.Helper()
	leaf, err := authority.IssueServer("127.0.0.1", []string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 1)}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(leaf.CertPEM, leaf.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	bundle := authority.Bundle()
	if bundleOverride != nil {
		bundle = bundleOverride
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/enroll/ca" {
			_ = json.NewEncoder(w).Encode(BootstrapDoc{
				V:           BootstrapVersion,
				CABundlePEM: string(bundle),
				Fingerprint: authority.RootSPKIPin(),
				NATSURLs:    []string{"tls://nats.example:4222"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func TestResolveTrust_PinMatchesAnchorsExactly(t *testing.T) {
	authority, _ := ca.Generate(ca.Config{})
	srv := caServer(t, authority, nil)
	anchor := filepath.Join(t.TempDir(), "enroll-ca.crt")

	rt, err := ResolveTrust(TrustConfig{
		Pins:         []string{authority.RootSPKIPin()},
		AnchorFile:   anchor,
		MasterURLs:   []string{srv.URL},
		FirstContact: true,
	})
	if err != nil {
		t.Fatalf("ResolveTrust: %v", err)
	}
	if rt.Source != TrustSourcePin {
		t.Errorf("source = %v, want pin", rt.Source)
	}
	if rt.RootSPKI != authority.RootSPKIPin() {
		t.Error("wrong root pinned")
	}
	// Anchor persisted for subsequent strict boots.
	if _, err := os.Stat(anchor); err != nil {
		t.Errorf("anchor not persisted: %v", err)
	}
	// The resolved config actually verifies the server.
	if !configTrusts(t, rt.TLSConfig, srv) {
		t.Error("resolved TLS config does not trust the server it pinned")
	}
}

func TestResolveTrust_PinMismatchIsFatalNeverDowngrade(t *testing.T) {
	authority, _ := ca.Generate(ca.Config{})
	other, _ := ca.Generate(ca.Config{})
	srv := caServer(t, authority, nil)

	_, err := ResolveTrust(TrustConfig{
		Pins:         []string{other.RootSPKIPin()}, // wrong pin
		AnchorFile:   filepath.Join(t.TempDir(), "a.crt"),
		MasterURLs:   []string{srv.URL},
		FirstContact: true,
	})
	if !errors.Is(err, ErrTrustPinMismatch) {
		t.Fatalf("err = %v, want ErrTrustPinMismatch", err)
	}
}

func TestResolveTrust_BundlePoisoningRejected(t *testing.T) {
	real, _ := ca.Generate(ca.Config{})
	attacker, _ := ca.Generate(ca.Config{})
	// Server presents the REAL leaf but serves a poisoned bundle
	// (attacker root appended to the real root).
	poisoned := append(append([]byte{}, real.Bundle()...), attacker.Bundle()...)
	srv := caServer(t, real, poisoned)

	rt, err := ResolveTrust(TrustConfig{
		Pins:         []string{real.RootSPKIPin()},
		AnchorFile:   filepath.Join(t.TempDir(), "a.crt"),
		MasterURLs:   []string{srv.URL},
		FirstContact: true,
	})
	if err != nil {
		t.Fatalf("ResolveTrust: %v", err)
	}
	// Only the pinned (real) root is anchored — NOT the attacker cert.
	if rt.Anchor == nil || ca.SPKIPin(rt.Anchor) != real.RootSPKIPin() {
		t.Fatal("anchored the wrong cert from a poisoned bundle")
	}
	pool := x509.NewCertPool()
	pool.AddCert(rt.Anchor)
	// An attacker-signed leaf must NOT verify against the anchored pool.
	attackerLeaf, _ := attacker.IssueServer("x", []string{"x"}, nil, time.Hour)
	cert, _ := tls.X509KeyPair(attackerLeaf.CertPEM, attackerLeaf.KeyPEM)
	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "x"}); err == nil {
		t.Fatal("attacker leaf verified against the pinned anchor — bundle poisoning succeeded")
	}
}

func TestResolveTrust_TOFUFirstContactOnly(t *testing.T) {
	authority, _ := ca.Generate(ca.Config{})
	srv := caServer(t, authority, nil)
	anchor := filepath.Join(t.TempDir(), "enroll-ca.crt")

	// First contact TOFU: trusts and persists.
	rt, err := ResolveTrust(TrustConfig{
		Mode:         TrustModeTOFU,
		AnchorFile:   anchor,
		MasterURLs:   []string{srv.URL},
		FirstContact: true,
	})
	if err != nil {
		t.Fatalf("TOFU first contact: %v", err)
	}
	if rt.Source != TrustSourceTOFU {
		t.Errorf("source = %v, want tofu", rt.Source)
	}

	// NOT first contact + no anchor + tofu = refuse (never re-TOFU an
	// already-enrolled peel). Use a fresh anchor path so rung 3 can't apply.
	_, err = ResolveTrust(TrustConfig{
		Mode:         TrustModeTOFU,
		AnchorFile:   filepath.Join(t.TempDir(), "missing.crt"),
		MasterURLs:   []string{srv.URL},
		FirstContact: false,
	})
	if !errors.Is(err, ErrTrustPinMismatch) {
		t.Errorf("non-first-contact TOFU err = %v, want refusal", err)
	}
}

func TestResolveTrust_StrictModeNoFetch(t *testing.T) {
	// strict + no pin/file = system trust, no insecure fetch.
	rt, err := ResolveTrust(TrustConfig{
		Mode:         TrustModeStrict,
		AnchorFile:   filepath.Join(t.TempDir(), "missing.crt"),
		MasterURLs:   []string{"https://unused.example"},
		FirstContact: true,
	})
	if err != nil {
		t.Fatalf("strict resolve: %v", err)
	}
	if rt.Source != TrustSourceSystem {
		t.Errorf("source = %v, want system", rt.Source)
	}
	if rt.Doc != nil {
		t.Error("strict mode must not fetch a bootstrap doc")
	}
}

func TestResolveTrust_PersistedAnchorNeverReTOFU(t *testing.T) {
	authority, _ := ca.Generate(ca.Config{})
	anchor := filepath.Join(t.TempDir(), "enroll-ca.crt")
	if err := os.WriteFile(anchor, authority.Bundle(), 0600); err != nil {
		t.Fatal(err)
	}
	// Even not-first-contact with no master reachable: the anchor governs.
	rt, err := ResolveTrust(TrustConfig{
		AnchorFile:   anchor,
		MasterURLs:   []string{"https://unused.example"},
		FirstContact: false,
	})
	if err != nil {
		t.Fatalf("persisted-anchor resolve: %v", err)
	}
	if rt.Source != TrustSourceAnchor {
		t.Errorf("source = %v, want anchor", rt.Source)
	}
	// Anchor + mismatching pin = fatal (pin-upgrade guard).
	other, _ := ca.Generate(ca.Config{})
	if _, err := ResolveTrust(TrustConfig{
		AnchorFile: anchor, Pins: []string{other.RootSPKIPin()},
		MasterURLs: []string{"https://unused"}, FirstContact: false,
	}); !errors.Is(err, ErrTrustPinMismatch) {
		t.Errorf("anchor/pin mismatch err = %v, want fatal", err)
	}
}

func configTrusts(t *testing.T, cfg *tls.Config, srv *httptest.Server) bool {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
	resp, err := client.Get(srv.URL + "/api/v1/enroll/ca")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// TestResolveTrust_RootRolloverTwoPhaseOverlap walks the enrollment-plane
// half of the root-rollover runbook against real TLS handshakes: an
// already-enrolled peel (persisted anchor, never re-TOFU) crosses a root
// rollover purely through anchor-file drops.
//
//	Phase 0  anchor = old root            -> old master OK, new master REFUSED
//	Phase 1  anchor = old+new overlap     -> BOTH verify (either root's chain)
//	Phase 2  anchor = new root only       -> new OK, old REFUSED (retired)
//
// It also pins the pin-ordering constraint: MatchPin checks the FIRST root
// of the anchor bundle, so during overlap the pin set must still cover the
// bundle's first root — retiring the old pin before the old root leaves the
// bundle is a (loud) misconfiguration, never a silent downgrade.
func TestResolveTrust_RootRolloverTwoPhaseOverlap(t *testing.T) {
	oldCA, _ := ca.Generate(ca.Config{Organization: "OldRoot"})
	newCA, _ := ca.Generate(ca.Config{Organization: "NewRoot"})
	srvOld := caServer(t, oldCA, nil)
	srvNew := caServer(t, newCA, nil)
	anchor := filepath.Join(t.TempDir(), "enroll-ca.crt")

	resolve := func(pins []string) (*ResolvedTrust, error) {
		return ResolveTrust(TrustConfig{
			AnchorFile:   anchor,
			Pins:         pins,
			MasterURLs:   []string{srvOld.URL},
			FirstContact: false, // already enrolled: the anchor governs
		})
	}

	// Phase 0 — pre-rollover baseline: the persisted anchor is the old root.
	if err := os.WriteFile(anchor, oldCA.Bundle(), 0600); err != nil {
		t.Fatal(err)
	}
	rt, err := resolve(nil)
	if err != nil {
		t.Fatalf("phase 0 resolve: %v", err)
	}
	if !configTrusts(t, rt.TLSConfig, srvOld) {
		t.Fatal("phase 0: old-root chain must verify")
	}
	if configTrusts(t, rt.TLSConfig, srvNew) {
		t.Fatal("phase 0: unknown new root must be REFUSED (never re-TOFU)")
	}

	// Phase 1 — overlap: the anchor becomes the old+new root bundle (a file
	// drop). Chains from EITHER root verify, so masters can be moved to the
	// new hierarchy one at a time with the fleet fully functional.
	overlap := append(append([]byte{}, oldCA.Bundle()...), newCA.Bundle()...)
	if err := os.WriteFile(anchor, overlap, 0600); err != nil {
		t.Fatal(err)
	}
	rt, err = resolve(nil)
	if err != nil {
		t.Fatalf("phase 1 resolve: %v", err)
	}
	if !configTrusts(t, rt.TLSConfig, srvOld) {
		t.Error("phase 1: old-root chain must still verify during overlap")
	}
	if !configTrusts(t, rt.TLSConfig, srvNew) {
		t.Error("phase 1: new-root chain must verify during overlap")
	}

	// Phase 1 with pins: the overlap pin set covers the bundle's first root.
	if _, err := resolve([]string{oldCA.RootSPKIPin(), newCA.RootSPKIPin()}); err != nil {
		t.Errorf("phase 1 with overlap pin set: %v", err)
	}
	// Pin-ordering constraint: dropping the old pin while the old root still
	// leads the bundle is FATAL (pin-upgrade guard) — the operator must
	// retire the old root from the bundle first (or reorder it).
	if _, err := resolve([]string{newCA.RootSPKIPin()}); !errors.Is(err, ErrTrustPinMismatch) {
		t.Errorf("phase 1 with premature pin retirement = %v, want ErrTrustPinMismatch", err)
	}

	// Phase 2 — retire the old root: anchor becomes the new root only.
	if err := os.WriteFile(anchor, newCA.Bundle(), 0600); err != nil {
		t.Fatal(err)
	}
	rt, err = resolve([]string{newCA.RootSPKIPin()})
	if err != nil {
		t.Fatalf("phase 2 resolve: %v", err)
	}
	if !configTrusts(t, rt.TLSConfig, srvNew) {
		t.Error("phase 2: new-root chain must verify")
	}
	if configTrusts(t, rt.TLSConfig, srvOld) {
		t.Error("phase 2: retired old root must be refused")
	}
}

// TestResolveTrust_IntermediateRotationNoPeelAction: the enrollment plane's
// view of the ROUTINE rotation — the peel's persisted anchor (root only)
// verifies the master's chain both before and after the signing intermediate
// is rotated, with zero peel-side changes.
func TestResolveTrust_IntermediateRotationNoPeelAction(t *testing.T) {
	authority, _ := ca.Generate(ca.Config{})
	anchor := filepath.Join(t.TempDir(), "enroll-ca.crt")
	if err := os.WriteFile(anchor, authority.Bundle(), 0600); err != nil {
		t.Fatal(err)
	}
	rt, err := ResolveTrust(TrustConfig{
		AnchorFile:   anchor,
		Pins:         []string{authority.RootSPKIPin()},
		MasterURLs:   []string{"https://unused.example"},
		FirstContact: false,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Pre-rotation master.
	srvBefore := caServer(t, authority, nil)
	if !configTrusts(t, rt.TLSConfig, srvBefore) {
		t.Fatal("pre-rotation chain must verify")
	}

	// Rotate the signing intermediate; the master now presents a chain
	// through the NEW intermediate. Same anchor file, same pin, no re-resolve
	// needed — the trust material is untouched.
	rotated, err := authority.RotateIntermediate(ca.Config{})
	if err != nil {
		t.Fatalf("RotateIntermediate: %v", err)
	}
	srvAfter := caServer(t, rotated, nil)
	if !configTrusts(t, rt.TLSConfig, srvAfter) {
		t.Error("post-rotation chain must verify against the unchanged peel anchor")
	}
}
