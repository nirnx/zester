package job

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/ptorbus/zester/pkg/bus"
)

// ScheduledResultConsumerName is the durable consumer shared by all masters
// for persisting peel scheduler results from the job-events stream.
const ScheduledResultConsumerName = "schedule-results"

// ErrInvalidScheduledResult marks a scheduled result that is permanently
// invalid and must be dropped rather than redelivered.
var ErrInvalidScheduledResult = errors.New("job: invalid scheduled result")

// ScheduledResult is published by a peel when a locally scheduled entry with
// return_job enabled completes. The master persists it as a synthetic job
// record plus return so it appears in "zester job list" like any other job.
//
// Peels publish to zester.job.<jid>.schedule.<peel-id>; the trailing peel-id
// token is enforced by the peel's NATS permissions, so the subject — not the
// payload — is the authoritative source of the reporting peel's identity.
type ScheduledResult struct {
	// JID is the peel-generated job identifier (KSUID).
	JID string `msgpack:"jid"`

	// Entry is the schedule entry name that produced this result.
	Entry string `msgpack:"entry"`

	// Module is the executed module (e.g., "cmd.run", "state.highstate").
	Module string `msgpack:"module"`

	// Args contains the module arguments.
	Args map[string]any `msgpack:"args"`

	// Success is true if the execution succeeded.
	Success bool `msgpack:"success"`

	// Error is set when the execution failed.
	Error string `msgpack:"error,omitempty"`

	// ReturnData contains the execution output.
	ReturnData any `msgpack:"return_data"`

	// Duration is how long the execution took.
	Duration time.Duration `msgpack:"duration"`

	// Timestamp is when the execution completed.
	Timestamp time.Time `msgpack:"timestamp"`
}

// PublishScheduledResult encodes and publishes a scheduled result on the
// peel-scoped schedule subject. Used by the peel's scheduler ReturnFn.
func PublishScheduledResult(ps bus.PubSub, peelID string, res ScheduledResult) error {
	data, err := bus.Encode(res)
	if err != nil {
		return fmt.Errorf("job: encode scheduled result: %w", err)
	}
	if err := ps.Publish(bus.JobScheduleSubject(res.JID, peelID), data); err != nil {
		return fmt.Errorf("job: publish scheduled result: %w", err)
	}
	return nil
}

// HandleScheduledResult persists a scheduled result on the master side:
// it creates the synthetic job record (idempotently, so redeliveries and
// multi-master races are safe) and stores the return under its per-peel
// key ("{jid}.{peelID}") in the job-returns bucket — the same format the
// dispatch watcher writes, so GetReturns and the CLI read it uniformly.
//
// peelID must come from the message subject: NATS permissions guarantee a
// peel can only publish with its own ID as the trailing token, so Targets
// and the return's PeelID are forced to that value regardless of payload.
func HandleScheduledResult(ctx context.Context, js bus.JetStreamAPI, peelID string, res ScheduledResult, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if res.JID == "" || strings.ContainsAny(res.JID, ". *>") {
		return fmt.Errorf("%w: invalid jid %q", ErrInvalidScheduledResult, res.JID)
	}
	if res.Module == "" {
		return fmt.Errorf("%w: module is required", ErrInvalidScheduledResult)
	}

	status := StatusComplete
	if !res.Success {
		status = StatusFailed
	}
	ts := res.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	successCount := 0
	if res.Success && res.Error == "" {
		successCount = 1
	}
	j := &Job{
		JID:          res.JID,
		Function:     res.Module,
		Args:         res.Args,
		Targets:      []string{peelID},
		Status:       status,
		Created:      ts,
		Updated:      ts,
		ReturnCount:  1,
		SuccessCount: successCount,
		Metadata: map[string]string{
			"source":   "schedule",
			"schedule": res.Entry,
		},
	}

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		return fmt.Errorf("job: scheduled result: get jobs bucket: %w", err)
	}
	jobData, err := bus.Encode(j)
	if err != nil {
		return fmt.Errorf("job: scheduled result: encode job: %w", err)
	}
	if _, err := jobsBucket.Create(ctx, j.JID, jobData); err != nil {
		// Already-exists means a redelivery or another master won the race.
		// The return writes below are idempotent by content, so continue.
		logger.Debug("scheduled job record already exists", "jid", j.JID, "error", err)
	}

	ret := Return{
		JID:        res.JID,
		PeelID:     peelID,
		Success:    res.Success,
		ReturnData: res.ReturnData,
		Error:      res.Error,
		Duration:   res.Duration,
		Timestamp:  ts,
	}

	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		return fmt.Errorf("job: scheduled result: get returns bucket: %w", err)
	}
	if _, err := bus.KVPut(ctx, returnsBucket, res.JID+"."+peelID, ret); err != nil {
		return fmt.Errorf("job: scheduled result: persist per-peel return: %w", err)
	}

	logger.Info("scheduled job persisted", "jid", res.JID, "peel", peelID, "entry", res.Entry, "success", res.Success)
	return nil
}

// StartScheduledResultConsumer creates (or joins) the durable consumer on the
// job-events stream that persists peel scheduler results. All masters share
// the same durable, so each result is processed once fleet-wide; processing
// is idempotent, making redeliveries after failures safe. Returns a stop
// function.
func StartScheduledResultConsumer(ctx context.Context, consumers bus.ConsumerAPI, js bus.JetStreamAPI, logger *slog.Logger) (func(), error) {
	if logger == nil {
		logger = slog.Default()
	}

	cons, err := consumers.CreateOrUpdateConsumer(ctx, bus.StreamJobEvents, jetstream.ConsumerConfig{
		Durable:       ScheduledResultConsumerName,
		Description:   "Persist peel scheduler results as synthetic jobs",
		FilterSubject: bus.JobScheduleWildcard(),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxDeliver:    5,
	})
	if err != nil {
		return nil, fmt.Errorf("job: create scheduled-result consumer: %w", err)
	}

	cctx, err := cons.Consume(func(msg jetstream.Msg) {
		// Subject: zester.job.<jid>.schedule.<peel-id>
		parts := strings.Split(msg.Subject(), ".")
		if len(parts) != 5 {
			logger.Warn("scheduled result: malformed subject, dropping", "subject", msg.Subject())
			_ = msg.Ack()
			return
		}
		subjectJID, peelID := parts[2], parts[4]

		var res ScheduledResult
		if err := bus.Decode(msg.Data(), &res); err != nil {
			logger.Warn("scheduled result: decode failed, dropping", "subject", msg.Subject(), "error", err)
			_ = msg.Ack()
			return
		}
		if res.JID != subjectJID {
			logger.Warn("scheduled result: payload JID does not match subject, dropping",
				"subject", msg.Subject(), "payload_jid", res.JID)
			_ = msg.Ack()
			return
		}

		hctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := HandleScheduledResult(hctx, js, peelID, res, logger); err != nil {
			if errors.Is(err, ErrInvalidScheduledResult) {
				logger.Warn("scheduled result: invalid, dropping", "jid", res.JID, "error", err)
				_ = msg.Ack()
				return
			}
			logger.Warn("scheduled result: persist failed, will redeliver", "jid", res.JID, "error", err)
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return nil, fmt.Errorf("job: consume scheduled results: %w", err)
	}

	logger.Info("scheduled-result consumer started", "durable", ScheduledResultConsumerName)
	return cctx.Stop, nil
}
