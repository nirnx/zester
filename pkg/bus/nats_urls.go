package bus

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DefaultNATSCAPath is the conventional location for the NATS CA certificate
// on Zester nodes. NATSClientTLS falls back to it when present and no explicit
// CA is configured.
const DefaultNATSCAPath = "/var/lib/zester/auth/nats-ca.crt"

// NormalizeNATSURLs flattens comma-separated URL entries, trims whitespace,
// and drops empty values.
func NormalizeNATSURLs(urls []string) []string {
	var out []string
	for _, raw := range urls {
		for _, part := range strings.Split(raw, ",") {
			u := strings.TrimSpace(part)
			if u == "" {
				continue
			}
			out = append(out, u)
		}
	}
	return out
}

// ValidateTLSNATSURLs enforces tls:// URLs for all NATS connections.
func ValidateTLSNATSURLs(urls []string) error {
	normalized := NormalizeNATSURLs(urls)
	if len(normalized) == 0 {
		return fmt.Errorf("at least one NATS URL is required")
	}

	for _, raw := range normalized {
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("invalid NATS URL %q: %w", raw, err)
		}
		if u.Scheme == "" {
			return fmt.Errorf("invalid NATS URL %q: missing scheme", raw)
		}
		if !strings.EqualFold(u.Scheme, "tls") {
			return fmt.Errorf("insecure NATS URL %q: only tls:// URLs are allowed", raw)
		}
	}

	return nil
}

// ValidateAdvertisableNATSURLs filters a candidate NATS URL list down to the
// entries safe to advertise to remote peels: tls:// scheme (reusing the
// mandatory-TLS rule), and a host that is NOT loopback / unspecified /
// link-local — a master's own view (packaged default tls://localhost:4222)
// must never leak to the fleet, and a NATS server binding a specific host or
// 0.0.0.0 can gossip such addresses (per the maintainer's mandate the reject
// set is applied at every trust boundary). Checks are net.ParseIP-based so
// bracketed IPv6, IPv4-mapped IPv6, and 0.0.0.0/8 forms are caught; an empty
// host is rejected outright. Hostnames that are not IP literals are accepted
// as-is (no DNS resolution) — resolution is the operator's responsibility.
// Returns the accepted URLs (order preserved, deduplicated) and the rejected
// ones mapped to a reason for logging.
func ValidateAdvertisableNATSURLs(urls []string) (accepted []string, rejected map[string]string) {
	rejected = make(map[string]string)
	seen := make(map[string]struct{})
	for _, raw := range NormalizeNATSURLs(urls) {
		if _, dup := seen[raw]; dup {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			rejected[raw] = "unparseable URL"
			continue
		}
		if !strings.EqualFold(u.Scheme, "tls") {
			rejected[raw] = "not a tls:// URL"
			continue
		}
		host := u.Hostname() // strips brackets and port
		if host == "" {
			rejected[raw] = "empty host"
			continue
		}
		if reason := loopbackRejectReason(host); reason != "" {
			rejected[raw] = reason
			continue
		}
		seen[raw] = struct{}{}
		accepted = append(accepted, raw)
	}
	return accepted, rejected
}

// loopbackRejectReason returns a non-empty reason when host is an address (or
// well-known name) that must never be advertised to remote peels.
func loopbackRejectReason(host string) string {
	// Strip a trailing dot (RFC 6761 FQDN form: "localhost." resolves to
	// loopback just like "localhost").
	lower := strings.TrimSuffix(strings.ToLower(host), ".")
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return "localhost"
	}
	if ip := net.ParseIP(host); ip != nil {
		switch {
		case ip.IsLoopback():
			return "loopback address"
		case ip.IsUnspecified():
			return "unspecified address (0.0.0.0 / ::)"
		case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
			return "link-local address"
		}
		// 0.0.0.0/8: IsUnspecified only catches 0.0.0.0 itself, but on
		// Linux any 0.x destination routes to loopback.
		if v4 := ip.To4(); v4 != nil && v4[0] == 0 {
			return "0.0.0.0/8 address"
		}
		return ""
	}
	// All-numeric hostnames that fail ParseIP (e.g. "127.1", decimal
	// "2130706433") resolve to loopback via inet_aton on many systems.
	if isAllNumericHost(lower) {
		return "ambiguous numeric hostname"
	}
	return ""
}

// isAllNumericHost reports whether host is a bare-numeric form that ParseIP
// rejected but the C resolver (inet_aton) would still parse as an address —
// decimal/dotted-decimal (127.1, 2130706433) and hex (0x7f000001, 0x7f.1).
func isAllNumericHost(host string) bool {
	if host == "" {
		return false
	}
	// Hex inet_aton form: any label beginning 0x/0X.
	for _, label := range strings.Split(host, ".") {
		if strings.HasPrefix(label, "0x") || strings.HasPrefix(label, "0X") {
			return true
		}
	}
	for _, r := range host {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

// ResolveNATSCAFile resolves the CA certificate path for NATS TLS
// verification in order: the explicit path, the NATS_CA_FILE environment
// variable, then the conventional per-daemon candidate <authDir>/nats-ca.crt
// (DefaultNATSCAPath when authDir is empty). The returned path is NOT read
// or stat'd here — the client re-reads it on every (re)connect attempt, so
// trust material that materializes or rotates later is picked up without a
// restart. optional reports the trust semantics: explicit/env paths are
// strict (missing file = failed attempt, retried — never a silent fallback
// to the system store), while the conventional candidate is optional (a
// missing file means the system trust store for that attempt).
func ResolveNATSCAFile(explicitCA, authDir string) (path string, optional bool) {
	if explicitCA != "" {
		return explicitCA, false
	}
	if p := os.Getenv("NATS_CA_FILE"); p != "" {
		return p, false
	}
	if authDir != "" {
		return filepath.Join(authDir, "nats-ca.crt"), true
	}
	return DefaultNATSCAPath, true
}

// NATSClientTLS builds the client TLS configuration for tls:// NATS URLs.
// It returns a base *tls.Config (TLS 1.3 floor) plus the resolved CA file
// path and its optionality per ResolveNATSCAFile (authDir supplies the
// conventional <authDir>/nats-ca.crt candidate; pass "" for
// DefaultNATSCAPath). Wire all three into ClientConfig (TLS, CAFile,
// CAFileOptional). The CA file is NOT read here — bus.NewClient installs a
// per-connect callback that re-reads it on every (re)connect attempt, so CA
// rotation is a file drop (no process restart) and a not-yet-materialized
// file heals once it appears. Returns (nil, "", false) when no URL uses the
// tls:// scheme.
func NATSClientTLS(urls []string, explicitCA, authDir string) (*tls.Config, string, bool) {
	hasTLS := false
	for _, u := range NormalizeNATSURLs(urls) {
		if strings.HasPrefix(strings.ToLower(u), "tls://") {
			hasTLS = true
			break
		}
	}
	if !hasTLS {
		return nil, "", false
	}

	caFile, optional := ResolveNATSCAFile(explicitCA, authDir)
	return &tls.Config{MinVersion: tls.VersionTLS13}, caFile, optional
}
