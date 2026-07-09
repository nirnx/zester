package masterd

import (
	"context"
	"os"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/fileserver"
)

// fileserverUpdateTimeout bounds one operator-triggered republish (three
// directory walks plus KV puts).
const fileserverUpdateTimeout = 30 * time.Second

// startFileserverService subscribes the fileserver-update admin subject
// (`zester fileserver update`). Deliberately a PLAIN subscription on every
// master, not a queue group: a queue group would deliver the request to an
// arbitrary master, but only the publisher-lease holder may publish — so
// every master receives the request, non-holders drop it silently, and the
// holder alone runs the (hash-gated, or forced) republish and replies. The
// CLI's request timeout therefore doubles as the "no lease holder running"
// signal.
func (d *Daemon) startFileserverService(ps bus.PubSub) (bus.Subscription, error) {
	updateSub, err := ps.Subscribe(bus.SubjectAdminFileserverUpdate, func(msg *bus.Msg) {
		var req fileserver.UpdateRequest
		if err := bus.Decode(msg.Data, &req); err != nil {
			d.logger.Warn("fileserver update: decode request", "error", err)
			return
		}
		if !d.publisherLeader() {
			d.logger.Debug("fileserver update: not the publisher-lease holder; staying silent")
			return
		}

		ctx, cancel := context.WithTimeout(d.runCtx, fileserverUpdateTimeout)
		defer cancel()
		d.logger.Info("fileserver update requested", "force", req.Force)
		// Re-check leadership immediately before publishing: the lease could
		// have moved between the check above and here, and a non-holder must
		// not publish (publishAllFiles serializes on pubAllMu, but only the
		// holder should ever run it).
		if !d.publisherLeader() {
			return
		}
		resp := fileserver.UpdateResponse{
			Master: d.masterID,
			Sets:   d.publishAllFiles(ctx, req.Force),
		}
		d.respondFileserver(msg, "update", resp)
	})
	if err != nil {
		return nil, err
	}

	statusSub, err := ps.Subscribe(bus.SubjectAdminFileserverStatus, func(msg *bus.Msg) {
		if !d.publisherLeader() {
			return
		}
		hostname, _ := os.Hostname()
		_, since := d.publisherRole()
		resp := fileserver.StatusResponse{
			Master:   d.masterID,
			Hostname: hostname,
		}
		if !since.IsZero() {
			resp.SinceUnix = since.Unix()
		}
		d.respondFileserver(msg, "status", resp)
	})
	if err != nil {
		_ = updateSub.Unsubscribe()
		return nil, err
	}

	return combinedSub{updateSub, statusSub}, nil
}

// respondFileserver encodes and sends one fileserver service reply.
func (d *Daemon) respondFileserver(msg *bus.Msg, what string, resp any) {
	data, err := bus.Encode(resp)
	if err != nil {
		d.logger.Warn("fileserver "+what+": encode response", "error", err)
		return
	}
	if err := msg.Respond(data); err != nil {
		d.logger.Warn("fileserver "+what+": respond", "error", err)
	}
}

// combinedSub unsubscribes a group of subscriptions as one.
type combinedSub []bus.Subscription

func (c combinedSub) Unsubscribe() error {
	var first error
	for _, s := range c {
		if err := s.Unsubscribe(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
