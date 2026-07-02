package enroll

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/ksuid"

	"github.com/ptorbus/zester/pkg/bus"
)

const (
	// ChallengeTTL is how long a challenge nonce is valid.
	ChallengeTTL = 5 * time.Minute

	// ChallengeSize is the number of random bytes in a challenge nonce.
	ChallengeSize = 32
)

// ChallengeRecord is stored in the enroll-challenges KV bucket.
// The bucket's TTL auto-expires entries after ChallengeTTL.
type ChallengeRecord struct {
	ChallengeID string    `msgpack:"challenge_id"`
	Challenge   []byte    `msgpack:"challenge"`
	PeelID      string    `msgpack:"peel_id"`
	PublicKey   string    `msgpack:"public_key"`
	IssuedAt    time.Time `msgpack:"issued_at"`
	ExpiresAt   time.Time `msgpack:"expires_at"`
	Used        bool      `msgpack:"used"`
}

// ChallengeStore manages enrollment challenge nonces in a
// short-TTL NATS KV bucket.
type ChallengeStore struct {
	kv     bus.KV
	logger *slog.Logger
}

// NewChallengeStore creates a challenge store backed by the
// enroll-challenges KV bucket.
func NewChallengeStore(ctx context.Context, js bus.JetStreamAPI, logger *slog.Logger) (*ChallengeStore, error) {
	if logger == nil {
		logger = slog.Default()
	}

	kv, err := bus.GetBucket(ctx, js, bus.BucketEnrollChallenges)
	if err != nil {
		return nil, fmt.Errorf("enroll: get challenges bucket: %w", err)
	}

	return &ChallengeStore{
		kv:     kv,
		logger: logger,
	}, nil
}

// Issue creates a new challenge nonce bound to the given peel ID and
// public key. The challenge is stored in KV (auto-expires via bucket TTL)
// and the ChallengeRecord is returned.
func (cs *ChallengeStore) Issue(ctx context.Context, peelID, publicKey string) (*ChallengeRecord, error) {
	nonce := make([]byte, ChallengeSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("enroll: generate challenge nonce: %w", err)
	}

	now := time.Now().UTC()
	rec := &ChallengeRecord{
		ChallengeID: "chl-" + ksuid.New().String(),
		Challenge:   nonce,
		PeelID:      peelID,
		PublicKey:   publicKey,
		IssuedAt:    now,
		ExpiresAt:   now.Add(ChallengeTTL),
		Used:        false,
	}

	data, err := bus.Encode(rec)
	if err != nil {
		return nil, fmt.Errorf("enroll: encode challenge: %w", err)
	}

	// Use Create (not Put) to guarantee uniqueness -- prevents overwriting
	// an existing challenge if the KSUID were ever to collide.
	if _, err := cs.kv.Create(ctx, rec.ChallengeID, data); err != nil {
		return nil, fmt.Errorf("enroll: store challenge: %w", err)
	}

	cs.logger.Info("enrollment challenge issued",
		"challenge_id", rec.ChallengeID,
		"peel_id", peelID,
		"expires_at", rec.ExpiresAt,
	)
	return rec, nil
}

// Consume retrieves a challenge by ID, validates it hasn't been used
// or expired, and atomically marks it as consumed. Returns the
// challenge record if valid.
//
// Single-use is enforced via CAS (Compare-And-Swap): the challenge is
// marked Used=true with the original KV revision. If two concurrent
// callers race, only the first CAS succeeds; the second fails with a
// revision mismatch, preventing double-consumption.
func (cs *ChallengeStore) Consume(ctx context.Context, challengeID string) (*ChallengeRecord, error) {
	entry, err := cs.kv.Get(ctx, challengeID)
	if err != nil {
		return nil, fmt.Errorf("enroll: challenge not found: %w", err)
	}

	var rec ChallengeRecord
	if err := bus.Decode(entry.Value(), &rec); err != nil {
		return nil, fmt.Errorf("enroll: decode challenge: %w", err)
	}

	if rec.Used {
		return nil, fmt.Errorf("enroll: challenge already consumed")
	}

	if time.Now().After(rec.ExpiresAt) {
		return nil, fmt.Errorf("enroll: challenge expired")
	}

	// Atomically mark as used via CAS to prevent concurrent consumption.
	// Only the first caller with the correct revision wins.
	rec.Used = true
	data, err := bus.Encode(&rec)
	if err != nil {
		return nil, fmt.Errorf("enroll: encode consumed challenge: %w", err)
	}

	if _, err := cs.kv.Update(ctx, challengeID, data, entry.Revision()); err != nil {
		return nil, fmt.Errorf("enroll: challenge already consumed")
	}

	// Best-effort cleanup after successful CAS. The bucket TTL handles
	// cleanup if this delete fails.
	if err := cs.kv.Delete(ctx, challengeID); err != nil {
		cs.logger.Debug("enroll: challenge cleanup deferred to TTL",
			"challenge_id", challengeID)
	}

	return &rec, nil
}
