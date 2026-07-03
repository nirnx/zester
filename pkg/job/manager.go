package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
)

// ErrJIDConflict marks a Dispatch rejected because the JID is already
// claimed by a job with DIFFERENT dispatch intent (function, args, targets,
// timeout, user, or metadata differ). Callers that mint deterministic,
// content-addressed JIDs on an exclusive keyspace — the reactor's "rxn-"
// jobs — classify errors.Is(err, ErrJIDConflict) as duplicate-suppressed
// success: an existing job under such a JID IS the same logical dispatch by
// construction, even when resolved targets drifted between redeliveries.
var ErrJIDConflict = errors.New("job: jid conflict")

// ErrJobClaimPending marks a Dispatch that collided with a job record still
// in StatusClaimed and owned by ANOTHER master. A claimed record is provably
// never-published, but its owner only resumes it if its own Dispatch call
// retries — a live master whose Dispatch already returned will never publish
// it, and the orphan scanner ignores jobs whose owner is alive. The
// collision is therefore neither a success nor a permanent conflict: callers
// on retryable transports (the reactor Naks the triggering event) retry
// until the owner resumes the claim or the orphan scanner reclaims it.
var ErrJobClaimPending = errors.New("job: claim pending")

// Manager dispatches jobs to peels, collects returns, and tracks job status.
// It uses NATS for pub/sub communication, KV buckets for persistence, and
// JetStream for event logging. In multi-master mode, each Manager is
// identified by a unique MasterID and uses KV CAS for job ownership.
type Manager struct {
	nc       bus.PubSub
	js       bus.JetStreamAPI
	masterID string
	logger   *slog.Logger

	// OnJobFinalized, if set, is invoked once whenever a job managed by
	// this instance reaches a terminal status (complete, failed, partial,
	// timeout, canceled). duration is the elapsed time since the job was
	// created (defensively zero if Created was never set). Optional metric
	// hook: nil means no-op. Set before dispatching; it is copied onto
	// every watcher this manager starts.
	OnJobFinalized func(function, status string, duration time.Duration)

	// AckWindow configures the silent-target re-dispatch on watchers
	// started for freshly published dispatches (Dispatch and the
	// claimed-job reclaim path): after this window, targets that neither
	// acked nor returned get the ExecRequest re-published once. 0 means
	// DefaultAckWindow (5s); a negative value disables reconciliation.
	// Set before dispatching (tests shrink it).
	AckWindow time.Duration

	// ReplayReturns, when non-nil, is called by ReclaimJob for running
	// jobs to recover returns published during the ownerless failover
	// window: peels kept publishing while no watcher was subscribed, and
	// those messages sit durably in the job-events stream. The result is
	// merged with the KV-seeded per-peel returns (KV wins on collision)
	// before the recovery watcher starts. It is invoked a second time
	// once the watcher's live subscriptions are active (mergeGapReturns),
	// so a return published between the first replay finishing and the
	// subscription attaching is not lost. nil = skip replay. NewManager
	// wires the production implementation (NewStreamReturnReplayer)
	// automatically when js also implements bus.ConsumerAPI; tests
	// substitute a fake function.
	ReplayReturns func(ctx context.Context, jid string) ([]Return, error)

	mu       sync.RWMutex
	watchers map[string]*Watcher // jid -> watcher
}

// NewManager creates a job manager connected to NATS.
// masterID uniquely identifies this master instance for job ownership.
func NewManager(nc bus.PubSub, js bus.JetStreamAPI, masterID string, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		nc:       nc,
		js:       js,
		masterID: masterID,
		logger:   logger,
		watchers: make(map[string]*Watcher),
	}
	// Production wiring: the bus.JS adapter also implements
	// bus.ConsumerAPI, so failover replay from the job-events stream is
	// available by default. Test fakes (bustest.FakeJS) don't, keeping
	// ReplayReturns nil unless a test sets a fake function.
	if consumers, ok := js.(bus.ConsumerAPI); ok {
		m.ReplayReturns = NewStreamReturnReplayer(consumers, logger)
	}
	return m
}

// MasterID returns this manager's unique instance identifier.
func (m *Manager) MasterID() string {
	return m.masterID
}

// Dispatch claims and sends a job to the targeted peels.
// It uses KV CAS to claim ownership, preventing duplicate dispatch across
// multiple masters. A JID collision resolves by the existing record's state:
// our own still-claimed record resumes the interrupted dispatch, another
// master's still-claimed record returns ErrJobClaimPending (retryable — see
// the sentinel), a past-claimed record with matching intent is an idempotent
// no-op, and one with different intent returns ErrJIDConflict.
//
// Ordering invariant (architecture review finding 17): intent is persisted
// BEFORE anything is published — Create(claimed) → CAS to StatusRunning →
// publish ExecRequests → start watcher. A job record in StatusClaimed is
// therefore provably never-published, which makes ReclaimJob's re-dispatch
// of claimed jobs safe by construction. If the running CAS fails, Dispatch
// returns an error without publishing.
func (m *Manager) Dispatch(ctx context.Context, j *Job) error {
	j.Owner = m.masterID
	j.Status = StatusClaimed
	j.Updated = time.Now().UTC()

	// The deadline is enforced against this master's clock, so never trust
	// the client-computed value: a CLI host with a lagging clock would
	// otherwise finalize the job as timed out before any returns arrive.
	effective := j.Timeout
	if effective <= 0 {
		effective = defaultJobTimeout
	}
	j.Deadline = time.Now().UTC().Add(effective)

	// Claim job in KV via Create (fails if key already exists = idempotent).
	jobsBucket, err := bus.GetBucket(ctx, m.js, bus.BucketJobs)
	if err != nil {
		return fmt.Errorf("job: get jobs bucket: %w", err)
	}

	data, err := bus.Encode(j)
	if err != nil {
		return fmt.Errorf("job: encode job: %w", err)
	}

	rev, err := jobsBucket.Create(ctx, j.JID, data)
	if err != nil {
		// Create fails if key exists. Check if it's ours from a retry.
		existing, getErr := jobsBucket.Get(ctx, j.JID)
		if getErr != nil {
			return fmt.Errorf("job: claim job %s: %w", j.JID, err)
		}
		var existingJob Job
		if decErr := bus.Decode(existing.Value(), &existingJob); decErr != nil {
			return fmt.Errorf("job: decode existing job %s: %w", j.JID, decErr)
		}
		switch {
		case existingJob.Status == StatusClaimed && existingJob.Owner == m.masterID:
			// Our own earlier attempt persisted the claim but died (or
			// errored) before the running CAS. Claimed records are provably
			// unpublished, so resume from the running-CAS step — BEFORE any
			// intent comparison: deterministic-JID dispatches (the reactor's
			// rxn- keyspace) legitimately re-render with drifted args or
			// targets between attempts, and the unpublished claim makes
			// adopting the requested intent safe.
			m.logger.Info("resuming interrupted dispatch of claimed job", "jid", j.JID)
			rev = existing.Revision()
			// Re-ensure the active-index key (the crash may have hit the
			// window between the record Create and the index Create).
			writeActiveKey(ctx, jobsBucket, j.JID, m.masterID, false, m.logger)
		case existingJob.Status == StatusClaimed:
			// Another master's claim, provably never-published — and its
			// owner may never publish it (a live master whose Dispatch call
			// already failed leaves the claim wedged until the scanner
			// reclaims it). Not a success: return the typed retryable
			// sentinel so the caller retries instead of assuming dispatch.
			m.logger.Info("job claim pending on another master", "jid", j.JID, "owner", existingJob.Owner)
			return fmt.Errorf("job: jid %s claimed by %s but not yet published: %w", j.JID, existingJob.Owner, ErrJobClaimPending)
		default:
			// The job progressed past claimed: idempotent no-op when the
			// intent matches exactly, typed conflict when it differs.
			if ok, reason := sameDispatchIntent(&existingJob, j); !ok {
				return fmt.Errorf("job: jid %s conflict: existing job differs (%s): %w", j.JID, reason, ErrJIDConflict)
			}
			m.logger.Info("job already dispatched", "jid", j.JID, "owner", existingJob.Owner)
			return nil
		}
	} else {
		// Fresh claim: index it so the orphan scanner sees it without
		// enumerating the whole jobs bucket. Best-effort (see active.go).
		writeActiveKey(ctx, jobsBucket, j.JID, m.masterID, true, m.logger)
	}

	j.Epoch = rev

	// Publish dispatch event to JetStream.
	dispatchEvent := Event{
		JID:       j.JID,
		Type:      EventDispatched,
		Data:      j,
		Timestamp: time.Now().UTC(),
	}
	eventData, err := bus.Encode(dispatchEvent)
	if err != nil {
		return fmt.Errorf("job: encode dispatch event: %w", err)
	}
	if _, err := m.js.Publish(ctx, bus.JobDispatchSubject(j.JID), eventData); err != nil {
		return fmt.Errorf("job: publish dispatch event: %w", err)
	}

	// Persist the running status BEFORE publishing to peels: once an
	// ExecRequest is on the wire, KV must never claim the job was not
	// dispatched (that is exactly the window that caused double execution
	// on reclaim). Failure here means nothing was published — safe to
	// retry Dispatch (the resume path above) or let the scanner reclaim.
	j.Status = StatusRunning
	j.Updated = time.Now().UTC()
	runData, err := bus.Encode(j)
	if err != nil {
		return fmt.Errorf("job: encode running job: %w", err)
	}
	newRev, err := jobsBucket.Update(ctx, j.JID, runData, rev)
	if err != nil {
		return fmt.Errorf("job: update job %s to running: %w", j.JID, err)
	}
	j.Epoch = newRev

	// Publish ExecRequest to each target peel via CmdSubject.
	for _, target := range j.Targets {
		reqData, err := bus.Encode(execRequestForJob(j))
		if err != nil {
			m.logger.Error("failed to encode exec request", "jid", j.JID, "target", target, "error", err)
			continue
		}
		subject := bus.CmdSubject(target)
		if err := m.nc.Publish(subject, reqData); err != nil {
			m.logger.Error("failed to dispatch to target", "jid", j.JID, "target", target, "error", err)
		}
	}

	// Start watcher (with ack-reconciliation: this watcher follows an
	// actual publish, so silent targets get one bounded re-send).
	m.startWatcher(ctx, j, true)

	m.logger.Info("job dispatched",
		"jid", j.JID,
		"function", j.Function,
		"targets", len(j.Targets),
		"master", m.masterID,
	)
	return nil
}

// maxReclaims caps how many times a job may bounce between masters.
// A job whose ReclaimCount exceeds this is finalized as failed instead of
// being re-dispatched/re-watched, stopping ownership ping-pong under
// repeated master crashes.
const maxReclaims = 3

// ReclaimJob re-dispatches a job that was orphaned by a dead master.
// For "claimed" jobs (never dispatched), it re-dispatches to peels.
// For "running" jobs, it starts a recovery watcher to collect
// outstanding returns.
//
// Jobs reclaimed more than maxReclaims times are finalized as failed
// (with the reason recorded in Metadata) instead of being recovered.
// Recovered watchers honor the job's remaining deadline budget: an
// already-expired deadline finalizes immediately from collected returns.
func (m *Manager) ReclaimJob(ctx context.Context, j *Job) {
	// Defensive: dispatched jobs always carry a deadline; derive one for
	// a hand-crafted record so there is a single deadline-honoring path.
	normalizeDeadline(j)

	if j.ReclaimCount > maxReclaims {
		m.logger.Warn("reclaim: job exceeded reclaim limit, finalizing as failed",
			"jid", j.JID, "reclaim_count", j.ReclaimCount, "max", maxReclaims)
		m.failReclaimLimitExceeded(ctx, j)
		return
	}

	switch j.Status {
	case StatusClaimed:
		// Job was claimed but never dispatched to peels. If the
		// deadline already passed, don't re-dispatch work nobody will
		// wait for: the watcher finalizes immediately (timeout/partial
		// based on any collected returns).
		if !time.Now().Before(j.Deadline) {
			m.logger.Warn("reclaim: claimed job deadline expired, finalizing without re-dispatch",
				"jid", j.JID, "deadline", j.Deadline)
			m.startWatcher(ctx, j, false)
			return
		}

		// Persist the running status BEFORE re-publishing (same invariant
		// as Dispatch, finding 17): a claimed record must provably mean
		// "never published". If the CAS fails, another master reclaimed
		// concurrently — abort without publishing or watching.
		m.logger.Info("reclaim: re-dispatching claimed job", "jid", j.JID)
		jobsBucket, err := bus.GetBucket(ctx, m.js, bus.BucketJobs)
		if err != nil {
			m.logger.Error("reclaim: get jobs bucket", "jid", j.JID, "error", err)
			return
		}
		j.Status = StatusRunning
		j.Updated = time.Now().UTC()
		data, err := bus.Encode(j)
		if err != nil {
			m.logger.Error("reclaim: encode running job", "jid", j.JID, "error", err)
			return
		}
		newRev, err := jobsBucket.Update(ctx, j.JID, data, j.Epoch)
		if err != nil {
			m.logger.Warn("reclaim: CAS update claimed job to running failed (superseded), not re-dispatching",
				"jid", j.JID, "error", err)
			return
		}
		j.Epoch = newRev

		for _, target := range j.Targets {
			reqData, err := bus.Encode(execRequestForJob(j))
			if err != nil {
				m.logger.Error("reclaim: encode exec request", "jid", j.JID, "target", target, "error", err)
				continue
			}
			if err := m.nc.Publish(bus.CmdSubject(target), reqData); err != nil {
				m.logger.Error("reclaim: dispatch to target", "jid", j.JID, "target", target, "error", err)
			}
		}

		// This watcher follows an actual publish: enable the one-shot
		// silent-target re-send, same as Dispatch.
		m.startWatcher(ctx, j, true)

	case StatusRunning:
		// Job was dispatched; start a recovery watcher to collect
		// any remaining returns. The watcher honors the job's deadline:
		// if it already expired, it finalizes immediately from the
		// persisted/seeded returns instead of waiting a fresh timeout.
		m.logger.Info("reclaim: recovering running job", "jid", j.JID)

		// Load any returns already persisted by the previous master from
		// the per-peel keys ("{jid}.{peelID}") — the only format returns
		// are ever stored in.
		existing := m.recoverPerPeelReturns(ctx, j.JID)

		// Returns published during the ownerless failover window had no
		// subscriber, but they sit durably in the job-events stream
		// (zester.job.<jid>.return.*). Replay and merge them: KV-seeded
		// returns win on peel-ID collision, and replayed-only returns
		// are queued for per-peel persistence so the KV catches up.
		var replayed []Return
		if m.ReplayReturns != nil {
			rctx, rcancel := context.WithTimeout(ctx, replayReturnsTimeout)
			streamReturns, err := m.ReplayReturns(rctx, j.JID)
			rcancel()
			if err != nil {
				m.logger.Warn("reclaim: replay returns from job-events stream failed, continuing with KV-seeded returns",
					"jid", j.JID, "error", err)
			} else if len(streamReturns) > 0 {
				seen := make(map[string]bool, len(existing))
				for _, r := range existing {
					seen[r.PeelID] = true
				}
				for _, r := range streamReturns {
					if r.PeelID == "" || seen[r.PeelID] {
						continue
					}
					seen[r.PeelID] = true
					replayed = append(replayed, r)
				}
				if len(replayed) > 0 {
					m.logger.Info("reclaim: recovered returns from job-events stream",
						"jid", j.JID, "replayed", len(replayed), "kv_seeded", len(existing))
				}
			}
		}

		// Recovery watchers never re-publish the ExecRequest: their
		// peels may be mid-execution, and the peel epoch fence does not
		// reject same-epoch duplicates.
		w := NewWatcher(j, m.nc, m.js, m.logger)
		w.OnJobFinalized = m.OnJobFinalized
		w.SeedReturns(existing)
		w.SeedReturnsPersist(replayed)
		m.runWatcher(ctx, w)

		// Close the replay→subscribe gap: the pre-watch replay above
		// finished BEFORE the recovery watcher's return subscription
		// became active, so a return published in that window is in the
		// job-events stream but seen by neither. Once the watcher is
		// subscribed, replay the stream once more and merge anything new
		// (already-collected peels are skipped, so this is idempotent).
		if m.ReplayReturns != nil {
			go m.mergeGapReturns(ctx, j.JID, w)
		}

	default:
		m.logger.Warn("reclaim: unexpected job status", "jid", j.JID, "status", j.Status)
	}
}

// mergeGapReturns runs the second, post-subscribe stream replay for a
// reclaimed running job: it waits until the recovery watcher's live
// subscriptions are established, replays the job-events stream once more,
// and merges any returns the pre-watch replay missed (those published
// between the first replay's read-until-idle and the subscription becoming
// active). Runs in its own goroutine; exits without merging if the watcher
// finished before ever subscribing (expired deadline, subscribe error) or
// the reclaim context is canceled.
func (m *Manager) mergeGapReturns(ctx context.Context, jid string, w *Watcher) {
	select {
	case <-w.Subscribed():
	case <-w.Done():
		return
	case <-ctx.Done():
		return
	}

	rctx, rcancel := context.WithTimeout(ctx, replayReturnsTimeout)
	defer rcancel()
	streamReturns, err := m.ReplayReturns(rctx, jid)
	if err != nil {
		m.logger.Warn("reclaim: post-subscribe replay failed; a return published in the replay→subscribe gap may be missed",
			"jid", jid, "error", err)
		return
	}
	w.MergeReturns(streamReturns)
}

// startWatcher creates and runs a watcher for j. redispatch enables the
// one-shot ack-reconciliation re-send and must only be true for watchers
// that follow an actual ExecRequest publish.
func (m *Manager) startWatcher(ctx context.Context, j *Job, redispatch bool) {
	w := NewWatcher(j, m.nc, m.js, m.logger)
	w.OnJobFinalized = m.OnJobFinalized
	if redispatch {
		w.Redispatch = true
		w.AckWindow = m.AckWindow
	}
	m.runWatcher(ctx, w)
}

// runWatcher registers a prepared watcher and runs it in a goroutine,
// removing it from the active set when it finishes.
func (m *Manager) runWatcher(ctx context.Context, w *Watcher) {
	jid := w.job.JID
	m.mu.Lock()
	m.watchers[jid] = w
	m.mu.Unlock()

	go func() {
		w.Watch(ctx)
		m.mu.Lock()
		delete(m.watchers, jid)
		m.mu.Unlock()
	}()
}

// failReclaimLimitExceeded finalizes a job as failed because it bounced
// between masters more than maxReclaims times. The reason is recorded in
// the job's Metadata; the write is CAS-fenced with the job's epoch so a
// concurrent owner cannot be overwritten.
func (m *Manager) failReclaimLimitExceeded(ctx context.Context, j *Job) {
	jobsBucket, err := bus.GetBucket(ctx, m.js, bus.BucketJobs)
	if err != nil {
		m.logger.Error("reclaim: get jobs bucket for reclaim-limit finalize", "jid", j.JID, "error", err)
		return
	}

	j.Status = StatusFailed
	j.Updated = time.Now().UTC()
	if j.Metadata == nil {
		j.Metadata = make(map[string]string)
	}
	j.Metadata["failed_reason"] = fmt.Sprintf(
		"reclaim limit exceeded: job reclaimed %d times (max %d)", j.ReclaimCount, maxReclaims)

	data, err := bus.Encode(j)
	if err != nil {
		m.logger.Error("reclaim: encode reclaim-limit finalize", "jid", j.JID, "error", err)
		return
	}
	if j.Epoch > 0 {
		newRev, casErr := jobsBucket.Update(ctx, j.JID, data, j.Epoch)
		if casErr != nil {
			m.logger.Warn("reclaim: CAS finalize of reclaim-limited job failed (superseded)",
				"jid", j.JID, "epoch", j.Epoch, "error", casErr)
			return
		}
		j.Epoch = newRev
	} else {
		// Defensive: every claimed job carries an epoch; a zero value
		// (only producible by a hand-crafted KV record) falls back to an
		// unfenced put.
		if _, err := bus.KVPut(ctx, jobsBucket, j.JID, j); err != nil {
			m.logger.Error("reclaim: store reclaim-limit finalize", "jid", j.JID, "error", err)
			return
		}
	}

	// Terminal transition: drop the active-jobs index entry.
	clearActiveKey(ctx, jobsBucket, j.JID, m.logger)

	if m.OnJobFinalized != nil {
		m.OnJobFinalized(j.Function, string(StatusFailed), sinceCreated(j))
	}

	// Publish a completion event so listeners see the terminal state.
	evt := Event{
		JID:       j.JID,
		Type:      EventFailed,
		Data:      map[string]any{"status": string(StatusFailed), "reason": j.Metadata["failed_reason"]},
		Timestamp: time.Now().UTC(),
	}
	if evtData, err := bus.Encode(evt); err == nil {
		if _, err := m.js.Publish(ctx, bus.JobStatusSubject(j.JID), evtData); err != nil {
			m.logger.Warn("reclaim: publish reclaim-limit event", "jid", j.JID, "error", err)
		}
	}

	m.logger.Info("job finalized after exceeding reclaim limit",
		"jid", j.JID, "reclaim_count", j.ReclaimCount)
}

// recoverPerPeelReturns reads per-peel return keys ("{jid}.{peelID}") from KV
// to reconstruct watcher state during orphan recovery. The listing is
// prefix-scoped server-side (O(returns of this job), not O(entire bucket)).
func (m *Manager) recoverPerPeelReturns(ctx context.Context, jid string) []Return {
	returnsBucket, err := bus.GetBucket(ctx, m.js, bus.BucketJobReturns)
	if err != nil {
		return nil
	}

	returns, err := m.readPerPeelReturns(ctx, returnsBucket, jid)
	if err != nil {
		m.logger.Warn("recover per-peel returns", "jid", jid, "error", err)
		return nil
	}
	return returns
}

// readPerPeelReturns lists and decodes the per-peel return keys for a job.
// Keys whose value fails to decode are skipped with a warning.
func (m *Manager) readPerPeelReturns(ctx context.Context, returnsBucket bus.KV, jid string) ([]Return, error) {
	keys, err := bus.ListKeysWithPrefix(ctx, returnsBucket, jid)
	if err != nil {
		return nil, fmt.Errorf("job: list per-peel returns for %s: %w", jid, err)
	}

	returns := make([]Return, 0, len(keys))
	for _, key := range keys {
		var ret Return
		if err := bus.KVGet(ctx, returnsBucket, key, &ret); err != nil {
			m.logger.Warn("read per-peel return", "jid", jid, "key", key, "error", err)
			continue
		}
		returns = append(returns, ret)
	}
	return returns, nil
}

// GetJob retrieves a job from KV storage.
func (m *Manager) GetJob(ctx context.Context, jid string) (*Job, error) {
	jobsBucket, err := bus.GetBucket(ctx, m.js, bus.BucketJobs)
	if err != nil {
		return nil, fmt.Errorf("job: get jobs bucket: %w", err)
	}

	var j Job
	if err := bus.KVGet(ctx, jobsBucket, jid, &j); err != nil {
		return nil, fmt.Errorf("job: get job %s: %w", jid, err)
	}
	return &j, nil
}

// GetReturns retrieves all returns for a job from KV storage. Returns are
// stored exclusively under per-peel keys ("{jid}.{peelID}"), listed with a
// prefix-scoped filter (O(returns of this job), never a full-bucket scan).
// An empty prefix result means the job has no returns.
func (m *Manager) GetReturns(ctx context.Context, jid string) ([]Return, error) {
	returnsBucket, err := bus.GetBucket(ctx, m.js, bus.BucketJobReturns)
	if err != nil {
		return nil, fmt.Errorf("job: get returns bucket: %w", err)
	}

	return m.readPerPeelReturns(ctx, returnsBucket, jid)
}

// CancelLocal cancels a local watcher without republishing the cancel signal.
// Used by the master's NATS cancel subscription to avoid amplification loops
// (issue HA-R8: all masters subscribe to cancel, so re-publishing would loop).
func (m *Manager) CancelLocal(jid string) {
	m.mu.RLock()
	w, ok := m.watchers[jid]
	m.mu.RUnlock()

	if ok {
		w.Cancel()
	}
}

// Cancel sends a cancellation signal for a running job.
// It cancels any local watcher and publishes the cancel to NATS so
// peels and other masters receive it. Only call this from the CLI/API
// path, never from a NATS subscription handler (use CancelLocal instead).
func (m *Manager) Cancel(ctx context.Context, jid string) error {
	m.CancelLocal(jid)

	// Publish cancel event to NATS for peels and other masters.
	cancelEvent := Event{
		JID:       jid,
		Type:      EventCanceled,
		Data:      "cancelled by user",
		Timestamp: time.Now().UTC(),
	}
	data, err := bus.Encode(cancelEvent)
	if err != nil {
		return fmt.Errorf("job: encode cancel event: %w", err)
	}

	subject := bus.JobCancelSubject(jid)
	if err := m.nc.Publish(subject, data); err != nil {
		return fmt.Errorf("job: publish cancel: %w", err)
	}

	return nil
}

// ActiveJobs returns a list of currently watched (active) job IDs.
func (m *Manager) ActiveJobs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	jids := make([]string, 0, len(m.watchers))
	for jid := range m.watchers {
		jids = append(jids, jid)
	}
	return jids
}

// Shutdown detaches all active watchers and waits for them to finish
// before returning. Detached watchers persist any collected returns but
// do NOT finalize their jobs: in-flight jobs stay "running" (peels are
// still executing them) so the orphan scanner on a surviving master can
// reclaim them once this master's heartbeat expires. Operator
// cancellation (Cancel/CancelLocal) still finalizes as StatusCanceled.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	watchers := make([]*Watcher, 0, len(m.watchers))
	for _, w := range m.watchers {
		watchers = append(watchers, w)
	}
	m.mu.Unlock()

	// Signal all watchers to detach.
	for _, w := range watchers {
		w.Detach()
	}

	// Wait for each watcher's Watch goroutine to persist and exit.
	for _, w := range watchers {
		<-w.Done()
	}

	// Clear the watchers map after all are done.
	m.mu.Lock()
	m.watchers = make(map[string]*Watcher)
	m.mu.Unlock()
}

func sameDispatchIntent(existing, requested *Job) (bool, string) {
	if existing == nil || requested == nil {
		return false, "nil job"
	}
	if existing.JID != requested.JID {
		return false, "jid"
	}
	if existing.Function != requested.Function {
		return false, "function"
	}
	if existing.Timeout != requested.Timeout {
		return false, "timeout"
	}
	if existing.User != requested.User {
		return false, "user"
	}

	exTargets := slices.Clone(existing.Targets)
	reqTargets := slices.Clone(requested.Targets)
	sort.Strings(exTargets)
	sort.Strings(reqTargets)
	if !slices.Equal(exTargets, reqTargets) {
		return false, "targets"
	}

	exArgs, exErr := canonicalJSON(existing.Args)
	reqArgs, reqErr := canonicalJSON(requested.Args)
	if exErr != nil || reqErr != nil {
		// Fallback to reflect comparison if canonicalization fails.
		if !reflect.DeepEqual(existing.Args, requested.Args) {
			return false, "args"
		}
	} else if exArgs != reqArgs {
		return false, "args"
	}

	exMeta, exErr := canonicalJSON(existing.Metadata)
	reqMeta, reqErr := canonicalJSON(requested.Metadata)
	if exErr != nil || reqErr != nil {
		if !reflect.DeepEqual(existing.Metadata, requested.Metadata) {
			return false, "metadata"
		}
	} else if exMeta != reqMeta {
		return false, "metadata"
	}

	return true, ""
}

func canonicalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
