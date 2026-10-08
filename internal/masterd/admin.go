package masterd

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/enroll"
)

// adminQueueGroup is the NATS queue group the masters join for enrollment
// admin request/reply: each admin request is handled by exactly one master.
const adminQueueGroup = "zester-masters-admin"

// adminOp performs one enrollment admin state transition.
type adminOp func(ctx context.Context, req enroll.AdminRequest) (*enroll.Record, error)

// startAdminService subscribes the enrollment admin subjects (approve /
// reject / revoke, see bus.SubjectAdminEnroll*) on the masters admin queue
// group and answers enroll.AdminRequests against d.enrollStore (roadmap C6:
// admin ops ride authenticated request/reply instead of requiring direct KV
// access from the operator's credentials). The returned cleanup
// unsubscribes all three subjects.
func (d *Daemon) startAdminService(ps bus.RequestPubSub) (func(), error) {
	routes := []struct {
		subject string
		action  string
		op      adminOp
		// after runs once the transition is persisted and returns an
		// operator-facing warning ("" = none) relayed in the response.
		after func(ctx context.Context, rec *enroll.Record) string
	}{
		{bus.SubjectAdminEnrollApprove, "approve", func(ctx context.Context, req enroll.AdminRequest) (*enroll.Record, error) {
			return d.enrollStore.ApproveForce(ctx, req.ID, req.Operator, req.Force)
		}, nil},
		{bus.SubjectAdminEnrollReject, "reject", func(ctx context.Context, req enroll.AdminRequest) (*enroll.Record, error) {
			return d.enrollStore.Reject(ctx, req.ID, req.Operator, req.Reason)
		}, nil},
		{bus.SubjectAdminEnrollRevoke, "revoke", func(ctx context.Context, req enroll.AdminRequest) (*enroll.Record, error) {
			return d.enrollStore.Revoke(ctx, req.ID, req.Operator, req.Reason)
		}, d.afterRevoke},
	}

	subs := make([]bus.Subscription, 0, len(routes))
	cleanup := func() {
		for _, s := range subs {
			_ = s.Unsubscribe()
		}
	}
	for _, r := range routes {
		action, op, after := r.action, r.op, r.after
		sub, err := ps.QueueSubscribe(r.subject, adminQueueGroup, func(msg *bus.Msg) {
			d.handleAdminRequest(action, op, after, msg)
		})
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("subscribe enrollment admin %s: %w", r.action, err)
		}
		subs = append(subs, sub)
	}
	d.logger.Info("enrollment admin service started", "queue", adminQueueGroup)
	return cleanup, nil
}

// handleAdminRequest decodes an enroll.AdminRequest, applies the operation
// with the request's operator/reason, and replies with an
// enroll.AdminResponse (error string on failure). Every admin action is
// Info-logged with id + operator + action for the audit trail.
func (d *Daemon) handleAdminRequest(action string, op adminOp, after func(context.Context, *enroll.Record) string, msg *bus.Msg) {
	var req enroll.AdminRequest
	if err := bus.Decode(msg.Data, &req); err != nil {
		d.logger.Warn("enrollment admin request decode failed", "action", action, "error", err)
		d.respondAdmin(msg, enroll.AdminResponse{Err: "decode: " + err.Error()})
		return
	}
	if err := req.Validate(); err != nil {
		d.logger.Warn("enrollment admin request invalid", "action", action, "id", req.ID, "operator", req.Operator, "error", err)
		d.respondAdmin(msg, enroll.AdminResponse{Err: err.Error()})
		return
	}

	rec, err := op(d.runCtx, req)
	if err != nil {
		d.logger.Warn("enrollment admin operation failed",
			"action", action, "id", req.ID, "operator", req.Operator, "error", err)
		d.respondAdmin(msg, enroll.AdminResponse{Err: err.Error()})
		return
	}

	d.logger.Info("enrollment admin operation",
		"action", action, "id", req.ID, "operator", req.Operator, "peel_id", rec.PeelID)
	resp := enroll.AdminResponse{Record: rec}
	if after != nil {
		resp.Warning = after(d.runCtx, rec)
	}
	d.respondAdmin(msg, resp)
}

// afterRevoke is the revoke post-hook: soft-revoke + purge the peel's KV
// footprint + push the account JWT revocation list (revocation.go). The
// syncer is always wired in a running master (startup fails without it);
// nil only occurs in unit tests that exercise the admin service alone.
func (d *Daemon) afterRevoke(ctx context.Context, rec *enroll.Record) string {
	if d.revocation == nil {
		return "credential revocation sync is not running on this master"
	}
	return d.revocation.onRevoked(ctx, rec)
}

func (d *Daemon) respondAdmin(msg *bus.Msg, resp enroll.AdminResponse) {
	data, err := bus.Encode(&resp)
	if err != nil {
		d.logger.Warn("enrollment admin response encode failed", "error", err)
		return
	}
	if err := msg.Respond(data); err != nil {
		d.logger.Warn("enrollment admin response send failed", "error", err)
	}
}
