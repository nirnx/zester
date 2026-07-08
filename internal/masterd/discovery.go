package masterd

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/enroll"
	"github.com/nirnx/zester/pkg/settings"
)

// advertiseURLs returns the validated fleet-facing NATS URL list served to
// peels via discovery. Loopback / unspecified / link-local entries are
// rejected (a master's own tls://localhost:4222 view must never leak); each
// rejection is warned. An empty result means discovery advertises no NATS
// endpoints (peels fall back to their configured nats_url / builtin default).
func (d *Daemon) advertiseURLs() []string {
	if len(d.cfg.NatsAdvertise) == 0 {
		return nil
	}
	accepted, rejected := bus.ValidateAdvertisableNATSURLs(d.cfg.NatsAdvertise)
	for url, reason := range rejected {
		d.logger.Warn("nats_advertise_urls: rejected non-advertisable URL", "url", url, "reason", reason)
	}
	return accepted
}

// bootstrapDoc builds the current bootstrap document from the embedded CA (if
// any) and the validated advertise list. Empty CA fields in external mode
// (the route still serves the NATS endpoints when advertising is configured).
func (d *Daemon) bootstrapDoc() enroll.BootstrapDoc {
	doc := enroll.BootstrapDoc{
		V:        enroll.BootstrapVersion,
		NATSURLs: d.advertiseURLs(),
	}
	if d.caManager != nil {
		doc.CABundlePEM = string(d.caManager.bundle())
		doc.Fingerprint = d.caManager.rootPin()
	}
	return doc
}

// bootstrapEnabled reports whether the /enroll/ca discovery route should be
// served: either the embedded CA has a bundle to hand out, or advertising is
// configured.
func (d *Daemon) bootstrapEnabled() bool {
	return d.caManager != nil || len(d.advertiseURLs()) > 0
}

// publishClusterInfo writes the bootstrap document (JSON) to the secrets
// bucket cluster-info key for the peel refresh channel. Idempotent and
// compare-before-put: identical content (ignoring IssuedAt) is not re-put, so
// steady-state masters produce no KV churn; a byte mismatch across masters is
// warned as a divergence signal. Not lease-gated.
func (d *Daemon) publishClusterInfo(ctx context.Context, secretsKV bus.KV) {
	if !d.bootstrapEnabled() {
		return
	}
	doc := d.bootstrapDoc()

	// Read the current value; skip the put when content-equal (ignoring the
	// per-publish timestamp) to keep multi-master publishes churn-free. A
	// differing CA fingerprint from another master is a split-brain-CA
	// signal (two masters with independently-generated roots) — warn loudly.
	if entry, err := secretsKV.Get(ctx, settings.ClusterInfoKey); err == nil {
		var existing enroll.BootstrapDoc
		if json.Unmarshal(entry.Value(), &existing) == nil {
			if existing.Fingerprint != "" && doc.Fingerprint != "" && existing.Fingerprint != doc.Fingerprint {
				d.logger.Warn("cluster-info: CA fingerprint diverges from another master's published value — split-brain CA? replicate ONE ca directory to every master",
					"our_ca", doc.Fingerprint, "published_ca", existing.Fingerprint)
			}
			if existing.ContentEqual(doc) {
				return
			}
		}
	}

	doc.IssuedAt = time.Now().UTC().Format(time.RFC3339)
	payload, err := json.Marshal(doc)
	if err != nil {
		d.logger.Warn("cluster-info: marshal bootstrap doc", "error", err)
		return
	}
	if _, err := secretsKV.Put(ctx, settings.ClusterInfoKey, payload); err != nil {
		d.logger.Warn("cluster-info: publish failed", "error", err)
		return
	}
	d.logger.Info("published cluster-info bootstrap document",
		"nats_urls", len(doc.NATSURLs), "has_ca", doc.CABundlePEM != "")
}
