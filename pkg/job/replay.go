package job

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/ptorbus/zester/pkg/bus"
)

const (
	// replayReturnsTimeout caps how long a reclaim waits for the
	// job-events stream replay before continuing with KV-seeded returns.
	replayReturnsTimeout = 30 * time.Second

	// replayFetchBatch and replayFetchWait tune the read-until-idle
	// fetch loop: the replayer pulls batches until a fetch comes back
	// empty, which means the consumer caught up with the stream.
	replayFetchBatch = 64
	replayFetchWait  = 2 * time.Second

	// replayConsumerInactiveThreshold lets the server clean up the
	// ephemeral replay consumer shortly after the replay finishes.
	replayConsumerInactiveThreshold = 30 * time.Second
)

// NewStreamReturnReplayer returns a Manager.ReplayReturns implementation
// backed by the job-events JetStream stream (architecture review findings
// 18/30): peels publish returns on zester.job.<jid>.return.<peel-id>, which
// the stream captures durably even while no master watcher is subscribed
// (the ownerless failover window). The replayer creates an ephemeral pull
// consumer filtered to the job's return subjects with DeliverAll and reads
// until idle, returning at most one (the newest) Return per peel.
//
// NewManager wires this automatically when its JetStreamAPI also implements
// bus.ConsumerAPI (the production bus.JS adapter does). Tests should set a
// fake Manager.ReplayReturns function instead of faking stream consumers.
func NewStreamReturnReplayer(consumers bus.ConsumerAPI, logger *slog.Logger) func(ctx context.Context, jid string) ([]Return, error) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, jid string) ([]Return, error) {
		return replayReturnsFromStream(ctx, consumers, jid, logger)
	}
}

func replayReturnsFromStream(ctx context.Context, consumers bus.ConsumerAPI, jid string, logger *slog.Logger) ([]Return, error) {
	if jid == "" || strings.ContainsAny(jid, ". *>") {
		return nil, fmt.Errorf("job: replay returns: invalid jid %q", jid)
	}

	// Ephemeral pull consumer (no Durable name): DeliverAll replays every
	// retained return message for this jid; the server removes the
	// consumer after the inactive threshold. The trailing ">" wildcard
	// covers peel IDs that contain dots (multiple subject tokens).
	filter := bus.JobSubject(jid) + "." + bus.SubjectJobReturn + "." + bus.WildcardMany
	cons, err := consumers.CreateOrUpdateConsumer(ctx, bus.StreamJobEvents, jetstream.ConsumerConfig{
		Description:       "Replay job returns during orphan reclaim",
		FilterSubject:     filter,
		DeliverPolicy:     jetstream.DeliverAllPolicy,
		AckPolicy:         jetstream.AckExplicitPolicy,
		InactiveThreshold: replayConsumerInactiveThreshold,
	})
	if err != nil {
		return nil, fmt.Errorf("job: replay returns: create consumer: %w", err)
	}

	byPeel := make(map[string]Return)
	var order []string // first-seen order, for deterministic output
	for ctx.Err() == nil {
		batch, err := cons.Fetch(replayFetchBatch, jetstream.FetchMaxWait(replayFetchWait))
		if err != nil {
			return nil, fmt.Errorf("job: replay returns: fetch: %w", err)
		}

		received := 0
		for msg := range batch.Messages() {
			received++
			_ = msg.Ack()

			// Subject: zester.job.<jid>.return.<peel-id...>. The peel-id
			// token(s) sit on a NATS-permission-scoped subject, so the
			// subject — not the payload — is authoritative for identity.
			parts := strings.SplitN(msg.Subject(), ".", 5)
			if len(parts) != 5 || parts[4] == "" {
				continue
			}
			peelID := parts[4]

			var ret Return
			if err := bus.Decode(msg.Data(), &ret); err != nil {
				logger.Warn("replay returns: undecodable message, skipping",
					"jid", jid, "subject", msg.Subject(), "error", err)
				continue
			}
			if ret.PeelID != peelID {
				logger.Warn("replay returns: payload peel differs from subject, using subject",
					"jid", jid, "payload_peel", ret.PeelID, "subject_peel", peelID)
				ret.PeelID = peelID
			}

			if _, seen := byPeel[peelID]; !seen {
				order = append(order, peelID)
			}
			byPeel[peelID] = ret // stream order: the newest return wins
		}
		if err := batch.Error(); err != nil {
			return nil, fmt.Errorf("job: replay returns: fetch batch: %w", err)
		}
		if received == 0 {
			// Idle: the consumer caught up with the stream.
			break
		}
	}

	returns := make([]Return, 0, len(byPeel))
	for _, peelID := range order {
		returns = append(returns, byPeel[peelID])
	}
	return returns, nil
}
