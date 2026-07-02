package masterd

import (
	"context"
	"fmt"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/enroll"
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
	}{
		{bus.SubjectAdminEnrollApprove, "approve", func(ctx context.Context, req enroll.AdminRequest) (*enroll.Record, error) {
			return d.enrollStore.Approve(ctx, req.ID, req.Operator)
		}},
		{bus.SubjectAdminEnrollReject, "reject", func(ctx context.Context, req enroll.AdminRequest) (*enroll.Record, error) {
			return d.enrollStore.Reject(ctx, req.ID, req.Operator, req.Reason)
		}},
		{bus.SubjectAdminEnrollRevoke, "revoke", func(ctx context.Context, req enroll.AdminRequest) (*enroll.Record, error) {
			return d.enrollStore.Revoke(ctx, req.ID, req.Operator, req.Reason)
		}},
	}

	subs := make([]bus.Subscription, 0, len(routes))
	cleanup := func() {
		for _, s := range subs {
			_ = s.Unsubscribe()
		}
	}
	for _, r := range routes {
		action, op := r.action, r.op
		sub, err := ps.QueueSubscribe(r.subject, adminQueueGroup, func(msg *bus.Msg) {
			d.handleAdminRequest(action, op, msg)
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
func (d *Daemon) handleAdminRequest(action string, op adminOp, msg *bus.Msg) {
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
	d.respondAdmin(msg, enroll.AdminResponse{Record: rec})
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
