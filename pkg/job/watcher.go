package job

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

const (
	// returnPersistThreshold is the number of new returns before an
	// incremental persist to KV. Returns are stored under per-peel keys
	// ("{jid}.{peelID}"), so each persist is O(1) — every return is
	// persisted immediately, and crash recovery loses at most the
	// return that was in flight when the master died.
	returnPersistThreshold = 1

	// persistQueueSize is the capacity of the buffered channel between
	// the return-subscription callback and the persist writer goroutine.
	// When the queue is full the callback falls back to a synchronous
	// persist — a return is never dropped and the callback never blocks
	// on channel capacity.
	persistQueueSize = 256
)

// DefaultAckWindow is how long a dispatch watcher waits after Watch starts
// before reconciling acked/returned peels against the job's targets and
// re-publishing the ExecRequest once to targets that stayed silent
// (architecture review finding 21: job delivery is fire-and-forget core
// NATS, so a peel that was briefly disconnected at dispatch time would
// otherwise never receive the job and the job burns its full timeout).
const DefaultAckWindow = 5 * time.Second

// DefaultUnreachableGrace is how long a dispatch watcher waits AFTER the
// ack-window re-publish before finalizing suspected-offline targets that
// stayed completely silent (no ack, no return) as UNREACHABLE. Exec dispatch
// is fire-and-forget core NATS with exactly two sends (publish + one
// re-publish), so a target that is heartbeat-absent and still silent after
// this point provably cannot produce a return — waiting the job's remaining
// deadline for it buys nothing.
const DefaultUnreachableGrace = 5 * time.Second

// Watcher monitors a single job for acks, returns, and timeout.
// It subscribes to the job's NATS subjects and aggregates results
// until all targets have returned or the timeout expires.
type Watcher struct {
	job    *Job
	nc     bus.PubSub
	js     bus.JetStreamAPI
	logger *slog.Logger

	// OnJobFinalized, if set, is invoked once when this watcher writes the
	// job's terminal status (it is NOT invoked when a CAS finalize is
	// fenced out by a newer owner, nor on a shutdown detach). duration is
	// the elapsed time since the job was created (defensively zero if
	// Created was never set). Optional metric hook: nil means no-op. Set
	// before calling Watch.
	OnJobFinalized func(function, status string, duration time.Duration)

	// Redispatch enables the one-shot silent-target re-dispatch: after
	// AckWindow, targets that neither acked nor returned get the job's
	// ExecRequest re-published once. Only set this on watchers that
	// follow an actual publish (fresh dispatch / claimed-job reclaim);
	// recovery watchers for running jobs must NOT re-publish, because
	// their peels may be mid-execution. Set before calling Watch.
	//
	// Peels publish an Ack on bus.JobAckSubject the moment they accept a
	// dispatch (before executing), so a slow-but-healthy peel is "heard"
	// within the window and never re-sent. CAUTION: the peel epoch fence
	// only rejects epochs LOWER than the highest one seen for a JID —
	// same-epoch duplicate suppression rests entirely on the peel's
	// persisted JID dedup state, which can be lost (on-disk state wiped)
	// and whose semantics future releases must keep (additive-only
	// policy), so this must stay a single bounded re-send.
	Redispatch bool

	// AckWindow overrides DefaultAckWindow for the silent-target
	// re-dispatch. 0 means DefaultAckWindow; a negative value disables
	// reconciliation even when Redispatch is set. Set before calling
	// Watch (tests shrink it).
	AckWindow time.Duration

	// UnreachableGrace arms the unreachable fast path: at
	// ackWindow+UnreachableGrace, targets listed in the job's
	// OfflineAtDispatch presence hint that have produced NO ack and NO
	// return get a synthetic UNREACHABLE return (job.UnreachableError)
	// instead of holding the job open until its deadline. Any ack or
	// return received first removes a target from consideration — the
	// heartbeat hint never overrides delivery proof. 0 means
	// DefaultUnreachableGrace; negative disables. Only honored when
	// Redispatch is set with an enabled AckWindow (the fast path's
	// "provably never received it" claim rests on the bounded re-send
	// having happened). Set before calling Watch.
	UnreachableGrace time.Duration

	mu         sync.Mutex
	acks       map[string]Ack
	returns    map[string]Return
	newReturns []string // peelIDs received since last persist
	canceled   bool
	detached   bool

	// persistCh carries flush signals from the return-subscription
	// callback to the persist writer goroutine, so KV round-trips never
	// run inside the NATS callback (finding 9: a return burst from a
	// wide target would otherwise queue against the subscription's
	// pending limit while the handler blocks on JetStream).
	persistCh chan struct{}

	cancelFunc context.CancelFunc
	done       chan struct{}
	// subscribed closes once Watch has established its live ack+return
	// subscriptions. Early-exit paths (expired deadline, subscribe
	// error) never close it — callers must select on Done() as well.
	subscribed chan struct{}
}

// NewWatcher creates a watcher for the given job. A missing deadline is
// defensively derived (normalizeDeadline) so Watch has a single
// deadline-honoring path.
func NewWatcher(j *Job, nc bus.PubSub, js bus.JetStreamAPI, logger *slog.Logger) *Watcher {
	if logger == nil {
		logger = slog.Default()
	}
	normalizeDeadline(j)
	return &Watcher{
		job:        j,
		nc:         nc,
		js:         js,
		logger:     logger,
		acks:       make(map[string]Ack),
		returns:    make(map[string]Return),
		persistCh:  make(chan struct{}, persistQueueSize),
		done:       make(chan struct{}),
		subscribed: make(chan struct{}),
	}
}

// Watch starts monitoring the job. It blocks until the job completes,
// times out, or is cancelled. This method should be called in a goroutine.
func (w *Watcher) Watch(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.cancelFunc = cancel
	// Cancel/Detach may have been called before cancelFunc was stored
	// (e.g. Manager.Shutdown right after Dispatch); honor it now instead
	// of blocking until the job timeout.
	stopNow := w.canceled || w.detached
	w.mu.Unlock()
	if stopNow {
		cancel()
	}

	defer func() {
		cancel()
		close(w.done)
	}()

	// Honor the job's absolute deadline (Created + Timeout; always set —
	// NewWatcher derives a missing one). A watcher recovered after a
	// master failover must not restart the full timeout budget: if the
	// deadline already passed, finalize immediately from whatever
	// returns were collected/seeded.
	if !time.Now().Before(w.job.Deadline) {
		w.logger.Warn("job deadline already expired at watch start, finalizing immediately",
			"jid", w.job.JID, "deadline", w.job.Deadline)
		w.finish(context.Background())
		return
	}

	// Persist writer goroutine: all KV writes for incoming returns run
	// here, off the NATS subscription callback. Stopped (with a final
	// drain) before finish/finalize so they observe fully persisted
	// per-peel state. stopWriter is idempotent: it runs explicitly on
	// the normal path (before finish) and via defer on error paths.
	writerStop := make(chan struct{})
	writerDone := make(chan struct{})
	go w.persistWriter(writerStop, writerDone)
	stopWriter := func() {
		select {
		case <-writerStop:
			// Already stopped.
		default:
			close(writerStop)
		}
		<-writerDone
	}
	defer stopWriter()

	// Returns seeded from a non-KV source (job-events stream replay via
	// SeedReturnsPersist) are queued in newReturns; nudge the writer so
	// the per-peel KV keys catch up without waiting for finalize.
	w.mu.Lock()
	pendingSeeds := len(w.newReturns) > 0
	w.mu.Unlock()
	if pendingSeeds {
		select {
		case w.persistCh <- struct{}{}:
		default:
		}
	}

	// Subscribe to acks. The peel's identity comes from the subject token
	// (NATS-permission-enforced), never the payload: a mismatching payload
	// PeelID is a forgery attempt and the message is dropped.
	ackSubject := bus.JobSubject(w.job.JID) + "." + bus.SubjectJobAck + ".*"
	ackSub, err := w.nc.Subscribe(ackSubject, func(msg *bus.Msg) {
		peelID, ok := subjectPeelID(msg.Subject)
		if !ok {
			w.logger.Warn("ack on malformed subject, dropping", "jid", w.job.JID, "subject", msg.Subject)
			return
		}
		var ack Ack
		if err := bus.Decode(msg.Data, &ack); err != nil {
			w.logger.Error("decode ack", "jid", w.job.JID, "error", err)
			return
		}
		if ack.PeelID != peelID {
			w.logger.Warn("ack payload peel does not match subject token, dropping",
				"jid", w.job.JID, "subject_peel", peelID, "payload_peel", ack.PeelID)
			return
		}
		w.mu.Lock()
		w.acks[peelID] = ack
		w.mu.Unlock()

		w.logger.Debug("job ack received", "jid", w.job.JID, "peel", peelID)
	})
	if err != nil {
		w.logger.Error("subscribe acks", "jid", w.job.JID, "error", err)
		w.finalizeJob(ctx, StatusFailed)
		return
	}
	defer ackSub.Unsubscribe()

	// Subscribe to returns. The callback only records in-memory state and
	// enqueues a persist signal; the writer goroutine does the KV write.
	// Identity is the subject token, same as acks: a payload PeelID that
	// mismatches it would let one compromised peel overwrite another peel's
	// collected return, so the message is dropped instead.
	returnSubject := bus.JobSubject(w.job.JID) + "." + bus.SubjectJobReturn + ".*"
	returnSub, err := w.nc.Subscribe(returnSubject, func(msg *bus.Msg) {
		peelID, ok := subjectPeelID(msg.Subject)
		if !ok {
			w.logger.Warn("return on malformed subject, dropping", "jid", w.job.JID, "subject", msg.Subject)
			return
		}
		var ret Return
		if err := bus.Decode(msg.Data, &ret); err != nil {
			w.logger.Error("decode return", "jid", w.job.JID, "error", err)
			return
		}
		if ret.PeelID != peelID {
			w.logger.Warn("return payload peel does not match subject token, dropping",
				"jid", w.job.JID, "subject_peel", peelID, "payload_peel", ret.PeelID)
			return
		}
		// Drop any wire message flagged Unreachable. Only THIS master's
		// markUnreachable legitimately produces such a return, and it
		// records it in w.returns in-memory BEFORE publishing it for live
		// listeners — so the publish echoes back here as a no-op input. If
		// we stored it, that echo could overwrite a REAL return for the
		// same peel that raced in just before the synthetic was published,
		// finalizing a peel that actually succeeded as UNREACHABLE (the
		// ruling: a return always overrides the presence classification, so
		// the synthetic must never clobber a real one). A compromised peel
		// cannot forge this flag to any effect either — it would only
		// suppress its own return.
		if ret.Unreachable {
			return
		}
		w.mu.Lock()
		// A real return always wins over a synthetic UNREACHABLE already
		// recorded (delivery proof overrides the presence hint); a real
		// return never overwrites another real return (first wins, matching
		// the pre-existing single-return-per-peel contract).
		if existing, ok := w.returns[peelID]; ok && !existing.Unreachable {
			w.mu.Unlock()
			return
		}
		w.returns[peelID] = ret
		count := len(w.returns)
		w.newReturns = append(w.newReturns, peelID)
		shouldPersist := len(w.newReturns) >= returnPersistThreshold
		w.mu.Unlock()

		w.logger.Debug("job return received", "jid", w.job.JID, "peel", peelID, "success", ret.Success)

		// Incremental persist per HA-R4, handed to the writer goroutine.
		// A full queue falls back to a synchronous persist: never drop a
		// return, never block unboundedly on the channel.
		if shouldPersist {
			select {
			case w.persistCh <- struct{}{}:
			default:
				w.persistReturns(ctx)
			}
		}

		// Check if all targets have returned.
		if count >= w.job.TargetCount() {
			cancel()
		}
	})
	if err != nil {
		w.logger.Error("subscribe returns", "jid", w.job.JID, "error", err)
		w.finalizeJob(ctx, StatusFailed)
		return
	}
	defer returnSub.Unsubscribe()

	// Both live subscriptions are active: signal readiness so a reclaim's
	// post-subscribe stream replay (Manager.mergeGapReturns) knows the
	// replay→subscribe gap is closed from this point on.
	close(w.subscribed)

	// A recovered watcher whose seeded returns (KV + stream replay)
	// already cover every target has nothing to wait for: finalize
	// immediately instead of burning the remaining deadline.
	w.mu.Lock()
	seededComplete := w.job.TargetCount() > 0 && len(w.returns) >= w.job.TargetCount()
	w.mu.Unlock()
	if seededComplete {
		cancel()
	}

	// Wait for completion or timeout.
	timeout := w.job.Timeout
	if timeout == 0 {
		timeout = defaultJobTimeout
	}
	// The deadline caps the wait at the remaining time budget (relevant
	// for watchers recovered mid-flight after a failover).
	if remaining := time.Until(w.job.Deadline); remaining < timeout {
		timeout = remaining
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	// Ack-reconciliation window (finding 21): one-shot re-dispatch to
	// silent targets, only on watchers that follow an actual publish.
	var ackC <-chan time.Time
	var unreachC <-chan time.Time
	if w.Redispatch {
		window := w.AckWindow
		if window == 0 {
			window = DefaultAckWindow
		}
		if window > 0 {
			ackTimer := time.NewTimer(window)
			defer ackTimer.Stop()
			ackC = ackTimer.C

			// Unreachable fast path: fires one grace period after the
			// re-publish. Any target that is still completely silent
			// (no ack, no return) after both sends is almost certainly
			// unreachable — the ack fires on accept (sub-ms), so 10s
			// of total silence is strong proof of non-delivery.
			grace := w.UnreachableGrace
			if grace == 0 {
				grace = DefaultUnreachableGrace
			}
			if grace > 0 {
				unreachTimer := time.NewTimer(window + grace)
				defer unreachTimer.Stop()
				unreachC = unreachTimer.C
			}
		}
	}

wait:
	for {
		select {
		case <-ctx.Done():
			// All returns received or cancelled.
			break wait
		case <-timer.C:
			// Timeout expired.
			w.logger.Warn("job timeout", "jid", w.job.JID)
			break wait
		case <-ackC:
			ackC = nil // fire exactly once
			w.redispatchSilent()
		case <-unreachC:
			unreachC = nil // fire exactly once
			if w.markUnreachable(cancel) {
				break wait
			}
		}
	}

	// Stop the writer (final drain included) before finishing so the
	// per-peel keys are complete when the terminal status lands.
	stopWriter()

	w.finish(context.Background())
}

// persistWriter is the dedicated KV writer goroutine: it flushes queued
// returns whenever the callback signals, and performs a final drain on stop.
// Persists use a background context because the watch context is canceled
// the moment the last return arrives — that return still has to be written.
func (w *Watcher) persistWriter(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-w.persistCh:
			w.persistReturns(context.Background())
		case <-stop:
			// Final drain: one persist flushes everything still queued
			// (persistReturns drains the whole newReturns list), so any
			// signals left in persistCh are moot.
			w.persistReturns(context.Background())
			return
		}
	}
}

// redispatchSilent re-publishes the job's ExecRequest once to every target
// that has neither acked nor returned within the ack window (architecture
// review finding 21). Delivery is fire-and-forget core NATS, so a peel that
// was briefly disconnected at dispatch time would otherwise silently never
// receive the job and the operator could not tell delivery failure from
// execution failure.
//
// The re-publish reuses the job's current epoch: peels that never saw the
// request accept it, and superseded-owner requests are still fenced. Note
// that the peel fence does NOT reject same-epoch duplicates (see Redispatch
// field docs), so this must stay a single bounded re-send.
func (w *Watcher) redispatchSilent() {
	w.mu.Lock()
	if w.canceled || w.detached {
		w.mu.Unlock()
		return
	}
	heard := make(map[string]bool, len(w.acks)+len(w.returns))
	for peelID := range w.acks {
		heard[peelID] = true
	}
	for peelID := range w.returns {
		heard[peelID] = true
	}
	w.mu.Unlock()

	var silent []string
	for _, target := range w.job.Targets {
		if !heard[target] {
			silent = append(silent, target)
		}
	}
	if len(silent) == 0 {
		return
	}

	data, err := bus.Encode(execRequestForJob(w.job))
	if err != nil {
		w.logger.Error("redispatch: encode exec request", "jid", w.job.JID, "error", err)
		return
	}
	for _, target := range silent {
		if err := w.nc.Publish(bus.CmdSubject(target), data); err != nil {
			w.logger.Error("redispatch: publish to silent target", "jid", w.job.JID, "target", target, "error", err)
		}
	}
	w.logger.Warn("re-dispatched job to silent targets (no ack or return within ack window)",
		"jid", w.job.JID, "peels", silent, "targets", w.job.TargetCount())
}

// markUnreachable executes the unreachable fast path: every target that has
// produced NEITHER an ack NOR a return after both the initial publish and the
// ack-window re-publish gets a synthetic UNREACHABLE return — recorded exactly
// like a real per-peel return (KV persistence via the writer goroutine) and
// additionally published on the job's return subject so live listeners (the
// dispatching CLI) finish early. Acks are published on accept (sub-ms, before
// any execution), so 10s of total silence is strong proof of non-delivery —
// a healthy peel that received the job always acks well within this window.
// Reports whether the synthetic returns completed the target set (the caller
// then stops waiting).
func (w *Watcher) markUnreachable(cancel context.CancelFunc) bool {
	w.mu.Lock()
	if w.canceled || w.detached {
		w.mu.Unlock()
		return false
	}
	now := time.Now().UTC()
	var marked []Return
	for _, target := range w.job.Targets {
		if _, ok := w.acks[target]; ok {
			continue // delivery-proven: waits the full deadline
		}
		if _, ok := w.returns[target]; ok {
			continue // already answered
		}
		ret := Return{
			JID:         w.job.JID,
			PeelID:      target,
			Success:     false,
			Error:       UnreachableError,
			Unreachable: true,
			Timestamp:   now,
		}
		w.returns[target] = ret
		w.newReturns = append(w.newReturns, target)
		marked = append(marked, ret)
	}
	complete := len(w.returns) >= w.job.TargetCount()
	w.mu.Unlock()

	if len(marked) == 0 {
		return false
	}

	// Nudge the persist writer (finalize's flush is the backstop).
	select {
	case w.persistCh <- struct{}{}:
	default:
	}

	// Publish the synthetic returns for live listeners. Master credentials
	// may publish on any return subject; our own return subscription echoes
	// them back into w.returns idempotently (same peel key, same content).
	peels := make([]string, 0, len(marked))
	for _, ret := range marked {
		peels = append(peels, ret.PeelID)
		data, err := bus.Encode(ret)
		if err != nil {
			w.logger.Error("encode unreachable return", "jid", w.job.JID, "peel", ret.PeelID, "error", err)
			continue
		}
		if err := w.nc.Publish(bus.JobReturnSubject(w.job.JID, ret.PeelID), data); err != nil {
			w.logger.Warn("publish unreachable return", "jid", w.job.JID, "peel", ret.PeelID, "error", err)
		}
	}

	w.logger.Warn("targets finalized as unreachable (no heartbeat at dispatch, no ack after republish)",
		"jid", w.job.JID, "peels", peels, "targets", w.job.TargetCount())

	if complete && cancel != nil {
		cancel()
		return true
	}
	return false
}

// finish either persists collected returns without finalizing (shutdown
// detach with outstanding targets) or finalizes the job with the terminal
// status derived from the collected returns.
//
// Shutdown detach: persist collected returns but do NOT write a terminal
// status. The job stays "running" with its current epoch so the orphan
// scanner on a surviving master can reclaim it and collect the
// outstanding returns (recoverPerPeelReturns reads the per-peel keys).
// If all targets already returned, the job is genuinely done — finalize
// normally instead.
func (w *Watcher) finish(ctx context.Context) {
	w.mu.Lock()
	returnCount := len(w.returns)
	detached := w.detached
	w.mu.Unlock()

	targetCount := w.job.TargetCount()

	if detached && returnCount < targetCount {
		w.persistReturns(ctx)
		w.logger.Info("watcher detached without finalizing", "jid", w.job.JID,
			"returns", returnCount, "targets", targetCount)
		return
	}

	w.finalizeJob(ctx, w.terminalStatus())
}

// terminalStatus derives the final job status from the collected returns
// and the cancel flag. Caller must not hold w.mu.
func (w *Watcher) terminalStatus() Status {
	w.mu.Lock()
	defer w.mu.Unlock()

	returnCount := len(w.returns)
	failedCount := 0
	for _, ret := range w.returns {
		if !ret.Success || ret.Error != "" {
			failedCount++
		}
	}
	targetCount := w.job.TargetCount()

	switch {
	case w.canceled && returnCount < targetCount:
		return StatusCanceled
	case returnCount >= targetCount && failedCount > 0:
		return StatusFailed
	case returnCount >= targetCount:
		return StatusComplete
	case returnCount > 0:
		return StatusPartial
	default:
		return StatusTimeout
	}
}

// Cancel signals the watcher to stop monitoring. The job is finalized
// with a terminal status (StatusCanceled if returns are incomplete).
// Use this for operator cancellation; use Detach for manager shutdown.
func (w *Watcher) Cancel() {
	w.mu.Lock()
	w.canceled = true
	fn := w.cancelFunc
	w.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Detach signals the watcher to stop monitoring WITHOUT finalizing the job.
// Collected returns are persisted per-peel, but no terminal status is
// written: the job record keeps its current status and epoch, so another
// master's orphan scanner can reclaim it after this master's heartbeat
// expires. Used by Manager.Shutdown; peels keep executing the job.
func (w *Watcher) Detach() {
	w.mu.Lock()
	w.detached = true
	fn := w.cancelFunc
	w.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Done returns a channel that closes when the watcher finishes.
func (w *Watcher) Done() <-chan struct{} {
	return w.done
}

// Subscribed returns a channel that closes once Watch has established its
// live ack and return subscriptions. Watchers that exit before subscribing
// (already-expired deadline, subscribe error) never close it, so callers
// must select on Done() alongside it.
func (w *Watcher) Subscribed() <-chan struct{} {
	return w.subscribed
}

// Returns returns a copy of the collected returns.
func (w *Watcher) Returns() map[string]Return {
	w.mu.Lock()
	defer w.mu.Unlock()

	result := make(map[string]Return, len(w.returns))
	for k, v := range w.returns {
		result[k] = v
	}
	return result
}

// Acks returns a copy of the collected acks.
func (w *Watcher) Acks() map[string]Ack {
	w.mu.Lock()
	defer w.mu.Unlock()

	result := make(map[string]Ack, len(w.acks))
	for k, v := range w.acks {
		result[k] = v
	}
	return result
}

// SeedReturns pre-loads returns recovered from KV (orphan recovery). The
// returns are already persisted per-peel, so they are NOT queued for
// another KV write. Call before Watch.
func (w *Watcher) SeedReturns(returns []Return) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range returns {
		w.returns[r.PeelID] = r
	}
}

// supersedesReturn reports whether an incoming return should replace what is
// already recorded for its peel. A slot is filled either when empty or when
// the recorded return is a synthetic UNREACHABLE and the incoming one is a
// REAL return (delivery proof always overrides the presence classification —
// the ruled model). A real return never displaces another real return.
func supersedesReturn(existing Return, existed bool, incoming Return) bool {
	if !existed {
		return true
	}
	return existing.Unreachable && !incoming.Unreachable
}

// SeedReturnsPersist pre-loads returns recovered from a durable source that
// is NOT the job-returns KV (e.g. a job-events stream replay after reclaim)
// and queues them for per-peel persistence so the KV catches up. Returns for
// peels already holding a REAL return are skipped; a real return DOES replace
// a synthetic UNREACHABLE seeded from KV (a peel that actually returned during
// the ownerless failover window must not stay recorded unreachable). Call
// before Watch.
func (w *Watcher) SeedReturnsPersist(returns []Return) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range returns {
		existing, ok := w.returns[r.PeelID]
		if !supersedesReturn(existing, ok, r) {
			continue
		}
		w.returns[r.PeelID] = r
		w.newReturns = append(w.newReturns, r.PeelID)
	}
}

// MergeReturns records returns recovered from a durable source AFTER the
// watcher has started (the post-subscribe job-events stream replay that
// closes the reclaim replay→subscribe gap). A real live/seeded return wins,
// but a real return DOES replace a synthetic UNREACHABLE (delivery proof
// overrides the presence classification); new or superseding ones are queued
// for per-peel persistence and, when they complete the target set, finish the
// watch. Safe to call concurrently with Watch.
func (w *Watcher) MergeReturns(returns []Return) {
	w.mu.Lock()
	added := 0
	for _, r := range returns {
		if r.PeelID == "" {
			continue
		}
		existing, ok := w.returns[r.PeelID]
		if !supersedesReturn(existing, ok, r) {
			continue
		}
		newPeel := !ok
		w.returns[r.PeelID] = r
		w.newReturns = append(w.newReturns, r.PeelID)
		if newPeel {
			added++
		}
	}
	count := len(w.returns)
	cancel := w.cancelFunc
	w.mu.Unlock()

	if added == 0 {
		return
	}

	// Nudge the persist writer (finalize's flush is the backstop if the
	// writer already stopped).
	select {
	case w.persistCh <- struct{}{}:
	default:
	}

	if count >= w.job.TargetCount() && cancel != nil {
		cancel()
	}
}

// persistReturns writes new returns as per-peel KV keys for crash recovery.
// Each return is stored under "{jid}.{peelID}", making each write O(1).
// Safe for concurrent use (writer goroutine, callback fallback, finalize).
func (w *Watcher) persistReturns(ctx context.Context) {
	w.mu.Lock()
	toWrite := make(map[string]Return, len(w.newReturns))
	for _, peelID := range w.newReturns {
		if ret, ok := w.returns[peelID]; ok {
			toWrite[peelID] = ret
		}
	}
	w.newReturns = w.newReturns[:0]
	w.mu.Unlock()

	if len(toWrite) == 0 {
		return
	}

	// Entries whose write fails are re-queued so a later persist (or the
	// detach-path flush) retries them: with Detach semantics the per-peel
	// keys are the ONLY record handed to a reclaiming master, so dropping
	// a return here would make the job finalize as partial after failover
	// even though the peel succeeded.
	var failed []string
	returnsBucket, err := bus.GetBucket(ctx, w.js, bus.BucketJobReturns)
	if err != nil {
		w.logger.Warn("persist returns: get bucket", "jid", w.job.JID, "error", err)
		for peelID := range toWrite {
			failed = append(failed, peelID)
		}
	} else {
		for peelID, ret := range toWrite {
			key := w.job.JID + "." + peelID
			if _, err := bus.KVPut(ctx, returnsBucket, key, ret); err != nil {
				w.logger.Warn("persist return", "jid", w.job.JID, "peel", peelID, "error", err)
				failed = append(failed, peelID)
			}
		}
	}
	if len(failed) > 0 {
		w.mu.Lock()
		w.newReturns = append(failed, w.newReturns...)
		w.mu.Unlock()
	}
}

func (w *Watcher) finalizeJob(ctx context.Context, status Status) {
	// Flush any still-queued per-peel returns first: the per-peel keys
	// are the sole store for the job's results. No aggregated single-key
	// value is ever written (finding 9: it would bound a job's total
	// returns by the ~1MB NATS payload cap).
	w.persistReturns(ctx)

	w.mu.Lock()
	returnCount := len(w.returns)
	successCount := 0
	for _, ret := range w.returns {
		if ret.Success && ret.Error == "" {
			successCount++
		}
	}
	w.mu.Unlock()

	w.job.Status = status
	w.job.ReturnCount = returnCount
	w.job.SuccessCount = successCount
	w.job.Updated = time.Now().UTC()

	// Store final job state via CAS to prevent stale owners from
	// overwriting a more recent finalization during reclaim races.
	jobsBucket, err := bus.GetBucket(ctx, w.js, bus.BucketJobs)
	if err != nil {
		w.logger.Error("get jobs bucket for finalize", "jid", w.job.JID, "error", err)
		return
	}
	data, err := bus.Encode(w.job)
	if err != nil {
		w.logger.Error("encode final job state", "jid", w.job.JID, "error", err)
		return
	}
	terminalStored := true
	if w.job.Epoch > 0 {
		newRev, casErr := jobsBucket.Update(ctx, w.job.JID, data, w.job.Epoch)
		if casErr != nil {
			// Superseded by a newer owner: that owner (or the scanner's
			// self-healing) is responsible for the active-index key too.
			w.logger.Warn("CAS finalize failed (likely superseded by new owner)", "jid", w.job.JID, "epoch", w.job.Epoch, "error", casErr)
			return
		}
		w.job.Epoch = newRev
	} else {
		// Defensive: every dispatched job carries an epoch; a zero value
		// (only producible by a hand-crafted KV record) falls back to an
		// unfenced put.
		if _, err := bus.KVPut(ctx, jobsBucket, w.job.JID, w.job); err != nil {
			w.logger.Error("failed to store final job state", "jid", w.job.JID, "error", err)
			terminalStored = false
		}
	}

	if terminalStored {
		// Terminal transition: drop the active-jobs index entry so the
		// orphan scanner never touches this job again. (If the terminal
		// write failed the job is still live in KV, so the index entry
		// must stay.)
		clearActiveKey(ctx, jobsBucket, w.job.JID, w.logger)
	}

	if w.OnJobFinalized != nil {
		w.OnJobFinalized(w.job.Function, string(status), sinceCreated(w.job))
	}

	// Publish completion event.
	evt := Event{
		JID:       w.job.JID,
		Type:      EventCompleted,
		Data:      map[string]any{"status": string(status), "returns": returnCount},
		Timestamp: time.Now().UTC(),
	}
	if data, err := bus.Encode(evt); err == nil {
		if _, err := w.js.Publish(ctx, bus.JobStatusSubject(w.job.JID), data); err != nil {
			w.logger.Warn("failed to publish completion event", "jid", w.job.JID, "error", err)
		}
	}

	w.logger.Info("job finalized", "jid", w.job.JID, "status", status,
		"returns", returnCount, "success", successCount, "targets", w.job.TargetCount())
}

// subjectPeelID extracts the trailing peel-id token(s) from a job ack or
// return subject ("zester.job.<jid>.<ack|return>.<peel-id>"). The trailing
// token is enforced by the peel's NATS publish permissions, so the subject —
// never the payload — is the authoritative source of the sender's identity
// (the same technique as the stream replayer and the scheduled-result
// consumer). ok is false for a subject with missing or empty tokens.
func subjectPeelID(subject string) (string, bool) {
	parts := strings.SplitN(subject, ".", 5)
	if len(parts) != 5 || parts[4] == "" {
		return "", false
	}
	return parts[4], true
}

// sinceCreated returns the elapsed time since the job was created, or
// defensively zero when Created was never set (NewJob and the scheduled-
// result handler always set it).
func sinceCreated(j *Job) time.Duration {
	if j.Created.IsZero() {
		return 0
	}
	return time.Since(j.Created)
}
