package enroll

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nirnx/zester/pkg/ca"
)

// TrustMode selects the fallback behavior when no explicit trust
// (enroll_ca file or enroll_ca_pin) and no persisted anchor is configured.
type TrustMode string

const (
	// TrustModeTOFU trusts the CA presented on first contact, persisting it
	// as the anchor and binding its fingerprint into the enrollment
	// submission for operator visibility. This is the default: setting
	// master_urls alone yields verified-once first contact.
	TrustModeTOFU TrustMode = "tofu"
	// TrustModeStrict refuses first contact without a pin or CA file:
	// unknown-authority is fatal, never a silent downgrade to TOFU.
	TrustModeStrict TrustMode = "strict"
)

// TrustConfig parameterizes the enrollment trust ladder.
type TrustConfig struct {
	// EnrollCAFile, when set, is the strict CA bundle for enrollment TLS
	// (rung 1). Highest precedence.
	EnrollCAFile string
	// Pins are sha256:<hex> root SPKI pins (rung 2). A configured pin that
	// fails is fatal — never a downgrade.
	Pins []string
	// AnchorFile is the persisted TOFU/pin anchor path (rung 3): once
	// written, subsequent boots verify strictly against it and never re-TOFU.
	AnchorFile string
	// Mode is the fallback mode (rung 4/5). Default tofu.
	Mode TrustMode
	// FirstContact reports whether this is genuine first contact (no
	// credentials yet). TOFU fires ONLY on first contact; an already-enrolled
	// peel seeing an unknown authority is an incident, never a re-TOFU.
	FirstContact bool
	// MasterURLs is the ordered enrollment base-URL list, used for the
	// insecure candidate-CA fetch in pin/TOFU modes.
	MasterURLs []string
	// Logger.
	Logger *slog.Logger
}

// TrustSource records which ladder rung established trust (advisory/log-only).
type TrustSource string

const (
	TrustSourceFile   TrustSource = "file"
	TrustSourcePin    TrustSource = "pin"
	TrustSourceAnchor TrustSource = "anchor"
	TrustSourceSystem TrustSource = "system"
	TrustSourceTOFU   TrustSource = "tofu"
)

// ResolvedTrust is the outcome of the trust ladder.
type ResolvedTrust struct {
	// TLSConfig verifies the enrollment endpoint per the resolved rung.
	TLSConfig *tls.Config
	// Source is the rung that established trust.
	Source TrustSource
	// RootSPKI is the sha256:<hex> pin of the trusted CA root, bound into
	// the enrollment submission. Empty in system-trust mode with no fleet CA.
	RootSPKI string
	// Anchor is the trusted root certificate (nil in pure system-trust mode).
	Anchor *x509.Certificate
	// Doc is the bootstrap document fetched during pin/TOFU resolution
	// (nil when trust came from a file or persisted anchor without a fetch).
	Doc *BootstrapDoc
}

// ErrTrustPinMismatch signals a configured pin (or persisted anchor) that did
// not match — a fatal-class error, never a downgrade trigger.
var ErrTrustPinMismatch = errors.New("enroll: trust anchor/pin mismatch")

// ResolveTrust walks the trust ladder and returns a verified TLS config for
// the enrollment endpoint. Precedence: enroll_ca file > pin > persisted
// anchor > system trust > TOFU (first contact only).
func ResolveTrust(cfg TrustConfig) (*ResolvedTrust, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Mode == "" {
		cfg.Mode = TrustModeTOFU
	}

	// Rung 1: explicit CA file (strict). Pin, if also set, is enforced as a
	// belt on top of the file's roots.
	if cfg.EnrollCAFile != "" {
		pool, root, err := poolFromFile(cfg.EnrollCAFile)
		if err != nil {
			return nil, err
		}
		if len(cfg.Pins) > 0 && root != nil && !ca.MatchPin(root, cfg.Pins) {
			return nil, fmt.Errorf("%w: enroll_ca file root does not match enroll_ca_pin", ErrTrustPinMismatch)
		}
		return &ResolvedTrust{
			TLSConfig: strictConfig(pool),
			Source:    TrustSourceFile,
			RootSPKI:  pinOf(root),
			Anchor:    root,
		}, nil
	}

	// Rung 3 (checked before pin fetch): a persisted anchor. Once written,
	// verify strictly against it and NEVER re-TOFU. If a pin is also
	// configured, the anchor must match it (offline pin-upgrade check).
	if cfg.AnchorFile != "" {
		if pool, root, err := poolFromFile(cfg.AnchorFile); err == nil {
			if len(cfg.Pins) > 0 && root != nil && !ca.MatchPin(root, cfg.Pins) {
				return nil, fmt.Errorf("%w: persisted anchor %s does not match enroll_ca_pin (delete it and restart to re-fetch under the pin)", ErrTrustPinMismatch, cfg.AnchorFile)
			}
			src := TrustSourceAnchor
			if len(cfg.Pins) > 0 {
				src = TrustSourcePin
			}
			return &ResolvedTrust{
				TLSConfig: strictConfig(pool),
				Source:    src,
				RootSPKI:  pinOf(root),
				Anchor:    root,
			}, nil
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	// Rung 2: pin configured, no file, no anchor -> fetch candidate CA and
	// require the pin to match, then persist the anchor.
	if len(cfg.Pins) > 0 {
		return resolveByFetch(cfg, cfg.Pins)
	}

	// Rung 4/5: no explicit trust. Strict mode = system trust only (fail
	// closed on unknown authority). TOFU mode = trust first contact, but
	// ONLY on genuine first contact.
	if cfg.Mode == TrustModeStrict {
		cfg.Logger.Warn("enrollment trust: no enroll_ca/enroll_ca_pin set and enroll_trust=strict; relying on the system trust store (embedded-CA masters require a pin)")
		return &ResolvedTrust{TLSConfig: strictConfig(nil), Source: TrustSourceSystem}, nil
	}
	if !cfg.FirstContact {
		// An enrolled/anchored peel must never TOFU. Fail closed.
		return nil, fmt.Errorf("%w: no trust anchor and not first contact; refusing to trust-on-first-use for an already-enrolled peel (delete %s to re-TOFU)", ErrTrustPinMismatch, cfg.AnchorFile)
	}
	return resolveByFetch(cfg, nil) // nil pins = TOFU
}

// resolveByFetch performs the insecure candidate-CA fetch used by pin (rung 2)
// and TOFU (rung 5). With pins, exactly the pinned root becomes the anchor;
// without pins (TOFU), the served root is trusted and its fingerprint logged.
func resolveByFetch(cfg TrustConfig, pins []string) (*ResolvedTrust, error) {
	doc, err := fetchCandidateCA(cfg.MasterURLs)
	if err != nil {
		return nil, err
	}
	bundle := []byte(doc.CABundlePEM)
	if len(bundle) == 0 {
		return nil, fmt.Errorf("enroll: master served no CA bundle at /api/v1/enroll/ca (embedded CA / discovery not enabled?)")
	}

	var root *x509.Certificate
	if len(pins) > 0 {
		root, err = ca.FindPinnedRoot(bundle, pins)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTrustPinMismatch, err)
		}
	} else {
		root, err = firstRoot(bundle)
		if err != nil {
			return nil, err
		}
		cfg.Logger.Warn("TOFU: trusting enrollment CA on first contact — verify against 'zester ca fingerprint' on the master; set enroll_ca_pin to prevent this window",
			"fingerprint", ca.CertFingerprint(root), "spki_pin", ca.SPKIPin(root))
	}

	// Anchor trust to EXACTLY the selected root (never the whole bundle).
	pool := x509.NewCertPool()
	pool.AddCert(root)

	// Persist the anchor so subsequent boots are strict (rung 3), never
	// re-TOFU. Write atomically (temp + rename): a crash or ENOSPC mid-write
	// must never leave a truncated anchor, which the strict read path treats
	// as a fatal non-ENOENT error and would wedge the peel until manual
	// deletion.
	if cfg.AnchorFile != "" {
		if err := writeFileAtomic(cfg.AnchorFile, ca.EncodeCertPEM(root), 0600); err != nil {
			cfg.Logger.Warn("enrollment trust: failed to persist anchor", "path", cfg.AnchorFile, "error", err)
		}
	}

	src := TrustSourceTOFU
	if len(pins) > 0 {
		src = TrustSourcePin
	}
	return &ResolvedTrust{
		TLSConfig: strictConfig(pool),
		Source:    src,
		RootSPKI:  ca.SPKIPin(root),
		Anchor:    root,
		Doc:       doc,
	}, nil
}

// fetchCandidateCA fetches the bootstrap document over deliberately-unverified
// TLS from the first reachable master URL. This is the ONLY InsecureSkipVerify
// path in the peel; it is scoped to exactly /api/v1/enroll/ca and its result
// is trusted only after pin verification (rung 2) or logged first-use (TOFU).
func fetchCandidateCA(masterURLs []string) (*BootstrapDoc, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true}, //nolint:gosec // pin/TOFU verified post-fetch
		},
	}
	var lastErr error
	for _, base := range masterURLs {
		url := strings.TrimRight(base, "/") + "/api/v1/enroll/ca"
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
			continue
		}
		var doc BootstrapDoc
		if err := json.Unmarshal(body, &doc); err != nil {
			lastErr = fmt.Errorf("decode bootstrap doc: %w", err)
			continue
		}
		return &doc, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no master URLs configured")
	}
	return nil, fmt.Errorf("enroll: fetch candidate CA: %w", lastErr)
}

// FetchBootstrap fetches the bootstrap document over a VERIFIED TLS config
// (from a persisted anchor or pin) — used by the peel's discovery-refresh and
// recovery loop, where trust is already established. Unlike fetchCandidateCA
// this does NOT skip verification.
func FetchBootstrap(masterURLs []string, tlsCfg *tls.Config) (*BootstrapDoc, error) {
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
	var lastErr error
	for _, base := range masterURLs {
		url := strings.TrimRight(base, "/") + "/api/v1/enroll/ca"
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
			continue
		}
		var doc BootstrapDoc
		if err := json.Unmarshal(body, &doc); err != nil {
			lastErr = err
			continue
		}
		return &doc, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no master URLs")
	}
	return nil, fmt.Errorf("enroll: fetch bootstrap: %w", lastErr)
}

func strictConfig(pool *x509.CertPool) *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if pool != nil {
		cfg.RootCAs = pool
	}
	return cfg
}

func poolFromFile(path string) (*x509.CertPool, *x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, nil, fmt.Errorf("enroll: %s: no CA certificates", path)
	}
	root, _ := firstRoot(data) // best-effort for pin/binding; nil if none
	return pool, root, nil
}

// firstRoot returns the first self-signed CA certificate in a PEM bundle.
func firstRoot(bundlePEM []byte) (*x509.Certificate, error) {
	root, err := ca.FirstCARoot(bundlePEM)
	if err != nil {
		return nil, fmt.Errorf("enroll: %w", err)
	}
	return root, nil
}

func pinOf(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	return ca.SPKIPin(cert)
}

// writeFileAtomic writes data to a temp file in the same directory and renames
// it over path, so a reader never observes a partial file (and a crash leaves
// either the old file or the complete new one, never a truncated anchor).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
