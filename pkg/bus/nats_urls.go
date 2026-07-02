package bus

import (
	"crypto/tls"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// DefaultNATSCAPath is the conventional location for the NATS CA certificate
// on Zester nodes. NATSClientTLS falls back to it when present and no explicit
// CA is configured.
const DefaultNATSCAPath = "/data/auth/nats-ca.crt"

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

// NATSClientTLS builds the client TLS configuration for tls:// NATS URLs.
// The CA certificate is resolved in order: the explicit path, the
// NATS_CA_FILE environment variable, then DefaultNATSCAPath if it exists
// (falling back to the system trust store otherwise). Returns nil when no
// URL uses the tls:// scheme.
func NATSClientTLS(urls []string, explicitCA string) (*tls.Config, error) {
	hasTLS := false
	for _, u := range NormalizeNATSURLs(urls) {
		if strings.HasPrefix(strings.ToLower(u), "tls://") {
			hasTLS = true
			break
		}
	}
	if !hasTLS {
		return nil, nil
	}

	caPath := explicitCA
	if caPath == "" {
		caPath = os.Getenv("NATS_CA_FILE")
	}
	if caPath == "" {
		if _, err := os.Stat(DefaultNATSCAPath); err == nil {
			caPath = DefaultNATSCAPath
		}
	}

	return ClientTLSConfig(TLSConfig{CAFile: caPath})
}
