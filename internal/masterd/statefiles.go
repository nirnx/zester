package masterd

import (
	"context"
	"fmt"
	"time"

	"github.com/nirnx/zester/internal/health"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/statefiles"
)

// startStatefilesPublisher creates the state-files publisher on
// d.statePublisher. The actual publish of the on-disk state tree to the
// state-files bucket is leader-only (publishStateFiles, called from
// runLeaderPublish), keeping the KV writes single-writer; the GitFS syncer
// reuses the same publisher to republish after each sync.
func (d *Daemon) startStatefilesPublisher(ctx context.Context) error {
	stateFilesKV, err := bus.GetBucket(ctx, d.client.JetStream(), bus.BucketStateFiles)
	if err != nil {
		return fmt.Errorf("get state-files bucket: %w", err)
	}
	d.statePublisher = statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: d.cfg.StatesDir,
		KV:        stateFilesKV,
		Logger:    d.logger,
	})
	return nil
}

// publishStateFiles publishes the on-disk state tree to the state-files KV
// bucket. Called only by the publisher-lease holder (runLeaderPublish).
func (d *Daemon) publishStateFiles(ctx context.Context) {
	if count, err := d.statePublisher.Publish(ctx); err != nil {
		d.logger.Warn("failed to publish state files", "error", err)
	} else {
		d.logger.Info("published state files", "count", count)
	}
}

// startGitFS constructs the GitFS syncer and registers the 'gitfs'
// readiness check when remotes are configured. The sync loop itself runs
// only while this master holds the publisher lease (runLeaderPublish), so
// on a standby master the check reports Degraded ("no successful sync yet")
// — which keeps /readyz at 200 — until the lease is acquired. GitFS.Run
// logs sync/publish errors and retries on the next tick (it never exits on
// error); gitfsCheck reports staleness from the last-successful-sync
// timestamp and flips Down if the syncer goroutine ever exits while the
// lease is still held.
func (d *Daemon) startGitFS() {
	if len(d.cfg.GitFS.Remotes) == 0 {
		return
	}
	gitfsInterval := d.cfg.GitFS.Interval
	if gitfsInterval <= 0 {
		gitfsInterval = 5 * time.Minute
	}
	d.gitfsInterval = gitfsInterval
	d.gitfs = statefiles.NewGitFS(statefiles.GitFSConfig{
		Remotes:    d.cfg.GitFS.Remotes,
		StatesDir:  d.cfg.StatesDir,
		Interval:   d.cfg.GitFS.Interval,
		SSHKeyPath: d.cfg.GitFS.SSHKey,
		Logger:     d.logger,
		OnSyncSuccess: func() {
			d.gitfsLastSync.Store(time.Now().UnixNano())
		},
	}, d.statePublisher)
	d.checker.Register("gitfs", d.gitfsCheck)
}

// gitfsCheck is the 'gitfs' readiness check: Down if the syncer goroutine
// exited (d.gitfsExit), Degraded until the first successful sync or when the
// last successful sync (d.gitfsLastSync, unix nanos) is at least three
// intervals old, OK otherwise.
func (d *Daemon) gitfsCheck(context.Context) health.CheckResult {
	if msg, ok := d.gitfsExit.Load().(string); ok {
		return health.CheckResult{Status: health.StatusDown, Message: msg}
	}
	last := d.gitfsLastSync.Load()
	if last == 0 {
		return health.CheckResult{Status: health.StatusDegraded, Message: "no successful sync yet"}
	}
	if age := time.Since(time.Unix(0, last)); age >= 3*d.gitfsInterval {
		return health.CheckResult{
			Status:  health.StatusDegraded,
			Message: fmt.Sprintf("last successful sync %s ago (interval %s)", age.Round(time.Second), d.gitfsInterval),
		}
	}
	return health.CheckResult{Status: health.StatusOK}
}
