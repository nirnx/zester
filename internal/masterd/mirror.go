package masterd

import (
	"context"
	"sync"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/statefiles"
)

// mirrorLeaseTTLDefault mirrors pkg/bus's default lease TTL; the standby
// mirror starts after ~2 TTLs without leadership (see startMirrorsWhenStandby).
const mirrorLeaseTTLDefault = 15 * time.Second

// filesMirror is one KV→dir mirror (a statefiles.Cache) plus its watch
// lifecycle. Master standbys run one per file set so their source dirs
// continuously track published truth — the failover-revert killer: a new
// lease holder's dirs already ARE the last-published tree, so its initial
// (hash-gated) publish is a no-op and the fleet never regresses.
type filesMirror struct {
	name   string
	cache  *statefiles.Cache
	cancel context.CancelFunc
}

// buildFilesMirrors constructs the per-set mirrors. Mirrored trees are
// MANAGED: a sync replaces the dir with exactly the published file set, so
// unpublished extras (hidden files, manual checkouts) do not survive on
// standbys. Set specifics:
//   - SETTINGS mirror from the SEALED masters-only master-settings bucket
//     (raw tree, each file sealed to the shared account curve key), NEVER
//     from the peel-facing sanitized bucket — a sanitized mirror would
//     destroy the !encrypted plaintext on every standby. Requires
//     d.masterEnc (skipped if settings never initialized). After each sync
//     the in-memory secret-extraction state is refreshed.
//   - states when GitFS is configured (the dir is a git clone; masters
//     converge through the remote, and a tree swap would destroy .git);
//   - everything when files_mirror is off (e.g. masters sharing one volume,
//     where the holder's own publishes would churn the standby mirror).
func (d *Daemon) buildFilesMirrors() []*filesMirror {
	if d.cfg == nil || !d.cfg.FilesMirror {
		return nil
	}
	var mirrors []*filesMirror

	if len(d.cfg.GitFS.Remotes) == 0 && d.cfg.StatesDir != "" {
		mirrors = append(mirrors, &filesMirror{
			name: "states",
			cache: statefiles.NewCache(statefiles.CacheConfig{
				CacheDir:       d.cfg.StatesDir,
				JS:             d.js,
				Logger:         d.logger,
				Bucket:         bus.BucketStateFiles,
				WarnLocalEdits: true,
			}),
		})
	}
	if d.cfg.SettingsDir != "" && d.masterEnc != nil {
		enc := d.masterEnc
		mirrors = append(mirrors, &filesMirror{
			name: "settings",
			cache: statefiles.NewCache(statefiles.CacheConfig{
				CacheDir: d.cfg.SettingsDir,
				JS:       d.js,
				Logger:   d.logger,
				Bucket:   bus.BucketMasterSettings,
				DecodeValue: func(_ string, stored []byte) ([]byte, error) {
					return enc.Open(stored, enc.PublicKey())
				},
				OnSynced:       d.refreshSettingsStateFromDisk,
				WarnLocalEdits: true,
			}),
		})
	}
	if d.cfg.Reactor.Dir != "" {
		mirrors = append(mirrors, &filesMirror{
			name: "reactor",
			cache: statefiles.NewCache(statefiles.CacheConfig{
				CacheDir:       d.cfg.Reactor.Dir,
				JS:             d.js,
				Logger:         d.logger,
				Bucket:         bus.BucketReactorFiles,
				KeyPrefix:      "reactor/",
				WarnLocalEdits: true,
			}),
		})
	}
	return mirrors
}

// startFilesMirrors begins mirroring KV→dir for every configured set:
// an immediate initial sync plus a revision watch. Idempotent (no-op while
// already running). Called when this master is a STANDBY — never while it
// holds the publisher lease (writer and mirror must not share a dir).
func (d *Daemon) startFilesMirrors(ctx context.Context) {
	d.mirrorMu.Lock()
	defer d.mirrorMu.Unlock()
	// Re-check leadership UNDER the mirror lock: the standby timer's
	// leadership check races a concurrent acquisition, and a mirror must
	// never start on the leader (runLeaderPublish's stopFilesMirrors
	// serializes on this same lock).
	if d.publisherLeader() {
		return
	}
	if d.mirrorsRunning {
		return
	}
	if d.mirrors == nil {
		d.mirrors = d.buildFilesMirrors()
	}

	// A standby settings-state refresh runs regardless of whether any mirror
	// set is configured — including files_mirror=false (shared-volume
	// masters see the leader's live dir but their in-memory
	// allSecrets/topFile would otherwise stay boot-stale, so a standby that
	// wins the facts-secrets lease would encrypt stale values). Ticks read
	// the local dir; on own-disk masters the mirror keeps it current, on
	// shared volumes it IS the leader's current dir.
	refreshCtx, refreshCancel := context.WithCancel(ctx)
	d.standbyRefreshCancel = refreshCancel
	go d.runStandbySettingsRefresh(refreshCtx)

	d.mirrorsRunning = true
	if len(d.mirrors) == 0 {
		d.logger.Debug("standby settings refresh started (no file mirrors configured)")
		return
	}
	for _, m := range d.mirrors {
		mctx, cancel := context.WithCancel(ctx)
		m.cancel = cancel
		go m.cache.TriggerSync(mctx)
		if stop, err := m.cache.Watch(mctx); err != nil {
			d.logger.Warn("files mirror: watch failed; standby dir will lag until leadership or restart",
				"set", m.name, "error", err)
		} else {
			prev := m.cancel
			m.cancel = func() { stop(); prev() }
		}
	}
	d.logger.Info("standby file mirrors started (source dirs now track published truth)", "sets", len(d.mirrors))
}

// runStandbySettingsRefresh periodically re-reads the settings dir into the
// in-memory secret-extraction state while this master is a standby. Cheap
// (dir read + sanitize), hash-gate-free — the point is only that a
// facts-secrets-lease-holding standby encrypts CURRENT secret values.
func (d *Daemon) runStandbySettingsRefresh(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.refreshSettingsStateFromDisk()
		}
	}
}

// stopFilesMirrors cancels the mirror watches and the standby refresh, and
// waits out any in-flight sync, so a takeover never races a half-finished
// mirror write. Idempotent.
func (d *Daemon) stopFilesMirrors() {
	d.mirrorMu.Lock()
	defer d.mirrorMu.Unlock()
	if !d.mirrorsRunning {
		return
	}
	if d.standbyRefreshCancel != nil {
		d.standbyRefreshCancel()
		d.standbyRefreshCancel = nil
	}
	for _, m := range d.mirrors {
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		m.cache.Quiesce()
	}
	d.mirrorsRunning = false
}

// catchUpFilesMirrors runs one final KV→dir sync per mirror on lease
// acquisition — closing the mirror-lag race: anything the previous holder
// published in its final seconds lands on disk BEFORE this master's first
// publish, which then hash-gates to a no-op.
//
// Only mirrors that completed at least one sync this lifetime catch up. A
// fresh boot that wins the lease immediately (the single-master case) skips
// it, so a master restarted after offline edits still publishes its OWN tree
// — today's semantics — instead of having the edits overwritten from KV.
func (d *Daemon) catchUpFilesMirrors(ctx context.Context) {
	d.mirrorMu.Lock()
	mirrors := d.mirrors
	d.mirrorMu.Unlock()
	for _, m := range mirrors {
		if !m.cache.HasSynced() {
			continue
		}
		if _, err := m.cache.Sync(ctx); err != nil {
			d.logger.Warn("files mirror: takeover catch-up sync failed; publishing the local tree as-is",
				"set", m.name, "error", err)
		}
	}
}

// startMirrorsWhenStandby arms the standby detection: if this master does
// not hold the publisher lease ~2 lease TTLs from now, another master holds
// it — start mirroring. Used both at boot and on lease loss (OnLost). The
// delay is load-bearing in both cases: a fresh single-master boot acquires
// within milliseconds (pre-acquisition mirroring would overwrite offline
// edits with old KV content), and a lease loss from a transient NATS blip
// re-acquires within ~one TTL (an instant mirror could race the
// re-acquisition's catch-up). The leadership check runs again inside
// startFilesMirrors under the mirror lock.
func (d *Daemon) startMirrorsWhenStandby(ctx context.Context) {
	ttl := d.leaseTTL
	if ttl <= 0 {
		ttl = mirrorLeaseTTLDefault
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * ttl):
		}
		if d.publisherLeader() {
			return
		}
		d.startFilesMirrors(ctx)
	}()
}

// mirrorState is the Daemon's mirror bookkeeping, embedded in Daemon.
type mirrorState struct {
	mirrorMu             sync.Mutex
	mirrors              []*filesMirror
	mirrorsRunning       bool
	standbyRefreshCancel context.CancelFunc
}
