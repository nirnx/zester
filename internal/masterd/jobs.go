package masterd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ptorbus/zester/internal/health"
	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/job"
)

// startJobManager creates the job manager with the master's instance ID for
// ownership tracking and wires the job-finalization metrics hook. The caller
// (Run) registers the jobMgr.Shutdown defer.
func (d *Daemon) startJobManager() {
	d.jobMgr = job.NewManager(bus.NewNATSPubSub(d.client.Conn()), d.client.JetStream(), d.masterID, d.logger)
	d.jobMgr.OnJobFinalized = d.onJobFinalized
}

// onJobFinalized records per-status job counts and per-function duration
// histograms in the Prometheus registry.
func (d *Daemon) onJobFinalized(function, status string, duration time.Duration) {
	d.reg.JobsTotal.WithLabelValues(status).Inc()
	d.reg.JobDuration.WithLabelValues(function).Observe(duration.Seconds())
}

// schedConsumerRetryInterval paces the background retry loop that keeps
// trying to start the scheduled-result consumer after a boot failure.
const schedConsumerRetryInterval = 60 * time.Second

// startSchedConsumer consumes peel scheduler results from the job-events
// stream and persists them as synthetic jobs. The durable consumer is shared
// by all masters; peels have no write access to the job KV buckets. When the
// consumer fails to start, startup continues with a Warn and a Down
// readiness check, and a background retry loop (retrySchedConsumer) keeps
// trying every schedConsumerRetryInterval, flipping the check to OK on
// success. The returned stop function stops whichever consumer instance is
// running at shutdown time (no-op when none ever started).
func (d *Daemon) startSchedConsumer(ctx context.Context) func() {
	if d.schedStart == nil {
		d.schedStart = func(ctx context.Context) (func(), error) {
			return job.StartScheduledResultConsumer(ctx, d.client.JetStream(), d.client.JetStream(), d.logger)
		}
	}
	if d.schedRetryInterval <= 0 {
		d.schedRetryInterval = schedConsumerRetryInterval
	}

	stop, err := d.schedStart(ctx)
	if err != nil {
		d.logger.Warn("scheduled-result consumer failed to start; scheduler return_job results will not be persisted", "error", err)
		d.schedState.Store(health.CheckResult{Status: health.StatusDown, Message: err.Error()})
		go d.retrySchedConsumer(ctx)
	} else {
		d.schedState.Store(health.CheckResult{Status: health.StatusOK})
		d.setSchedStop(stop)
	}
	d.checker.Register("sched-consumer", d.schedConsumerCheck)
	return d.shutdownSchedConsumer
}

// retrySchedConsumer retries starting the scheduled-result consumer until
// it succeeds or ctx is cancelled. On success it records the stop function
// and flips the 'sched-consumer' readiness check to OK — the check is no
// longer frozen Down for the daemon's lifetime after a transient boot
// failure (e.g. the job-events stream not yet reachable).
func (d *Daemon) retrySchedConsumer(ctx context.Context) {
	ticker := time.NewTicker(d.schedRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		stop, err := d.schedStart(ctx)
		if err != nil {
			d.logger.Warn("scheduled-result consumer retry failed", "error", err)
			d.schedState.Store(health.CheckResult{Status: health.StatusDown, Message: err.Error()})
			continue
		}
		if !d.setSchedStop(stop) {
			return // shut down while retrying; the fresh consumer was stopped
		}
		d.schedState.Store(health.CheckResult{Status: health.StatusOK})
		d.logger.Info("scheduled-result consumer started after retry")
		return
	}
}

// setSchedStop records the stop function of the currently running consumer.
// It returns false — after stopping the consumer — when shutdown has
// already run, so a retry that wins a race against shutdown does not leak a
// running consumer.
func (d *Daemon) setSchedStop(stop func()) bool {
	d.schedStopMu.Lock()
	if d.schedStopped {
		d.schedStopMu.Unlock()
		stop()
		return false
	}
	d.schedStop = stop
	d.schedStopMu.Unlock()
	return true
}

// shutdownSchedConsumer stops the currently running consumer (if any) and
// marks the consumer stopped for good.
func (d *Daemon) shutdownSchedConsumer() {
	d.schedStopMu.Lock()
	stop := d.schedStop
	d.schedStop = nil
	d.schedStopped = true
	d.schedStopMu.Unlock()
	if stop != nil {
		stop()
	}
}

// schedConsumerCheck is the 'sched-consumer' readiness check; it returns
// the latest result recorded by startSchedConsumer or the retry loop.
func (d *Daemon) schedConsumerCheck(context.Context) health.CheckResult {
	if v, ok := d.schedState.Load().(health.CheckResult); ok {
		return v
	}
	return health.CheckResult{Status: health.StatusDown, Message: "not started"}
}

// subscribeDispatch subscribes to job dispatch requests via the masters
// queue group. Only one master in the group receives each dispatch (HA-R3).
// The returned cleanup unsubscribes.
func (d *Daemon) subscribeDispatch() (func(), error) {
	dispatchSub, err := d.nc.QueueSubscribe(bus.SubjectDispatch, masterQueueGroup, d.handleDispatch)
	if err != nil {
		return nil, fmt.Errorf("subscribe job dispatch: %w", err)
	}
	return func() { _ = dispatchSub.Unsubscribe() }, nil
}

// handleDispatch is the dispatch-subject callback (formerly an inline
// closure): decode the job, write the dispatch audit log line, and hand the
// job to the manager. It uses d.runCtx — the daemon lifecycle context — as
// the dispatch context, exactly as the closure captured run()'s ctx.
func (d *Daemon) handleDispatch(msg *nats.Msg) {
	var j job.Job
	if err := bus.Decode(msg.Data, &j); err != nil {
		d.logger.Error("decode dispatch request", "error", err)
		if msg.Reply != "" {
			respondMap(msg, map[string]string{"error": "decode: " + err.Error()})
		}
		return
	}
	// Dispatch audit log: who ran what, where (j.User is set by the
	// CLI/API from the caller's identity; '-' for legacy callers).
	user := j.User
	if user == "" {
		user = "-"
	}
	d.logger.Info("dispatch request received",
		"jid", j.JID, "user", user, "function", j.Function,
		"targets", len(j.Targets), "master", d.masterID)

	if err := d.jobMgr.Dispatch(d.runCtx, &j); err != nil {
		d.logger.Error("dispatch job", "jid", j.JID, "error", err)
		if msg.Reply != "" {
			respondMap(msg, map[string]string{"error": err.Error()})
		}
		return
	}
	if msg.Reply != "" {
		respondMap(msg, map[string]string{"jid": j.JID})
	}
}

// subscribeCancel subscribes to the job cancel wildcard. All masters
// subscribe so any master can cancel jobs it owns locally (HA-R8). The
// returned cleanup unsubscribes.
func (d *Daemon) subscribeCancel() (func(), error) {
	cancelSubject := bus.SubjectJob + ".*.cancel"
	cancelSub, err := d.nc.Subscribe(cancelSubject, d.handleCancel)
	if err != nil {
		return nil, fmt.Errorf("subscribe job cancel: %w", err)
	}
	return func() { _ = cancelSub.Unsubscribe() }, nil
}

// handleCancel is the cancel-wildcard callback (formerly an inline closure).
func (d *Daemon) handleCancel(msg *nats.Msg) {
	// Extract JID from subject: zester.job.<jid>.cancel
	parts := strings.Split(msg.Subject, ".")
	if len(parts) < 3 {
		return
	}
	jid := parts[2]
	d.logger.Info("job cancel request received", "jid", jid, "master", d.masterID)
	// Use CancelLocal to avoid re-publishing to the same subject
	// (all masters subscribe, so re-publishing would cause an infinite loop).
	d.jobMgr.CancelLocal(jid)
}

// startHeartbeatAndOrphanScanner starts the master heartbeat (HA-R7:
// interval=5s, bucket TTL=15s) and the orphan scanner that reclaims jobs
// from dead masters.
func (d *Daemon) startHeartbeatAndOrphanScanner(ctx context.Context) {
	heartbeater := job.NewHeartbeater(d.masterID, d.client.JetStream(), d.logger, d.jobMgr.ActiveJobs)
	go heartbeater.Run(ctx)

	scanner := job.NewOrphanScanner(d.masterID, d.client.JetStream(), d.logger, d.reclaimJob)
	scanner.OnReclaim = d.reg.JobReclaims.Inc
	go scanner.Run(ctx)
	d.logger.Info("heartbeat and orphan scanner started")
}

// reclaimJob is the orphan scanner's reclaim callback (formerly an inline
// closure): it hands orphaned jobs back to this master's job manager.
func (d *Daemon) reclaimJob(rctx context.Context, j *job.Job) {
	d.jobMgr.ReclaimJob(rctx, j)
}

func respondMap(msg *nats.Msg, m map[string]string) {
	data, err := bus.Encode(m)
	if err != nil {
		return
	}
	msg.Respond(data)
}
