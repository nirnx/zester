package enroll

import (
	"encoding/json"
	"net/http"
)

// BootstrapDoc is the discovery + trust document served (unauthenticated) at
// GET /api/v1/enroll/ca and republished on the KV cluster-info channel. It
// carries everything a peel needs to reach the fleet's NATS after
// enrollment: the CA trust bundle and the fleet-facing NATS endpoint list.
//
// The document is additive-only (old peels ignore new fields; a new peel
// against an old master sees absent fields). It is deliberately delivered
// over the idempotent, re-fetchable /enroll/ca route — never bundled into
// the single-use creds response — so a peel that lost its cache or rotated
// trust can always re-fetch it.
//
// Identity for caching/ETag/idempotent-Put excludes IssuedAt (see
// ContentEqual): otherwise every master restart would churn the KV value.
type BootstrapDoc struct {
	V           int      `json:"v"`
	CABundlePEM string   `json:"ca_bundle_pem,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"` // root SPKI pin
	NATSURLs    []string `json:"nats_urls,omitempty"`
	IssuedAt    string   `json:"issued_at,omitempty"`
}

// BootstrapVersion is the current BootstrapDoc envelope version.
const BootstrapVersion = 1

// ContentEqual reports whether two docs are identical ignoring IssuedAt — the
// identity used for the idempotent multi-master KV Put, the divergence
// detector, and HTTP ETag stability.
func (d BootstrapDoc) ContentEqual(o BootstrapDoc) bool {
	if d.V != o.V || d.CABundlePEM != o.CABundlePEM || d.Fingerprint != o.Fingerprint {
		return false
	}
	if len(d.NATSURLs) != len(o.NATSURLs) {
		return false
	}
	for i := range d.NATSURLs {
		if d.NATSURLs[i] != o.NATSURLs[i] {
			return false
		}
	}
	return true
}

// BootstrapProvider supplies the current bootstrap document. The master wires
// it to its CA manager (bundle/pin) and validated advertise list.
type BootstrapProvider func() BootstrapDoc

// registerBootstrapRoute mounts GET /api/v1/enroll/ca when a provider is set.
func (h *Handler) registerBootstrapRoute(mux *http.ServeMux) {
	if h.bootstrap == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/enroll/ca", h.handleBootstrap)
}

// handleBootstrap serves the bootstrap document. Public material, so no auth;
// it inherits the enrollment listener's rate limiting. Cache-friendly:
// clients (and TOFU first-contact) may re-fetch freely.
func (h *Handler) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	doc := h.bootstrap()
	if doc.V == 0 {
		doc.V = BootstrapVersion
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}
