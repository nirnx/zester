// Package ca implements Zester's embedded certificate authority: a
// root + signing-intermediate hierarchy on stdlib crypto/x509, persisted as
// PEM files, issuing the TLS server certificates for the enrollment HTTPS
// listener and the external NATS server.
//
// Key layout (all under one directory, conventionally <auth_dir>/ca):
//
//	root.crt         self-signed root (the fleet trust anchor peels pin)
//	root.key         root private key (may be moved offline after init)
//	intermediate.crt signing intermediate (pathlen:0)
//	intermediate.key intermediate private key (needed for issuance/renewal)
//
// Trust distribution uses the ROOT only: peels verify chains against
// root.crt (delivered via the enrollment bootstrap document), servers
// present leaf+intermediate chains. Rotating the intermediate therefore
// never touches peel trust anchors; rotating the root is the documented
// two-phase bundle-overlap procedure.
//
// The CA private keys are held as crypto.Signer so a later HSM/KMS backend
// is additive; the file implementation uses ECDSA P-256.
package ca

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	// RootCertFile .. IntermediateKeyFile are the well-known file names
	// inside a CA directory.
	RootCertFile         = "root.crt"
	RootKeyFile          = "root.key"
	IntermediateCertFile = "intermediate.crt"
	IntermediateKeyFile  = "intermediate.key"

	// DefaultRootValidity and DefaultIntermediateValidity follow the
	// Puppet/Caddy convention: long root, shorter signing intermediate.
	DefaultRootValidity         = 10 * 365 * 24 * time.Hour
	DefaultIntermediateValidity = 5 * 365 * 24 * time.Hour

	// notBeforeBackdate guards freshly issued certificates against clock
	// skew on not-yet-NTP-synced nodes (Puppet backdates a full day).
	notBeforeBackdate = 24 * time.Hour
)

// ErrNotFound is returned by Load when the directory holds no CA material.
var ErrNotFound = errors.New("ca: no CA material found")

// ErrExists is returned by Generate/Init when CA material is already present
// (an existing CA is never silently overwritten).
var ErrExists = errors.New("ca: CA material already exists")

// Config parameterizes CA generation.
type Config struct {
	// Organization and CommonName label the root certificate subject.
	// Defaults: "Zester" / "Zester Root CA".
	Organization string
	CommonName   string

	// RootValidity / IntermediateValidity default to the package constants.
	RootValidity         time.Duration
	IntermediateValidity time.Duration
}

func (c *Config) defaults() {
	if c.Organization == "" {
		c.Organization = "Zester"
	}
	if c.CommonName == "" {
		c.CommonName = c.Organization + " Root CA"
	}
	if c.RootValidity == 0 {
		c.RootValidity = DefaultRootValidity
	}
	if c.IntermediateValidity == 0 {
		c.IntermediateValidity = DefaultIntermediateValidity
	}
}

// Authority is a loaded (or freshly generated) CA hierarchy. RootKey may be
// nil when the root key has been taken offline — issuance then still works
// (leaves are signed by the intermediate); only operations that need the
// root (intermediate rotation) fail with a clear error.
type Authority struct {
	Root            *x509.Certificate
	RootKey         crypto.Signer // nil in root-offline mode
	Intermediate    *x509.Certificate
	IntermediateKey crypto.Signer
}

// Leaf is an issued end-entity certificate: CertPEM carries the full served
// chain (leaf + intermediate), KeyPEM the PKCS#8 private key.
type Leaf struct {
	CertPEM []byte
	KeyPEM  []byte
	Cert    *x509.Certificate
}

// Generate creates a new root + intermediate hierarchy in memory.
func Generate(cfg Config) (*Authority, error) {
	cfg.defaults()

	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca: generate root key: %w", err)
	}
	rootTmpl, err := newCATemplate(pkix.Name{
		Organization: []string{cfg.Organization},
		CommonName:   cfg.CommonName,
	}, cfg.RootValidity, 1)
	if err != nil {
		return nil, err
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		return nil, fmt.Errorf("ca: create root certificate: %w", err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return nil, fmt.Errorf("ca: parse root certificate: %w", err)
	}

	intermediate, intKey, err := newIntermediate(root, rootKey, cfg)
	if err != nil {
		return nil, err
	}

	return &Authority{
		Root:            root,
		RootKey:         rootKey,
		Intermediate:    intermediate,
		IntermediateKey: intKey,
	}, nil
}

// newIntermediate mints a fresh signing intermediate under the given root.
// Shared by Generate and RotateIntermediate so the two can never drift.
func newIntermediate(root *x509.Certificate, rootKey crypto.Signer, cfg Config) (*x509.Certificate, crypto.Signer, error) {
	intKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("ca: generate intermediate key: %w", err)
	}
	intTmpl, err := newCATemplate(pkix.Name{
		Organization: []string{cfg.Organization},
		CommonName:   cfg.Organization + " Signing CA",
	}, cfg.IntermediateValidity, 0)
	if err != nil {
		return nil, nil, err
	}
	intDER, err := x509.CreateCertificate(rand.Reader, intTmpl, root, &intKey.PublicKey, rootKey)
	if err != nil {
		return nil, nil, fmt.Errorf("ca: create intermediate certificate: %w", err)
	}
	intermediate, err := x509.ParseCertificate(intDER)
	if err != nil {
		return nil, nil, fmt.Errorf("ca: parse intermediate certificate: %w", err)
	}
	return intermediate, intKey, nil
}

// RotateIntermediate returns a new Authority sharing this Authority's root
// with a freshly minted signing intermediate — the programmatic core of the
// "intermediate rotation (routine)" runbook step. It requires the root KEY:
// an Authority loaded in root-offline mode cannot rotate. The rotation is
// invisible to the fleet by construction — peels anchor the ROOT (whose SPKI
// pin is unchanged), and leaves issued by BOTH intermediates keep verifying
// against it. Persist the result with SaveIntermediate (Save refuses to
// touch a dir that already holds a root key).
func (a *Authority) RotateIntermediate(cfg Config) (*Authority, error) {
	if a.RootKey == nil {
		return nil, fmt.Errorf("ca: rotate intermediate: root key unavailable (root-offline mode)")
	}
	cfg.defaults()
	intermediate, intKey, err := newIntermediate(a.Root, a.RootKey, cfg)
	if err != nil {
		return nil, err
	}
	return &Authority{
		Root:            a.Root,
		RootKey:         a.RootKey,
		Intermediate:    intermediate,
		IntermediateKey: intKey,
	}, nil
}

// SaveIntermediate persists ONLY the intermediate certificate and key into
// dir, leaving the root material untouched — the on-disk half of an
// intermediate rotation ("replace intermediate.crt/intermediate.key in
// ca.dir"). The dir must already exist (it holds the root being kept).
func (a *Authority) SaveIntermediate(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, IntermediateCertFile), encodePEM("CERTIFICATE", a.Intermediate.Raw), 0644); err != nil {
		return fmt.Errorf("ca: write %s: %w", IntermediateCertFile, err)
	}
	if a.IntermediateKey == nil {
		return fmt.Errorf("ca: save intermediate: key unavailable")
	}
	keyPEM, err := marshalKeyPEM(a.IntermediateKey)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, IntermediateKeyFile), keyPEM, 0600); err != nil {
		return fmt.Errorf("ca: write %s: %w", IntermediateKeyFile, err)
	}
	return nil
}

// newCATemplate builds a CA certificate template. maxPathLen 0 sets
// MaxPathLenZero (a signing-only intermediate).
func newCATemplate(subject pkix.Name, validity time.Duration, maxPathLen int) (*x509.Certificate, error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               subject,
		NotBefore:             time.Now().Add(-notBeforeBackdate),
		NotAfter:              time.Now().Add(validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	if maxPathLen == 0 {
		tmpl.MaxPathLenZero = true
	} else {
		tmpl.MaxPathLen = maxPathLen
	}
	return tmpl, nil
}

// randomSerial returns a random positive 128-bit serial (RFC 5280 caps
// serials at 20 octets; random serials avoid Puppet-style serial-file
// state and locking).
func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("ca: generate serial: %w", err)
	}
	return serial, nil
}

// IssueServer issues a TLS server leaf signed by the intermediate. The
// returned CertPEM is the full served chain (leaf + intermediate). Both
// ServerAuth and ClientAuth EKUs are set (NATS cluster routes use mutual
// TLS; a future peel client cert reuses the same profile).
func (a *Authority) IssueServer(cn string, dnsNames []string, ips []net.IP, validity time.Duration) (*Leaf, error) {
	if a.IntermediateKey == nil {
		return nil, fmt.Errorf("ca: intermediate key unavailable; cannot issue")
	}
	// Refuse to issue from an expired intermediate — x509.CreateCertificate
	// does NOT validate the parent's validity window, so it would "succeed"
	// and produce an unverifiable chain.
	now := time.Now()
	if now.After(a.Intermediate.NotAfter) {
		return nil, fmt.Errorf("ca: intermediate expired %s; cannot issue (rotate the intermediate)", a.Intermediate.NotAfter.Format(time.RFC3339))
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca: generate leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	ski, err := subjectKeyID(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	// Clamp the leaf's expiry to the intermediate's — a leaf outliving its
	// issuer is unverifiable past the intermediate's NotAfter.
	notAfter := now.Add(validity)
	if notAfter.After(a.Intermediate.NotAfter) {
		notAfter = a.Intermediate.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now.Add(-notBeforeBackdate),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
		SubjectKeyId: ski,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.Intermediate, &key.PublicKey, a.IntermediateKey)
	if err != nil {
		return nil, fmt.Errorf("ca: create leaf certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("ca: parse leaf certificate: %w", err)
	}

	chain := append(encodePEM("CERTIFICATE", der), encodePEM("CERTIFICATE", a.Intermediate.Raw)...)
	keyPEM, err := marshalKeyPEM(key)
	if err != nil {
		return nil, err
	}
	return &Leaf{CertPEM: chain, KeyPEM: keyPEM, Cert: cert}, nil
}

// Bundle returns the PEM trust bundle clients should anchor on: the root
// certificate only (servers present leaf+intermediate chains).
func (a *Authority) Bundle() []byte {
	return encodePEM("CERTIFICATE", a.Root.Raw)
}

// RootSPKIPin returns the RFC 7469-style pin of the root's
// SubjectPublicKeyInfo: "sha256:<hex>". The pin survives re-issuing the
// root certificate with the same key.
func (a *Authority) RootSPKIPin() string {
	return SPKIPin(a.Root)
}

// Fingerprint returns "sha256:<hex>" of the root certificate DER (the
// human cross-check shown by `zester ca fingerprint` and logged by peels).
func (a *Authority) Fingerprint() string {
	return CertFingerprint(a.Root)
}

// Save persists the hierarchy into dir (created 0700 if needed): certs
// 0644, keys 0600. Refuses to overwrite an existing root key.
func (a *Authority) Save(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, RootKeyFile)); err == nil {
		return ErrExists
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("ca: create dir: %w", err)
	}
	writes := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{RootCertFile, encodePEM("CERTIFICATE", a.Root.Raw), 0644},
		{IntermediateCertFile, encodePEM("CERTIFICATE", a.Intermediate.Raw), 0644},
	}
	for _, w := range writes {
		if err := os.WriteFile(filepath.Join(dir, w.name), w.data, w.mode); err != nil {
			return fmt.Errorf("ca: write %s: %w", w.name, err)
		}
	}
	for _, k := range []struct {
		name string
		key  crypto.Signer
	}{
		{RootKeyFile, a.RootKey},
		{IntermediateKeyFile, a.IntermediateKey},
	} {
		if k.key == nil {
			continue
		}
		keyPEM, err := marshalKeyPEM(k.key)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, k.name), keyPEM, 0600); err != nil {
			return fmt.Errorf("ca: write %s: %w", k.name, err)
		}
	}
	return nil
}

// Load reads a hierarchy from dir. A missing root certificate yields
// ErrNotFound. A missing root KEY is tolerated (root-offline mode); a
// missing intermediate key is an error only when issuance is attempted.
func Load(dir string) (*Authority, error) {
	root, err := loadCert(filepath.Join(dir, RootCertFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	intermediate, err := loadCert(filepath.Join(dir, IntermediateCertFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("ca: root present but %s missing (torn init?): %w", IntermediateCertFile, err)
		}
		return nil, err
	}
	a := &Authority{Root: root, Intermediate: intermediate}

	if key, err := loadKey(filepath.Join(dir, RootKeyFile)); err == nil {
		a.RootKey = key
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if key, err := loadKey(filepath.Join(dir, IntermediateKeyFile)); err == nil {
		a.IntermediateKey = key
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return a, nil
}

// Exists reports whether dir contains CA material (the root certificate).
func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, RootCertFile))
	return err == nil
}

// LoadOrGenerate loads the hierarchy from dir, generating and saving a
// fresh one when none exists. generated reports whether generation ran.
func LoadOrGenerate(dir string, cfg Config) (a *Authority, generated bool, err error) {
	a, err = Load(dir)
	if err == nil {
		return a, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	a, err = Generate(cfg)
	if err != nil {
		return nil, false, err
	}
	if err := a.Save(dir); err != nil {
		return nil, false, err
	}
	return a, true, nil
}

func loadCert(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("ca: %s: no CERTIFICATE PEM block", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse %s: %w", path, err)
	}
	return cert, nil
}

func loadKey(path string) (crypto.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("ca: %s: no PEM block", path)
	}
	// PKCS#8 is what this package writes; SEC1 tolerated for material
	// produced by the older bus.SelfSignedCA primitive.
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("ca: %s: key type %T is not a signer", path, key)
		}
		return signer, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("ca: %s: unsupported private key format", path)
}

func marshalKeyPEM(key crypto.Signer) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("ca: marshal private key: %w", err)
	}
	return encodePEM("PRIVATE KEY", der), nil
}

func encodePEM(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}
