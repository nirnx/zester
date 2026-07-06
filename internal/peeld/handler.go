package peeld

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/job"
	"github.com/nirnx/zester/pkg/proto"
)

// Bounded exec queue (finding 32 / roadmap B15): mutating executions no
// longer run synchronously inside the NATS subscription callback. The
// callback classifies each request — read-only modules run inline without
// execMu; everything else is enqueued for the single worker goroutine that
// owns the execMu-serialized path. A full queue rejects immediately with
// execQueueFullMsg instead of silently piling work into the NATS client's
// pending buffer.
const (
	// execQueueSize bounds the number of pending (non-read-only) executions.
	execQueueSize = 64

	// execQueueFullMsg is the documented rejection error returned (as an
	// ExecResponse error / failed job return) when the queue is full.
	execQueueFullMsg = "peel busy: execution queue full"
)

// execTask is one queued execution.
type execTask struct {
	msg *bus.Msg
	req proto.ExecRequest
	// ctx is the execution context (runCtx, or a per-job cancellable child).
	ctx context.Context
	// cancel is non-nil for job dispatches; it owns the cancelFuncs registry
	// entry, which is released via releaseExecCancel after execution (or on
	// queue rejection / shutdown drain).
	cancel context.CancelFunc
}

// handleJobCancel is the single wildcard subscription callback for all job
// cancels (zester.job.*.cancel), dispatching to per-job cancel functions via
// map lookup. Cancel functions are registered at enqueue time, so a job that
// is still waiting in the exec queue is aborted too (its context is already
// cancelled when the worker picks it up).
func (a *Agent) handleJobCancel(msg *bus.Msg) {
	// Extract JID from subject: zester.job.<jid>.cancel
	parts := strings.SplitN(msg.Subject, ".", 4)
	if len(parts) < 4 {
		return
	}
	jid := parts[2]
	a.cancelMu.Lock()
	fn, ok := a.cancelFuncs[jid]
	a.cancelMu.Unlock()
	if ok {
		a.logger.Info("job cancel received, aborting execution", "jid", jid, "peel", a.peelID)
		fn()
	}
}

// handleExecRequest is the exec-subject subscription callback. Messages
// arrive via request/reply (CLI direct) or fire-and-forget (job manager).
//
// The callback itself stays cheap: decode, epoch/dedup fencing (before
// enqueue, so stale or duplicate dispatches never occupy a queue slot),
// read-only fast path, or enqueue for the worker.
func (a *Agent) handleExecRequest(msg *bus.Msg) {
	logger := a.logger
	peelID := a.peelID

	var req proto.ExecRequest
	if err := bus.Decode(msg.Data, &req); err != nil {
		logger.Error("decode exec request", "error", err)
		respondError(msg, a.ps, peelID, "", "decode request: "+err.Error())
		return
	}

	isJob := msg.Reply == "" && req.JID != ""

	// Fencing + dedup: reject stale dispatches from superseded masters and
	// re-deliveries of already-observed (jid, epoch) pairs. The tracker is
	// persisted and capped (see dedup.go). ObserveDurable flushes the
	// accepted record to disk synchronously BEFORE the job can execute, so
	// the fence holds even when the peel crashes mid-execution and is
	// restarted into the master's same-epoch ack-window redispatch (C0).
	if isJob && req.Epoch > 0 {
		if prev, rejected := a.dedup.ObserveDurable(req.JID, req.Epoch); rejected {
			if req.Epoch < prev {
				logger.Warn("rejected stale dispatch (epoch too old)", "jid", req.JID, "epoch", req.Epoch, "current", prev)
			} else {
				logger.Warn("rejected duplicate dispatch (jid already observed at this epoch)", "jid", req.JID, "epoch", req.Epoch)
			}
			return
		}
	}

	logger.Info("received exec request", "peel", peelID, "jid", req.JID, "module", req.Module, "job_dispatch", isJob)

	// Accepted job dispatch: acknowledge receipt so the master's ack-window
	// silent-target redispatch can distinguish "never received" from "still
	// executing" (C8). Published before enqueue/inline execution — the ack
	// means "received and accepted", not "completed".
	if isJob {
		a.publishJobAck(req.JID)
	}

	// For job-manager dispatches, create a cancellable context and register
	// it with the cancel registry so the wildcard cancel subscription can
	// abort this execution — including while it is still queued.
	execCtx := a.runCtx
	var execCancel context.CancelFunc
	if isJob {
		execCtx, execCancel = context.WithCancel(a.runCtx)
		a.cancelMu.Lock()
		a.cancelFuncs[req.JID] = execCancel
		a.cancelMu.Unlock()
	}

	// Read-only fast path: facts./settings./pillar. queries, test.ping,
	// grains.*, sys.list_functions run inline in the callback WITHOUT
	// execMu, so a long-running state apply never makes the peel appear
	// dead to liveness probes and data queries (finding 32).
	if a.readOnlyModule(req.Module) {
		a.dispatchExec(execCtx, msg, req, a.execReadOnly)
		a.releaseExecCancel(req.JID, execCancel)
		return
	}

	task := execTask{msg: msg, req: req, ctx: execCtx, cancel: execCancel}
	select {
	case a.execQueue <- task:
	default:
		a.releaseExecCancel(req.JID, execCancel)
		logger.Warn("execution queue full, rejecting request",
			"peel", peelID, "jid", req.JID, "module", req.Module, "queue_size", cap(a.execQueue))
		respondError(msg, a.ps, peelID, req.JID, execQueueFullMsg)
	}
}

// runExecWorker is the single consumer of the bounded exec queue. It runs the
// existing execMu-serialized execution path, exiting when ctx (runCtx) is
// done — at which point it drains still-queued tasks' cancel registrations
// without executing them (their contexts are dying with runCtx anyway).
func (a *Agent) runExecWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case t := <-a.execQueue:
					a.releaseExecCancel(t.req.JID, t.cancel)
				default:
					return
				}
			}
		case t := <-a.execQueue:
			a.dispatchExec(t.ctx, t.msg, t.req, a.execModule)
			a.releaseExecCancel(t.req.JID, t.cancel)
		}
	}
}

// dispatchExec runs one execution via run and delivers the result
// (request/reply response or job return), updating metrics and logging — the
// shared tail of both the read-only fast path and the queued worker path.
func (a *Agent) dispatchExec(execCtx context.Context, msg *bus.Msg, req proto.ExecRequest,
	run func(context.Context, proto.ExecRequest) (proto.ExecResponse, error)) {

	execStart := time.Now()
	resp, _ := run(execCtx, req)
	execDuration := time.Since(execStart)
	resp.JID = req.JID

	outcome := "success"
	if !resp.Success {
		outcome = "error"
	}
	a.metrics.PeelStateApplyTotal.WithLabelValues(req.Module, outcome).Inc()
	a.metrics.PeelStateApplyDuration.WithLabelValues(req.Module).Observe(execDuration.Seconds())

	encoded, err := bus.Encode(resp)
	if err != nil {
		a.logger.Error("encode exec response", "error", err)
		return
	}

	if msg.Reply != "" {
		if err := msg.Respond(encoded); err != nil {
			a.logger.Error("respond exec response", "error", err)
		}
	} else if req.JID != "" {
		publishJobReturn(a.ps, req.JID, a.peelID, resp.Success, resp, execDuration, resp.Error)
	}

	a.logger.Info("exec complete", "peel", a.peelID, "jid", req.JID, "module", req.Module, "success", resp.Success)
}

// releaseExecCancel removes a job's cancel registration and cancels its
// context, releasing resources. No-op for non-job requests (nil cancel).
func (a *Agent) releaseExecCancel(jid string, cancel context.CancelFunc) {
	if cancel == nil {
		return
	}
	a.cancelMu.Lock()
	delete(a.cancelFuncs, jid)
	a.cancelMu.Unlock()
	cancel()
}

// respondError sends an error back via request/reply or as a failed job return.
func respondError(msg *bus.Msg, ps bus.PubSub, peelID, jid, errMsg string) {
	if msg.Reply != "" {
		// Direct request/reply from CLI.
		resp := proto.ExecResponse{
			PeelID: peelID,
			JID:    jid,
			Error:  errMsg,
		}
		data, err := bus.Encode(resp)
		if err != nil {
			slog.Error("encode error response", "error", err)
			return
		}
		if err := msg.Respond(data); err != nil {
			slog.Error("respond error response", "error", err)
		}
	} else if jid != "" {
		// Fire-and-forget from job manager — publish failed return.
		publishJobReturn(ps, jid, peelID, false, nil, 0, errMsg)
	}
}

// publishJobAck publishes a job.Ack on the job's ack subject
// (zester.job.<jid>.ack.<peelID>), telling the dispatching master's watcher
// that this peel received and accepted the dispatch — so the ack-window
// redispatch does not re-send the request to a peel that is merely still
// executing. Failure is Debug-only: the ack is an optimization signal; the
// job return remains the source of truth.
func (a *Agent) publishJobAck(jid string) {
	ack := job.Ack{
		JID:       jid,
		PeelID:    a.peelID,
		Timestamp: time.Now().UTC(),
	}
	data, err := bus.Encode(ack)
	if err != nil {
		a.logger.Debug("encode job ack", "jid", jid, "error", err)
		return
	}
	if err := a.ps.Publish(bus.JobAckSubject(jid, a.peelID), data); err != nil {
		a.logger.Debug("publish job ack", "jid", jid, "error", err)
	}
}

// publishJobReturn publishes a job.Return to the job return subject.
func publishJobReturn(ps bus.PubSub, jid, peelID string, success bool, returnData any, dur time.Duration, errMsg string) {
	ret := job.Return{
		JID:        jid,
		PeelID:     peelID,
		Success:    success,
		ReturnData: returnData,
		Error:      errMsg,
		Duration:   dur,
		Timestamp:  time.Now().UTC(),
	}
	data, err := bus.Encode(ret)
	if err != nil {
		slog.Error("encode job return", "error", err)
		return
	}
	if err := ps.Publish(bus.JobReturnSubject(jid, peelID), data); err != nil {
		slog.Error("publish job return", "jid", jid, "peel", peelID, "error", err)
	}
}
