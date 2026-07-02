package enroll

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
)

// peelIndexPrefix is prepended to peel IDs for the peel-to-enrollment
// index stored in the same KV bucket. This allows O(1) lookup of
// enrollment records by peel ID.
const peelIndexPrefix = "peel."

// Store persists enrollment records in a NATS JetStream KV bucket.
// All mutations use CAS (Compare-And-Swap) to prevent race conditions
// between concurrent admin operations across multiple masters.
type Store struct {
	kv     bus.KV
	logger *slog.Logger
}

// StoreConfig configures the enrollment store.
type StoreConfig struct {
	JS     bus.JetStreamAPI
	Logger *slog.Logger
}

// NewStore creates an enrollment store backed by the enrollments KV bucket.
func NewStore(ctx context.Context, cfg StoreConfig) (*Store, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	kv, err := bus.GetBucket(ctx, cfg.JS, bus.BucketEnrollments)
	if err != nil {
		return nil, fmt.Errorf("enroll: get enrollments bucket: %w", err)
	}

	return &Store{
		kv:     kv,
		logger: cfg.Logger,
	}, nil
}

// Create persists a new enrollment record and its peel-to-ID index.
// The peel index is written first using KV Create (not Put) to serve
// as an atomic uniqueness guard — if a peel already has an enrollment
// record, the Create fails with a key-exists error. The enrollment
// record is then written. If the record write fails, the index is
// cleaned up to avoid a dangling reference.
func (s *Store) Create(ctx context.Context, rec *Record) error {
	data, err := bus.Encode(rec)
	if err != nil {
		return fmt.Errorf("enroll: encode record: %w", err)
	}

	// Step 1: Claim the peel index atomically. Create fails if the key
	// already exists, preventing duplicate enrollments for the same peel.
	indexKey := peelIndexPrefix + rec.PeelID
	if _, err := s.kv.Create(ctx, indexKey, []byte(rec.ID)); err != nil {
		return fmt.Errorf("enroll: peel %s already has an enrollment: %w", rec.PeelID, err)
	}

	// Step 2: Create the enrollment record.
	rev, err := s.kv.Create(ctx, rec.ID, data)
	if err != nil {
		// Roll back the index claim on failure.
		if delErr := s.kv.Delete(ctx, indexKey); delErr != nil {
			s.logger.Warn("enroll: failed to roll back peel index after record creation failure",
				"peel_id", rec.PeelID, "error", delErr)
		}
		return fmt.Errorf("enroll: create record %s: %w", rec.ID, err)
	}
	rec.Revision = rev

	s.logger.Info("enrollment created",
		"id", rec.ID,
		"peel_id", rec.PeelID,
		"public_key", truncateKey(rec.PublicKey),
	)
	return nil
}

// Get retrieves an enrollment record by ID.
func (s *Store) Get(ctx context.Context, id string) (*Record, error) {
	entry, err := s.kv.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("enroll: get record %s: %w", id, err)
	}

	var rec Record
	if err := bus.Decode(entry.Value(), &rec); err != nil {
		return nil, fmt.Errorf("enroll: decode record %s: %w", id, err)
	}
	rec.Revision = entry.Revision()
	return &rec, nil
}

// Update persists a modified enrollment record using CAS to prevent
// lost updates. The record's Revision field must match the current
// KV revision; if it doesn't, the update fails.
func (s *Store) Update(ctx context.Context, rec *Record) error {
	rec.UpdatedAt = time.Now().UTC()

	data, err := bus.Encode(rec)
	if err != nil {
		return fmt.Errorf("enroll: encode record: %w", err)
	}

	rev, err := s.kv.Update(ctx, rec.ID, data, rec.Revision)
	if err != nil {
		return fmt.Errorf("enroll: update record %s (rev %d): %w", rec.ID, rec.Revision, err)
	}

	rec.Revision = rev
	return nil
}

// Delete removes an enrollment record and its peel index.
func (s *Store) Delete(ctx context.Context, id string) error {
	// Attempt to read the record first to clean up the peel index.
	rec, err := s.Get(ctx, id)
	if err == nil && rec.PeelID != "" {
		indexKey := peelIndexPrefix + rec.PeelID
		_ = s.kv.Delete(ctx, indexKey)
	}

	if err := s.kv.Delete(ctx, id); err != nil {
		return fmt.Errorf("enroll: delete record %s: %w", id, err)
	}
	return nil
}

// List returns all enrollment records, optionally filtered by state.
// If filterState is nil, all records are returned.
func (s *Store) List(ctx context.Context, filterState *State) ([]*Record, error) {
	lister, err := s.kv.ListKeys(ctx)
	if err != nil {
		if errors.Is(err, bus.ErrNoKeysFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("enroll: list keys: %w", err)
	}

	var records []*Record
	for key := range lister.Keys() {
		// Skip peel index entries.
		if strings.HasPrefix(key, peelIndexPrefix) {
			continue
		}
		rec, err := s.Get(ctx, key)
		if err != nil {
			s.logger.Warn("enroll: skip unreadable record", "key", key, "error", err)
			continue
		}
		if filterState != nil && rec.State != *filterState {
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}

// FindByPeelID looks up an enrollment record by peel ID using the
// peel-to-enrollment index for O(1) lookup.
func (s *Store) FindByPeelID(ctx context.Context, peelID string) (*Record, error) {
	indexKey := peelIndexPrefix + peelID
	entry, err := s.kv.Get(ctx, indexKey)
	if err != nil {
		// No index entry means no enrollment for this peel.
		return nil, nil
	}

	enrollID := string(entry.Value())
	rec, err := s.Get(ctx, enrollID)
	if err != nil {
		// Index exists but record is gone (deleted or corrupted).
		// Treat as not found rather than propagating the storage error.
		s.logger.Warn("enroll: stale peel index",
			"peel_id", peelID, "enroll_id", enrollID, "error", err)
		return nil, nil
	}
	return rec, nil
}

// Approve transitions an enrollment from Pending to Approved.
func (s *Store) Approve(ctx context.Context, id, approvedBy string) (*Record, error) {
	rec, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if !rec.CanTransitionTo(StateApproved) {
		return nil, fmt.Errorf("enroll: cannot approve record %s in state %s", id, rec.State)
	}

	now := time.Now().UTC()
	rec.State = StateApproved
	rec.DecidedAt = &now
	rec.DecidedBy = approvedBy

	if err := s.Update(ctx, rec); err != nil {
		return nil, err
	}

	s.logger.Info("enrollment approved",
		"id", id,
		"peel_id", rec.PeelID,
		"decided_by", approvedBy,
	)
	return rec, nil
}

// Reject transitions an enrollment from Pending to Rejected.
func (s *Store) Reject(ctx context.Context, id, rejectedBy, reason string) (*Record, error) {
	rec, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if !rec.CanTransitionTo(StateRejected) {
		return nil, fmt.Errorf("enroll: cannot reject record %s in state %s", id, rec.State)
	}

	now := time.Now().UTC()
	rec.State = StateRejected
	rec.DecidedAt = &now
	rec.DecidedBy = rejectedBy
	rec.RejectReason = reason

	if err := s.Update(ctx, rec); err != nil {
		return nil, err
	}

	s.logger.Info("enrollment rejected",
		"id", id,
		"peel_id", rec.PeelID,
		"decided_by", rejectedBy,
		"reason", reason,
	)
	return rec, nil
}

// MarkIssued transitions an enrollment from Approved to Issued after
// the peel has successfully fetched its credentials.
func (s *Store) MarkIssued(ctx context.Context, id string, expiresAt time.Time) (*Record, error) {
	rec, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if !rec.CanTransitionTo(StateIssued) {
		return nil, fmt.Errorf("enroll: cannot mark record %s as issued in state %s", id, rec.State)
	}

	now := time.Now().UTC()
	rec.State = StateIssued
	rec.IssuedAt = &now
	rec.ExpiresAt = &expiresAt

	if err := s.Update(ctx, rec); err != nil {
		return nil, err
	}

	s.logger.Info("enrollment credentials issued",
		"id", id,
		"peel_id", rec.PeelID,
		"expires_at", expiresAt,
	)
	return rec, nil
}

// MarkActive transitions an enrollment from Issued to Active when the
// master detects the peel has connected to NATS and published facts.
func (s *Store) MarkActive(ctx context.Context, id string) (*Record, error) {
	rec, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if !rec.CanTransitionTo(StateActive) {
		return nil, fmt.Errorf("enroll: cannot mark record %s as active in state %s", id, rec.State)
	}

	rec.State = StateActive

	if err := s.Update(ctx, rec); err != nil {
		return nil, err
	}

	s.logger.Info("enrollment active",
		"id", id,
		"peel_id", rec.PeelID,
	)
	return rec, nil
}

// Revoke transitions an enrollment from Issued or Active to Revoked.
func (s *Store) Revoke(ctx context.Context, id, revokedBy, reason string) (*Record, error) {
	rec, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if !rec.CanTransitionTo(StateRevoked) {
		return nil, fmt.Errorf("enroll: cannot revoke record %s in state %s", id, rec.State)
	}

	now := time.Now().UTC()
	rec.State = StateRevoked
	rec.DecidedAt = &now
	rec.DecidedBy = revokedBy
	rec.RejectReason = reason

	if err := s.Update(ctx, rec); err != nil {
		return nil, err
	}

	s.logger.Info("enrollment revoked",
		"id", id,
		"peel_id", rec.PeelID,
		"decided_by", revokedBy,
		"reason", reason,
	)
	return rec, nil
}

// ReleaseIndex removes the peel-to-enrollment index entry for a peel ID.
// This is used when re-enrollment is allowed (after rejection or revocation)
// to free the index so a new enrollment record can claim it atomically.
func (s *Store) ReleaseIndex(ctx context.Context, peelID string) error {
	indexKey := peelIndexPrefix + peelID
	if err := s.kv.Delete(ctx, indexKey); err != nil {
		return fmt.Errorf("enroll: release peel index for %s: %w", peelID, err)
	}
	return nil
}

// WatchRecord starts a KV watcher for a specific enrollment record.
// The caller is responsible for calling watcher.Stop().
// The watcher first replays the current value (if it exists), then a nil
// sentinel, then live updates on every state change.
func (s *Store) WatchRecord(ctx context.Context, id string) (bus.KeyWatcher, error) {
	return s.kv.Watch(ctx, id)
}

// truncateKey returns the first 12 characters of a key for safe logging.
func truncateKey(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:12] + "..."
}
