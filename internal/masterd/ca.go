package masterd

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nirnx/zester/internal/health"
	"github.com/nirnx/zester/pkg/ca"
)

// caManager owns the embedded CA in embedded mode: the loaded Authority, the
// in-memory self-issued enrollment leaf (served via GetCertificate and
// renewed in-process), and the trust bundle served over discovery. It is nil
// in external mode.
type caManager struct {
	authority *ca.Authority
	validity  time.Duration
	sans      []string
	logger    *slog.Logger

	mu   sync.RWMutex
	leaf *tls.Certificate // current enrollment cert, hot-swapped by renew
}

// caResolvedMode is the effective CA mode after resolving "auto".
type caResolvedMode int

const (
	caModeExternal caResolvedMode = iota
	caModeEmbedded
)

// caDir returns the configured CA directory, defaulting to <auth_dir>/ca.
func (d *Daemon) caDir() string {
	if d.cfg.CA.Dir != "" {
		return d.cfg.CA.Dir
	}
	return filepath.Join(d.cfg.AuthDir, "ca")
}

// resolveCAMode maps the configured mode (auto|embedded|external) to the
// effective mode. auto = embedded iff CA material exists.
func (d *Daemon) resolveCAMode() (caResolvedMode, error) {
	switch strings.ToLower(strings.TrimSpace(d.cfg.CA.Mode)) {
	case "", "auto":
		if ca.Exists(d.caDir()) {
			return caModeEmbedded, nil
		}
		return caModeExternal, nil
	case "embedded":
		return caModeEmbedded, nil
	case "external":
		return caModeExternal, nil
	default:
		return caModeExternal, fmt.Errorf("invalid ca.mode %q (want auto|embedded|external)", d.cfg.CA.Mode)
	}
}

// startCA resolves the CA mode and, in embedded mode, loads the CA, issues
// the initial enrollment leaf, registers the 'ca' readiness check, and starts
// the renewal loop. It returns the GetCertificate callback to hand to the
// enrollment server (nil in external mode, so the server falls back to the
// operator-provided enroll cert/key files). Embedded mode with absent CA
// material is a fatal misconfiguration.
func (d *Daemon) startCA(ctx context.Context) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), error) {
	mode, err := d.resolveCAMode()
	if err != nil {
		return nil, err
	}
	if mode == caModeExternal {
		d.logger.Info("CA mode: external (operator-provided enrollment certificate)")
		return nil, nil
	}

	dir := d.caDir()
	authority, err := ca.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("embedded CA: %w (run 'zester ca init --dir %s')", err, dir)
	}
	if authority.IntermediateKey == nil {
		return nil, fmt.Errorf("embedded CA in %s has no intermediate key; cannot issue the enrollment certificate", dir)
	}

	m := &caManager{
		authority: authority,
		validity:  time.Duration(d.cfg.CA.EnrollCertValidity),
		sans:      d.enrollSANs(),
		logger:    d.logger,
	}
	if m.validity <= 0 {
		m.validity = 90 * 24 * time.Hour
	}
	if err := m.issue(); err != nil {
		return nil, fmt.Errorf("embedded CA: issue enrollment certificate: %w", err)
	}
	d.caManager = m

	d.checker.Register("ca", d.caCheck)

	d.logger.Info("CA mode: embedded",
		"dir", dir,
		"root_pin", authority.RootSPKIPin(),
		"enroll_cert_validity", m.validity,
		"enroll_sans", m.sans,
	)

	go m.renewLoop(ctx)
	return m.getCertificate, nil
}

// enrollSANs computes the SAN set for the self-issued enrollment leaf: the
// machine hostname and localhost are always included, plus any operator
// additions. Peels must be able to verify the master's enroll endpoint by a
// name in this set, so operators add whatever names appear in peel
// master_urls.
func (d *Daemon) enrollSANs() []string {
	set := map[string]struct{}{"localhost": {}}
	if host, err := os.Hostname(); err == nil && host != "" {
		set[host] = struct{}{}
	}
	for _, s := range d.cfg.CA.EnrollSANs {
		if s = strings.TrimSpace(s); s != "" {
			set[s] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}

// issue mints a fresh enrollment leaf and swaps it in atomically.
func (m *caManager) issue() error {
	var dns []string
	var ips []net.IP
	for _, s := range m.sans {
		if ip := net.ParseIP(s); ip != nil {
			ips = append(ips, ip)
		} else {
			dns = append(dns, s)
		}
	}
	leaf, err := m.authority.IssueServer("zester-master", dns, ips, m.validity)
	if err != nil {
		return err
	}
	cert, err := tls.X509KeyPair(leaf.CertPEM, leaf.KeyPEM)
	if err != nil {
		return fmt.Errorf("load issued enroll leaf: %w", err)
	}
	m.mu.Lock()
	m.leaf = &cert
	m.mu.Unlock()
	return nil
}

// getCertificate serves the current enrollment leaf on every handshake.
func (m *caManager) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.leaf == nil {
		return nil, fmt.Errorf("enrollment certificate not yet issued")
	}
	return m.leaf, nil
}

// renewLoop re-issues the enrollment leaf at ~2/3 of its validity. On a
// renewal failure it retries on a SHORT cadence (not another full ⅔ interval,
// which at the 90-day default would guarantee an expired-cert window), until
// it succeeds and returns to the normal cadence.
func (m *caManager) renewLoop(ctx context.Context) {
	normal := m.validity * 2 / 3
	if normal < time.Minute {
		normal = time.Minute
	}
	const retry = time.Hour
	timer := time.NewTimer(normal)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := m.issue(); err != nil {
				m.logger.Error("embedded CA: enrollment certificate renewal failed; retrying soon", "error", err, "retry_in", retry)
				timer.Reset(retry)
				continue
			}
			timer.Reset(normal)
		}
	}
}

// bundle returns the CA trust bundle PEM (root only). Empty in external mode.
func (m *caManager) bundle() []byte {
	if m == nil {
		return nil
	}
	return m.authority.Bundle()
}

// rootPin returns the CA root SPKI pin. Empty in external mode.
func (m *caManager) rootPin() string {
	if m == nil {
		return ""
	}
	return m.authority.RootSPKIPin()
}

// caCheck is the 'ca' readiness check: Down until the leaf is issued,
// Degraded when the served leaf is within its renewal window (surfaces an
// approaching expiry before it becomes total), OK otherwise.
func (d *Daemon) caCheck(context.Context) health.CheckResult {
	m := d.caManager
	if m == nil {
		return health.CheckResult{Status: health.StatusOK}
	}
	m.mu.RLock()
	leaf := m.leaf
	m.mu.RUnlock()
	if leaf == nil || leaf.Leaf == nil {
		return health.CheckResult{Status: health.StatusDown, Message: "enrollment certificate not issued"}
	}
	// CA-chain expiry is fleet-fatal (every peel handshake fails once the
	// intermediate/root expires) and is not fixed by leaf renewal — surface
	// it. Down when already expired, Degraded when within 30 days.
	now := time.Now()
	for _, c := range []struct {
		name string
		na   time.Time
	}{{"CA root", m.authority.Root.NotAfter}, {"CA intermediate", m.authority.Intermediate.NotAfter}} {
		if now.After(c.na) {
			return health.CheckResult{Status: health.StatusDown, Message: fmt.Sprintf("%s expired %s", c.name, c.na.Format(time.RFC3339))}
		}
		if time.Until(c.na) < 30*24*time.Hour {
			return health.CheckResult{Status: health.StatusDegraded, Message: fmt.Sprintf("%s expires in %s", c.name, time.Until(c.na).Round(time.Hour))}
		}
	}
	if remaining := time.Until(leaf.Leaf.NotAfter); remaining < m.validity/3 {
		return health.CheckResult{
			Status:  health.StatusDegraded,
			Message: fmt.Sprintf("enrollment certificate expires in %s", remaining.Round(time.Minute)),
		}
	}
	return health.CheckResult{Status: health.StatusOK}
}
