package masterd

import (
	"context"
	"fmt"
	"strings"

	"github.com/nats-io/nats.go"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/target"
	"github.com/nirnx/zester/pkg/update"
)

// startRolloutController initializes the rollout controller for fleet-wide
// updates (opening the update KV buckets) and subscribes to the CLI's
// rollout start/abort requests on the masters queue group. The returned
// cleanup unsubscribes abort first, then start — the pre-extraction LIFO
// defer order.
func (d *Daemon) startRolloutController(ctx context.Context) (func(), error) {
	manifestKV, err := bus.GetBucket(ctx, d.client.JetStream(), bus.BucketUpdateManifests)
	if err != nil {
		return nil, fmt.Errorf("open manifest bucket: %w", err)
	}
	statusKV, err := bus.GetBucket(ctx, d.client.JetStream(), bus.BucketUpdateStatus)
	if err != nil {
		return nil, fmt.Errorf("open update-status bucket: %w", err)
	}
	rolloutKV, err := bus.GetBucket(ctx, d.client.JetStream(), bus.BucketUpdateRollouts)
	if err != nil {
		return nil, fmt.Errorf("open rollout bucket: %w", err)
	}
	d.statusKV = statusKV

	d.rolloutCtrl = update.NewRolloutController(
		ctx,
		d.rolloutRequest,
		update.NewRolloutStore(rolloutKV),
		update.NewManifestStore(manifestKV),
		statusKV,
		d.logger,
	)

	// Subscribe to rollout start requests from CLI.
	rolloutStartSub, err := d.nc.QueueSubscribe(bus.SubjectUpdateRolloutStart, masterQueueGroup, d.handleRolloutStart)
	if err != nil {
		return nil, fmt.Errorf("subscribe rollout start: %w", err)
	}

	// Subscribe to rollout abort requests from CLI.
	rolloutAbortSub, err := d.nc.QueueSubscribe(bus.SubjectUpdateRolloutAbort, masterQueueGroup, d.handleRolloutAbort)
	if err != nil {
		_ = rolloutStartSub.Unsubscribe()
		return nil, fmt.Errorf("subscribe rollout abort: %w", err)
	}
	return func() {
		_ = rolloutAbortSub.Unsubscribe()
		_ = rolloutStartSub.Unsubscribe()
	}, nil
}

// rolloutRequest is the rollout controller's RequestFunc (formerly an inline
// closure): a NATS request/reply round-trip with MessagePack encoding
// against the raw connection.
func (d *Daemon) rolloutRequest(rctx context.Context, subject string, req *update.UpdateCommand) (*update.UpdateResponse, error) {
	data, err := bus.Encode(req)
	if err != nil {
		return nil, err
	}
	reply, err := d.nc.RequestWithContext(rctx, subject, data)
	if err != nil {
		return nil, err
	}
	var resp update.UpdateResponse
	if err := bus.Decode(reply.Data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// handleRolloutStart is the rollout-start callback (formerly an inline
// closure): it resolves target nodes from the status KV and starts the
// rollout. It uses d.runCtx, exactly as the closure captured run()'s ctx.
func (d *Daemon) handleRolloutStart(msg *nats.Msg) {
	var req update.RolloutStartRequest
	if err := bus.Decode(msg.Data, &req); err != nil {
		respondRolloutStart(msg, update.RolloutStartResponse{Error: "decode: " + err.Error()})
		return
	}

	// Resolve target nodes from status KV.
	nodeIDs, targetExpr, err := resolveRolloutTargets(d.runCtx, d.client.JetStream(), d.statusKV, req.Config.Component, req.Config.Target)
	if err != nil {
		respondRolloutStart(msg, update.RolloutStartResponse{Error: "resolve target: " + err.Error()})
		return
	}
	if len(nodeIDs) == 0 {
		respondRolloutStart(msg, update.RolloutStartResponse{
			Error: fmt.Sprintf("no nodes found matching target %q for component %q", targetExpr, req.Config.Component),
		})
		return
	}

	state, err := d.rolloutCtrl.StartRollout(d.runCtx, req.Config, nodeIDs)
	if err != nil {
		respondRolloutStart(msg, update.RolloutStartResponse{Error: err.Error()})
		return
	}

	resp := update.RolloutStartResponse{
		ID:      state.ID,
		Batches: len(state.Batches),
		Nodes:   len(nodeIDs),
	}
	if req.Config.DryRun {
		resp.State = state
	}
	respondRolloutStart(msg, resp)
}

// handleRolloutAbort is the rollout-abort callback (formerly an inline
// closure). It uses d.runCtx, exactly as the closure captured run()'s ctx.
func (d *Daemon) handleRolloutAbort(msg *nats.Msg) {
	var req update.RolloutAbortRequest
	if err := bus.Decode(msg.Data, &req); err != nil {
		respondRolloutAbort(msg, update.RolloutAbortResponse{Error: "decode: " + err.Error()})
		return
	}

	if err := d.rolloutCtrl.AbortRollout(d.runCtx, req.RolloutID); err != nil {
		respondRolloutAbort(msg, update.RolloutAbortResponse{Error: err.Error()})
		return
	}
	respondRolloutAbort(msg, update.RolloutAbortResponse{})
}

func respondRolloutStart(msg *nats.Msg, resp update.RolloutStartResponse) {
	data, err := bus.Encode(&resp)
	if err != nil {
		return
	}
	msg.Respond(data)
}

func respondRolloutAbort(msg *nats.Msg, resp update.RolloutAbortResponse) {
	data, err := bus.Encode(&resp)
	if err != nil {
		return
	}
	msg.Respond(data)
}

func resolveRolloutTargets(ctx context.Context, js bus.JetStreamAPI, statusKV bus.KV, component, expr string) ([]string, string, error) {
	statuses, err := update.ListNodeStatuses(ctx, statusKV, component)
	if err != nil {
		return nil, "", fmt.Errorf("list nodes: %w", err)
	}

	targetExpr := expr
	if strings.TrimSpace(targetExpr) == "" {
		targetExpr = "*"
	}

	targetType := target.DetectType(targetExpr)
	lister := newRolloutTargetLister(js, statuses)
	nodeIDs, err := target.Resolve(ctx, targetExpr, targetType, lister)
	if err != nil {
		return nil, targetExpr, err
	}
	return nodeIDs, targetExpr, nil
}
