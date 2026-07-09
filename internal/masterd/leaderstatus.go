package masterd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nirnx/zester/internal/health"
)

// leaderStatus tracks the publisher-lease role for the operator-facing
// surfaces: the status file (MOTD), the readyz entry, the metrics gauge, and
// `zester fileserver status`. Embedded in Daemon.
type leaderStatus struct {
	leaderMu    sync.Mutex
	leaderRole  string // "leader" | "standby"
	leaderSince time.Time
}

// setPublisherRole records a lease transition and fans it out to every
// notification surface. hostname is resolved once per call (cheap, and it
// can legitimately change).
func (d *Daemon) setPublisherRole(role string) {
	now := time.Now().UTC()
	d.leaderMu.Lock()
	changed := d.leaderRole != role
	if changed {
		d.leaderRole = role
		d.leaderSince = now
	}
	role, since := d.leaderRole, d.leaderSince
	d.leaderMu.Unlock()
	if !changed {
		return
	}

	if d.reg != nil {
		v := 0.0
		if role == "leader" {
			v = 1.0
		}
		d.reg.PublisherLeader.Set(v)
	}
	d.writePublisherStatusFile(role, since)
}

// publisherRole returns the current role ("standby" before the first
// transition) and when it began.
func (d *Daemon) publisherRole() (string, time.Time) {
	d.leaderMu.Lock()
	defer d.leaderMu.Unlock()
	role := d.leaderRole
	if role == "" {
		role = "standby"
	}
	return role, d.leaderSince
}

// writePublisherStatusFile atomically rewrites the operator-facing status
// file. Failures are Debug-level: /run/zester exists only under the packaged
// unit (RuntimeDirectory=zester), and a dev box without it should not warn
// on every transition.
func (d *Daemon) writePublisherStatusFile(role string, since time.Time) {
	if d.cfg == nil || d.cfg.PublisherStatusFile == "" {
		return
	}
	path := d.cfg.PublisherStatusFile
	hostname, _ := os.Hostname()
	content := fmt.Sprintf("role=%s\nmaster_id=%s\nhostname=%s\nsince=%s\n",
		role, d.masterID, hostname, since.Format(time.RFC3339))

	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		d.logger.Debug("publisher status file unavailable", "path", path, "error", err)
		return
	}
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		d.logger.Debug("publisher status file write failed", "path", path, "error", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		d.logger.Debug("publisher status file rename failed", "path", path, "error", err)
	}
}

// publisherLeaseCheck is the informational readyz entry: always OK — standby
// is a healthy state — with the role in the message so `curl /readyz` (and
// dashboards) show file-distribution leadership placement.
func (d *Daemon) publisherLeaseCheck(context.Context) health.CheckResult {
	role, since := d.publisherRole()
	msg := role
	if !since.IsZero() {
		msg = fmt.Sprintf("%s since %s", role, since.Format(time.RFC3339))
	}
	return health.CheckResult{Status: health.StatusOK, Message: msg}
}
