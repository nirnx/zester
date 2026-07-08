package peeld

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/ca"
	"github.com/nirnx/zester/pkg/enroll"
	"github.com/nirnx/zester/pkg/settings"
)

// recoveryUnhealthyThreshold is how long the NATS connection must stay
// unhealthy before the recovery loop re-discovers endpoints via the
// enrollment channel.
const recoveryUnhealthyThreshold = 5 * time.Minute

// bootstrapCacheFileName is the durable local cache of the fleet NATS
// endpoint list, written under the peel's data_dir. Later boots (and
// offline-first boots) read it so they never need the master.
const bootstrapCacheFileName = "nats-bootstrap.msgpack"

// bootstrapCache is the on-disk cache content (JSON; the ".msgpack" name is
// historical parity with the settings snapshot — content is small JSON).
type bootstrapCache struct {
	V             int      `json:"v"`
	NATSURLs      []string `json:"nats_urls"`
	Source        string   `json:"source"`
	IdentityHash  string   `json:"identity_hash"`
	CAFingerprint string   `json:"ca_fingerprint,omitempty"`
}

func (a *Agent) bootstrapCachePath() string {
	return filepath.Join(a.cfg.DataDir, bootstrapCacheFileName)
}

// builtinNATSTail is the last-resort NATS endpoint when nothing is configured
// or discovered — preserves the historical default.
const builtinNATSTail = "tls://nats:4222"

// resolveNATSURLs applies the boot precedence: explicit nats_url (non-empty)
// wins over discovery; otherwise the validated bootstrap cache; otherwise the
// builtin tail. A Warn is logged when falling to the tail while master_urls
// are set (discovery is expected but has not populated the cache yet — the
// connected phase self-heals via the cluster-info watch).
func (a *Agent) resolveNATSURLs() []string {
	if explicit := bus.NormalizeNATSURLs([]string{a.cfg.NatsURL}); len(explicit) > 0 {
		return explicit
	}
	if cached := a.loadBootstrapCache(); len(cached) > 0 {
		a.logger.Info("using NATS endpoints from bootstrap cache", "count", len(cached))
		return cached
	}
	if len(a.cfg.MasterURLs) > 0 || a.cfg.MasterURL != "" {
		a.logger.Warn("no explicit nats_url and no bootstrap cache yet; using builtin default until enrollment discovery populates it",
			"builtin", builtinNATSTail)
	}
	return []string{builtinNATSTail}
}

// identityHash keys the cache to the bootstrap identity (master URLs + pins),
// so a changed master_urls / pin invalidates the cache.
func (a *Agent) identityHash() string {
	h := sha256.New()
	for _, u := range a.cfg.MasterURLs {
		h.Write([]byte(u))
		h.Write([]byte{0})
	}
	h.Write([]byte(a.cfg.MasterURL))
	h.Write([]byte{0})
	for _, p := range a.cfg.EnrollCAPin {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	// hex, not raw bytes: the hash is stored in the JSON cache, and raw
	// non-UTF-8 bytes in a JSON string are lossily replaced (U+FFFD), which
	// would break the identity comparison on reload.
	return hex.EncodeToString(h.Sum(nil))
}

// persistBootstrap writes the CA trust bundle to the resolved nats_ca path and
// the validated NATS endpoint list to the bootstrap cache, after a successful
// enrollment. The anchor file (enroll-ca.crt) is already persisted by
// ResolveTrust. Best-effort: failures are logged, never fatal.
//
// SECURITY: the NATS CA written here is the VERIFIED anchor
// (trust.Anchor) — never the raw ca_bundle_pem from the insecure candidate
// fetch, which an active first-contact MITM could poison with extra certs.
// In embedded mode the enrollment and NATS certs share the one CA root, so
// the pin/anchor-verified root is exactly the NATS trust anchor. The NATS
// endpoint list is taken from a doc RE-FETCHED over the verified TLS config
// (not the insecure fetch), so a MITM cannot inject attacker NATS URLs even
// when a correct enroll_ca_pin is set.
func (a *Agent) persistBootstrap(trust *enroll.ResolvedTrust) {
	if trust == nil || trust.Anchor == nil {
		return
	}

	// Write the verified anchor as the NATS CA (unless the operator
	// configured an explicit nats_ca, which wins and is left untouched).
	if a.cfg.NatsCA == "" {
		caPath := filepath.Join(a.cfg.AuthDir, "nats-ca.crt")
		if err := writeFileAtomic(caPath, ca.EncodeCertPEM(trust.Anchor), 0644); err != nil {
			a.logger.Warn("bootstrap: failed to write NATS CA", "path", caPath, "error", err)
		} else {
			a.logger.Info("bootstrap: wrote NATS CA from verified enrollment anchor", "path", caPath)
		}
	}

	// Re-fetch the bootstrap document over the VERIFIED TLS config to obtain
	// trustworthy NATS endpoints. A failure here is non-fatal: the peel falls
	// to the builtin tail and self-heals via the connected-phase watch.
	doc, err := enroll.FetchBootstrap(a.peelMasterURLs(), trust.TLSConfig)
	if err != nil {
		a.logger.Warn("bootstrap: verified endpoint fetch failed; will discover after connect", "error", err)
		return
	}
	a.writeBootstrapCache(doc.NATSURLs, string(trust.Source), doc.Fingerprint)
}

// writeBootstrapCache validates and persists the NATS endpoint list. Every
// candidate list — fetched, KV-delivered, or read from disk — passes the
// tls://-only + loopback gate; an unvalidated cache would brick a boot
// (ValidateTLSNATSURLs is fatal on a bad URL).
func (a *Agent) writeBootstrapCache(natsURLs []string, source, caFingerprint string) {
	accepted, rejected := bus.ValidateAdvertisableNATSURLs(natsURLs)
	for url, reason := range rejected {
		a.logger.Warn("bootstrap cache: dropping non-advertisable NATS URL", "url", url, "reason", reason)
	}
	if len(accepted) == 0 {
		return
	}
	cache := bootstrapCache{
		V:             1,
		NATSURLs:      accepted,
		Source:        source,
		IdentityHash:  a.identityHash(),
		CAFingerprint: caFingerprint,
	}
	data, err := json.Marshal(cache)
	if err != nil {
		a.logger.Warn("bootstrap cache: marshal", "error", err)
		return
	}
	if err := writeFileAtomic(a.bootstrapCachePath(), data, 0600); err != nil {
		a.logger.Warn("bootstrap cache: write", "path", a.bootstrapCachePath(), "error", err)
		return
	}
	a.logger.Info("bootstrap cache written", "nats_urls", len(accepted), "source", source)
}

// peelMasterURLs returns the enrollment base URLs (list preferred, single
// fallback).
func (a *Agent) peelMasterURLs() []string {
	if len(a.cfg.MasterURLs) > 0 {
		return a.cfg.MasterURLs
	}
	if a.cfg.MasterURL != "" {
		return []string{a.cfg.MasterURL}
	}
	return nil
}

// startDiscoveryRefresh runs the two live-refresh mechanisms for a
// discovery-driven peel: the cluster-info KV watch (fast path on a running
// connection) and the recovery loop (unhealthy > T → re-discover via the
// enrollment channel). Both apply new NATS endpoints via SetServers without a
// process restart.
func (a *Agent) startDiscoveryRefresh(ctx context.Context) {
	go a.watchClusterInfo(ctx)
	a.recoveryLoop(ctx)
}

// watchClusterInfo watches the secrets-bucket cluster-info key and applies new
// bootstrap docs (NATS endpoints + CA) as they are published.
func (a *Agent) watchClusterInfo(ctx context.Context) {
	kv, err := bus.GetBucket(ctx, a.client.JetStream(), bus.BucketSecrets)
	if err != nil {
		a.logger.Warn("discovery: cannot open secrets bucket for cluster-info watch", "error", err)
		return
	}
	watcher, err := kv.Watch(ctx, settings.ClusterInfoKey)
	if err != nil {
		a.logger.Warn("discovery: cannot watch cluster-info", "error", err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-watcher.Updates():
			if !ok {
				return
			}
			if entry == nil || len(entry.Value()) == 0 {
				continue
			}
			var doc enroll.BootstrapDoc
			if err := json.Unmarshal(entry.Value(), &doc); err != nil {
				a.logger.Warn("discovery: cluster-info decode", "error", err)
				continue
			}
			a.applyBootstrapDoc(&doc, "kv")
		}
	}
}

// recoveryLoop re-discovers endpoints when the connection has been unhealthy
// past the threshold — the "all cached NATS URLs dead" self-heal. It fetches
// the current bootstrap doc over anchor/pin-verified TLS and applies it.
func (a *Agent) recoveryLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	var unhealthySince time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if a.client.IsHealthy() {
			unhealthySince = time.Time{}
			continue
		}
		if unhealthySince.IsZero() {
			unhealthySince = time.Now()
			continue
		}
		if time.Since(unhealthySince) < recoveryUnhealthyThreshold {
			continue
		}
		a.logger.Warn("discovery: NATS unhealthy past threshold; re-discovering endpoints via enrollment")
		trust, err := enroll.ResolveTrust(enroll.TrustConfig{
			EnrollCAFile: a.cfg.EnrollCA,
			Pins:         a.cfg.EnrollCAPin,
			AnchorFile:   filepath.Join(a.cfg.AuthDir, "enroll-ca.crt"),
			Mode:         enroll.TrustMode(a.cfg.EnrollTrust),
			FirstContact: false, // already enrolled: never re-TOFU
			MasterURLs:   a.peelMasterURLs(),
			Logger:       a.logger,
		})
		if err != nil {
			a.logger.Warn("discovery: recovery trust resolve failed", "error", err)
			unhealthySince = time.Now() // back off a full threshold
			continue
		}
		doc, err := enroll.FetchBootstrap(a.peelMasterURLs(), trust.TLSConfig)
		if err != nil {
			a.logger.Warn("discovery: recovery fetch failed", "error", err)
			unhealthySince = time.Now()
			continue
		}
		a.applyBootstrapDoc(doc, "recovery")
		unhealthySince = time.Now() // re-arm; SetServers takes effect async
	}
}

// applyBootstrapDoc validates and applies a fetched/watched bootstrap doc:
// rewrites the NATS-CA file (unless an explicit nats_ca is configured),
// persists the cache, and repoints the live NATS pool when the endpoint set
// changed.
func (a *Agent) applyBootstrapDoc(doc *enroll.BootstrapDoc, source string) {
	if doc == nil {
		return
	}
	if doc.CABundlePEM != "" && a.cfg.NatsCA == "" {
		caPath := filepath.Join(a.cfg.AuthDir, "nats-ca.crt")
		if err := writeFileAtomic(caPath, []byte(doc.CABundlePEM), 0644); err != nil {
			a.logger.Warn("discovery: write NATS CA", "error", err)
		}
	}
	accepted, rejected := bus.ValidateAdvertisableNATSURLs(doc.NATSURLs)
	for url, reason := range rejected {
		a.logger.Warn("discovery: dropping non-advertisable NATS URL", "url", url, "reason", reason)
	}
	if len(accepted) == 0 {
		return
	}
	a.writeBootstrapCache(accepted, source, doc.Fingerprint)
	if err := a.client.SetServers(accepted); err != nil {
		a.logger.Warn("discovery: SetServers failed", "error", err)
		return
	}
	a.logger.Info("discovery: applied NATS endpoints", "source", source, "count", len(accepted))
}

// loadBootstrapCache reads and validates the cache, returning the NATS URL
// list. A missing file or identity mismatch yields nil (the caller falls
// through the precedence chain). Invalid entries are dropped with a warning
// (the file is kept for forensics).
func (a *Agent) loadBootstrapCache() []string {
	data, err := os.ReadFile(a.bootstrapCachePath())
	if err != nil {
		return nil
	}
	var cache bootstrapCache
	if err := json.Unmarshal(data, &cache); err != nil {
		a.logger.Warn("bootstrap cache: unreadable, ignoring", "error", err)
		return nil
	}
	if cache.IdentityHash != a.identityHash() {
		a.logger.Warn("bootstrap cache: identity changed (master_urls/pin edited); ignoring stale cache")
		return nil
	}
	accepted, rejected := bus.ValidateAdvertisableNATSURLs(cache.NATSURLs)
	for url, reason := range rejected {
		a.logger.Warn("bootstrap cache: dropping invalid cached NATS URL", "url", url, "reason", reason)
	}
	return accepted
}
