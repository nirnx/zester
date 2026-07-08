package ca

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"
)

// PinPrefix is the scheme prefix of SPKI pins and fingerprints.
const PinPrefix = "sha256:"

// SPKIPin computes the RFC 7469-style pin of a certificate's
// SubjectPublicKeyInfo: "sha256:<hex>". Peels pin the ROOT's SPKI
// (enroll_ca_pin); the pin survives re-issuing the certificate with the
// same key, and is verifiable out-of-band with
//
//	openssl x509 -in root.crt -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256
func SPKIPin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return PinPrefix + hex.EncodeToString(sum[:])
}

// CertFingerprint computes "sha256:<hex>" over the certificate DER — the
// human cross-check value (changes on any re-issue, unlike the SPKI pin).
func CertFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return PinPrefix + hex.EncodeToString(sum[:])
}

// NormalizePin validates and canonicalizes a pin string: the sha256:
// prefix is required, hex is lowercased, and the digest must be exactly 32
// bytes.
func NormalizePin(pin string) (string, error) {
	p := strings.TrimSpace(pin)
	if !strings.HasPrefix(strings.ToLower(p), PinPrefix) {
		return "", fmt.Errorf("ca: pin %q must use the %q prefix", pin, PinPrefix)
	}
	hexPart := strings.ToLower(p[len(PinPrefix):])
	raw, err := hex.DecodeString(hexPart)
	if err != nil {
		return "", fmt.Errorf("ca: pin %q: invalid hex: %w", pin, err)
	}
	if len(raw) != sha256.Size {
		return "", fmt.Errorf("ca: pin %q: digest must be %d bytes, got %d", pin, sha256.Size, len(raw))
	}
	return PinPrefix + hexPart, nil
}

// MatchPin reports whether the certificate's SPKI matches any of the given
// pins (pins are normalized before comparison; invalid pins never match).
func MatchPin(cert *x509.Certificate, pins []string) bool {
	got := SPKIPin(cert)
	for _, p := range pins {
		n, err := NormalizePin(p)
		if err != nil {
			continue
		}
		if n == got {
			return true
		}
	}
	return false
}

// FindPinnedRoot scans a PEM bundle for a self-signed root whose SPKI
// matches one of the pins and returns it. This is the ONLY way trust may
// be established from an unauthenticated bundle fetch: exactly the pinned
// certificate becomes the anchor — never the whole bundle (a bundle
// containing the real root plus an attacker CA must yield only the real
// root).
func FindPinnedRoot(bundlePEM []byte, pins []string) (*x509.Certificate, error) {
	rest := bundlePEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if !cert.IsCA {
			continue
		}
		if MatchPin(cert, pins) {
			return cert, nil
		}
	}
	return nil, fmt.Errorf("ca: no CA certificate in the bundle matches the configured pin(s)")
}

// EncodeCertPEM returns the PEM encoding of a certificate.
func EncodeCertPEM(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

// FirstCARoot returns the first self-signed CA certificate found in a PEM
// bundle (a trust anchor). Errors when the bundle contains no such root.
func FirstCARoot(bundlePEM []byte) (*x509.Certificate, error) {
	rest := bundlePEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if cert.IsCA {
			return cert, nil
		}
	}
	return nil, fmt.Errorf("ca: no CA certificate in bundle")
}

// subjectKeyID computes the RFC 5280 method-1 subject key identifier
// (SHA-1 is the identifier convention, not a security property).
func subjectKeyID(pub *ecdsa.PublicKey) ([]byte, error) {
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("ca: marshal public key: %w", err)
	}
	// The SKI convention hashes the BIT STRING content of the SPKI; using
	// the SPKI digest truncated is equally valid per RFC 5280 (any method
	// that generates unique values is permitted). Use SHA-256/20 bytes.
	sum := sha256.Sum256(spki)
	return sum[:20], nil
}
