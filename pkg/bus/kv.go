package bus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// ObjectStoreAPI is the subset of jetstream.JetStream used for Object Store operations.
// Separated from JetStreamAPI to avoid breaking existing test fakes.
type ObjectStoreAPI interface {
	CreateOrUpdateObjectStore(ctx context.Context, cfg jetstream.ObjectStoreConfig) (jetstream.ObjectStore, error)
	ObjectStore(ctx context.Context, bucket string) (jetstream.ObjectStore, error)
	DeleteObjectStore(ctx context.Context, bucket string) error
}

// ConsumerAPI is the subset of jetstream.JetStream used for durable stream
// consumers. Separated from JetStreamAPI to avoid breaking existing test fakes.
type ConsumerAPI interface {
	CreateOrUpdateConsumer(ctx context.Context, stream string, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error)
}

// KeyRevision is the well-known KV key used to signal that all files in a
// bucket have been updated atomically. Publishers bump this after writing all
// files; watchers observe only this key to avoid seeing partial updates.
const KeyRevision = "_revision"

// Standard KV bucket names used by Zester.
const (
	BucketFacts            = "facts"
	BucketSettingsFiles    = "settings-files"
	BucketSecrets          = "secrets"
	BucketBasket           = "basket"
	BucketJobs             = "jobs"
	BucketJobReturns       = "job-returns"
	BucketMasterHeartbeat  = "master-heartbeat"
	BucketEnrollments      = "enrollments"
	BucketEnrollChallenges = "enroll-challenges"
	BucketStateFiles       = "state-files"
	BucketUpdateManifests  = "update-manifests"
	BucketUpdateStatus     = "update-status"
	BucketUpdateRollouts   = "update-rollouts"
	BucketPeelHeartbeat    = "peel-heartbeat"
	BucketLeases           = "leases"
	BucketReactorFiles     = "reactor-files"

	// BucketMasterSettings replicates the RAW settings tree between masters,
	// with every file value SEALED to the shared account curve key (NaCl
	// box) — any master can open it (they all hold account.seed), no peel
	// can, and JetStream storage/backups see only ciphertext. Peel JWTs get
	// NO grant for this bucket. Manifest hashes cover the PLAINTEXT so the
	// publishers' hash-gate stays deterministic despite randomized seals.
	BucketMasterSettings = "master-settings"
)

// Object Store bucket names used by Zester.
const (
	ObjectBucketUpdateBinaries = "update-binaries"
)

// Standard JetStream stream names used by Zester.
const (
	StreamJobEvents = "job-events"
	StreamEvents    = "events"
)

// DurabilityTier classifies a JetStream asset by the cost of losing it.
type DurabilityTier int

const (
	// TierCritical marks assets whose loss is permanent and unrecoverable
	// (job history, enrollment records, secrets, published files). The zero
	// value is critical on purpose: untagged assets get the safe treatment.
	TierCritical DurabilityTier = iota

	// TierEphemeral marks TTL-scoped scratch data (heartbeats, challenge
	// nonces, leases) that is regenerated continuously and whose loss costs
	// at most one refresh interval.
	TierEphemeral
)

// BucketConfig defines configuration for a KV bucket.
type BucketConfig struct {
	// Bucket is the bucket name. Required.
	Bucket string

	// Description is a human-readable description.
	Description string

	// TTL is the time-to-live for entries. 0 = no expiry.
	TTL time.Duration

	// History is the number of historical values to keep per key.
	// Defaults to 1.
	History int

	// MaxValueSize is the maximum size of a single value in bytes.
	// 0 = default (server limit).
	MaxValueSize int32

	// MaxBytes is the maximum total size of the bucket. 0 = unlimited.
	MaxBytes int64

	// Replicas is the number of replicas for the bucket.
	// Defaults to 1. Use 3 for production clusters.
	Replicas int

	// Storage is the storage type (file or memory).
	// Defaults to file.
	Storage jetstream.StorageType

	// Tier is the durability tier used by InitializeStorageOpts to decide
	// when to warn about under-replication. Defaults to TierCritical.
	Tier DurabilityTier
}

func (c *BucketConfig) defaults() {
	if c.History == 0 {
		c.History = 1
	}
	if c.Replicas == 0 {
		c.Replicas = 1
	}
}

// CreateBucket creates a JetStream KV bucket with the given configuration.
// If the bucket already exists, it returns the existing one (idempotent).
func CreateBucket(ctx context.Context, js JetStreamAPI, cfg BucketConfig) (KV, error) {
	cfg.defaults()

	kvCfg := jetstream.KeyValueConfig{
		Bucket:       cfg.Bucket,
		Description:  cfg.Description,
		TTL:          cfg.TTL,
		History:      uint8(cfg.History),
		MaxValueSize: cfg.MaxValueSize,
		MaxBytes:     cfg.MaxBytes,
		Replicas:     cfg.Replicas,
		Storage:      cfg.Storage,
	}

	kv, err := js.CreateOrUpdateKeyValue(ctx, kvCfg)
	if err != nil {
		return nil, fmt.Errorf("bus: create bucket %q: %w", cfg.Bucket, err)
	}
	return kv, nil
}

// GetBucket retrieves an existing KV bucket by name.
func GetBucket(ctx context.Context, js JetStreamAPI, name string) (KV, error) {
	kv, err := js.KeyValue(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("bus: get bucket %q: %w", name, err)
	}
	return kv, nil
}

// DeleteBucket deletes a KV bucket by name.
func DeleteBucket(ctx context.Context, js JetStreamAPI, name string) error {
	if err := js.DeleteKeyValue(ctx, name); err != nil {
		return fmt.Errorf("bus: delete bucket %q: %w", name, err)
	}
	return nil
}

// KVPut encodes v as MessagePack and stores it in the KV bucket at the given key.
func KVPut(ctx context.Context, kv KV, key string, v any) (uint64, error) {
	data, err := Encode(v)
	if err != nil {
		return 0, err
	}
	rev, err := kv.Put(ctx, key, data)
	if err != nil {
		return 0, fmt.Errorf("bus: kv put %q: %w", key, err)
	}
	return rev, nil
}

// KVGetAll drains all entries from a KV bucket via WatchAll and decodes each
// value from MessagePack into T. Returns a map keyed by KV key. Deleted keys
// are excluded. This is much more efficient than N individual KVGet calls when
// you need the entire bucket contents.
func KVGetAll[T any](ctx context.Context, kv KV) (map[string]T, error) {
	watcher, err := kv.WatchAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("bus: kv watch all: %w", err)
	}
	defer watcher.Stop()

	result := make(map[string]T)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case entry := <-watcher.Updates():
			if entry == nil {
				// nil sentinel = InitDone, all current values delivered.
				return result, nil
			}
			if entry.Operation() != KVOpPut {
				// Skip delete/purge markers (bus.KV.WatchAll has no
				// IgnoreDeletes option; filtering here is equivalent).
				continue
			}
			var v T
			if err := Decode(entry.Value(), &v); err != nil {
				return nil, fmt.Errorf("bus: kv decode %q: %w", entry.Key(), err)
			}
			result[entry.Key()] = v
		}
	}
}

// KVGet retrieves and decodes a MessagePack value from the KV bucket.
func KVGet(ctx context.Context, kv KV, key string, v any) error {
	entry, err := kv.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("bus: kv get %q: %w", key, err)
	}
	return Decode(entry.Value(), v)
}

// GetRevision reads the current _revision counter from a KV bucket.
// Returns 0 if the key does not exist.
func GetRevision(ctx context.Context, kv KV) uint64 {
	entry, err := kv.Get(ctx, KeyRevision)
	if err != nil {
		return 0
	}
	var rev uint64
	fmt.Sscanf(string(entry.Value()), "%d", &rev)
	return rev
}

// bumpRevisionMaxAttempts bounds the CAS retry loop in BumpRevision.
const bumpRevisionMaxAttempts = 10

// bumpRevisionRetryDelay returns a small jittered backoff between CAS retries
// so concurrent publishers do not lock-step on the same revision.
func bumpRevisionRetryDelay() time.Duration {
	return 5*time.Millisecond + time.Duration(rand.IntN(15))*time.Millisecond
}

// BumpRevision atomically increments the _revision counter in a KV bucket
// using a compare-and-swap retry loop: the first revision is written with
// kv.Create, subsequent increments with kv.Update fenced on the revision read
// beforehand, so concurrent publishers never lose increments. CAS conflicts
// (key created or updated by another publisher between the read and the
// write) are retried up to bumpRevisionMaxAttempts times with a small
// jittered delay; after exhaustion a wrapped error is returned.
//
// Publishers call this after writing all files so that watchers observing only
// the revision key can detect a consistent batch update.
func BumpRevision(ctx context.Context, kv KV) error {
	var lastErr error
	for attempt := 0; attempt < bumpRevisionMaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("bus: bump revision: %w", ctx.Err())
			case <-time.After(bumpRevisionRetryDelay()):
			}
		}

		entry, err := kv.Get(ctx, KeyRevision)
		if err != nil {
			if !errors.Is(err, ErrKeyNotFound) {
				return fmt.Errorf("bus: bump revision: read %q: %w", KeyRevision, err)
			}
			// Key missing: create the first revision. A failure here is
			// most likely ErrKeyExists from racing another publisher's
			// Create — retry the loop and re-read.
			if _, cerr := kv.Create(ctx, KeyRevision, []byte("1")); cerr == nil {
				return nil
			} else {
				lastErr = cerr
				continue
			}
		}

		var current uint64
		fmt.Sscanf(string(entry.Value()), "%d", &current)
		next := []byte(strconv.FormatUint(current+1, 10))
		// Update failures are dominated by CAS conflicts (wrong last
		// revision / key-exists class errors from jetstream when another
		// publisher bumped first) — retry the loop and re-read.
		if _, uerr := kv.Update(ctx, KeyRevision, next, entry.Revision()); uerr == nil {
			return nil
		} else {
			lastErr = uerr
			continue
		}
	}
	return fmt.Errorf("bus: bump revision: gave up after %d attempts: %w", bumpRevisionMaxAttempts, lastErr)
}

// DefaultBuckets returns the configurations for all standard Zester KV buckets.
func DefaultBuckets() []BucketConfig {
	return []BucketConfig{
		{
			Bucket:      BucketFacts,
			Description: "Peel fact data (system information)",
			History:     5,
			Replicas:    1,
			Tier:        TierCritical,
		},
		{
			Bucket:      BucketSettingsFiles,
			Description: "Raw .zy template files for peel-side rendering",
			History:     3,
			Replicas:    1,
			Tier:        TierCritical,
		},
		{
			Bucket:      BucketSecrets,
			Description: "Per-peel encrypted sensitive values",
			History:     3,
			Replicas:    1,
			Tier:        TierCritical,
		},
		{
			Bucket:      BucketBasket,
			Description: "Peel-to-peel data sharing",
			History:     1,
			Replicas:    1,
			Tier:        TierCritical,
		},
		{
			Bucket:      BucketJobs,
			Description: "Job specs and status tracking",
			TTL:         7 * 24 * time.Hour, // 7 days
			History:     10,
			Replicas:    1,
			Tier:        TierCritical,
		},
		{
			Bucket:      BucketJobReturns,
			Description: "Per-peel job execution results",
			TTL:         7 * 24 * time.Hour, // 7 days
			History:     1,
			Replicas:    1,
			Tier:        TierCritical,
		},
		{
			Bucket:      BucketMasterHeartbeat,
			Description: "Master instance heartbeats for liveness detection",
			TTL:         15 * time.Second, // 3x heartbeat interval (5s)
			History:     1,
			Replicas:    1,
			Tier:        TierEphemeral,
		},
		{
			Bucket:      BucketEnrollments,
			Description: "Peel enrollment records and state",
			History:     10,
			Replicas:    1,
			Tier:        TierCritical,
		},
		{
			Bucket:      BucketEnrollChallenges,
			Description: "Short-lived enrollment challenge nonces",
			TTL:         5 * time.Minute,
			History:     1,
			Replicas:    1,
			Storage:     jetstream.MemoryStorage,
			Tier:        TierEphemeral,
		},
		{
			Bucket:      BucketStateFiles,
			Description: "Raw .zy state files for peel-side caching",
			History:     3,
			Replicas:    1,
			Tier:        TierCritical,
		},
		{
			Bucket:      BucketUpdateManifests,
			Description: "Published binary update manifests",
			History:     5,
			Replicas:    1,
			Tier:        TierCritical,
		},
		{
			Bucket:      BucketUpdateStatus,
			Description: "Per-node watchdog status heartbeat",
			TTL:         60 * time.Second,
			History:     1,
			Replicas:    1,
			Tier:        TierEphemeral,
		},
		{
			Bucket:      BucketUpdateRollouts,
			Description: "Rollout state and progress tracking",
			History:     10,
			Replicas:    1,
			Tier:        TierCritical,
		},
		{
			Bucket:      BucketPeelHeartbeat,
			Description: "Peel liveness heartbeats",
			TTL:         30 * time.Second,
			History:     1,
			Replicas:    1,
			Tier:        TierEphemeral,
		},
		{
			Bucket:      BucketLeases,
			Description: "Advisory leader leases for single-publisher work",
			TTL:         15 * time.Second, // must match LeaderLease default TTL
			History:     1,
			Replicas:    1,
			Tier:        TierEphemeral,
		},
		{
			Bucket:      BucketReactorFiles,
			Description: "Reactor rule files (master-only; no peel JWT grants)",
			History:     3,
			Replicas:    1,
			Tier:        TierCritical,
		},
		{
			Bucket:      BucketMasterSettings,
			Description: "Raw settings tree sealed to the account key (masters-only; no peel JWT grants)",
			// History 1: old sealed ciphertext (and its keyed manifest hash)
			// must not linger after a secret rotation — the mirror only ever
			// reads the current revision.
			History:  1,
			Replicas: 1,
			Tier:     TierCritical,
		},
	}
}

// CreateDefaultBuckets creates all standard Zester KV buckets.
// Returns a map of bucket name to KV interface.
func CreateDefaultBuckets(ctx context.Context, js JetStreamAPI) (map[string]KV, error) {
	buckets := make(map[string]KV)

	for _, cfg := range DefaultBuckets() {
		kv, err := CreateBucket(ctx, js, cfg)
		if err != nil {
			return nil, err
		}
		buckets[cfg.Bucket] = kv
	}
	return buckets, nil
}

// StreamConfig holds configuration for creating a JetStream stream.
type StreamConfig struct {
	// Name is the stream name. Required.
	Name string

	// Description is a human-readable description.
	Description string

	// Subjects is the list of subjects captured by this stream.
	Subjects []string

	// MaxAge is the maximum age of messages in the stream. 0 = unlimited.
	MaxAge time.Duration

	// MaxBytes is the maximum total size. 0 = unlimited.
	MaxBytes int64

	// MaxMsgs is the maximum number of messages. 0 = unlimited.
	MaxMsgs int64

	// Replicas is the number of replicas. Defaults to 1.
	Replicas int

	// Retention is the retention policy. Defaults to LimitsPolicy.
	Retention jetstream.RetentionPolicy

	// Storage is the storage type. Defaults to FileStorage.
	Storage jetstream.StorageType

	// Duplicates is the message-ID deduplication window: publishes carrying
	// the same Nats-Msg-Id header within this window are dropped by the
	// server. 0 = server default.
	Duplicates time.Duration

	// Tier is the durability tier used by InitializeStorageOpts to decide
	// when to warn about under-replication. Defaults to TierCritical.
	Tier DurabilityTier
}

func (c *StreamConfig) defaults() {
	if c.Replicas == 0 {
		c.Replicas = 1
	}
}

// CreateStream creates a JetStream stream with the given configuration.
func CreateStream(ctx context.Context, js JetStreamAPI, cfg StreamConfig) (jetstream.Stream, error) {
	cfg.defaults()

	sCfg := jetstream.StreamConfig{
		Name:        cfg.Name,
		Description: cfg.Description,
		Subjects:    cfg.Subjects,
		MaxAge:      cfg.MaxAge,
		MaxBytes:    cfg.MaxBytes,
		MaxMsgs:     cfg.MaxMsgs,
		Replicas:    cfg.Replicas,
		Retention:   cfg.Retention,
		Storage:     cfg.Storage,
		Duplicates:  cfg.Duplicates,
	}

	s, err := js.CreateStream(ctx, sCfg)
	if err != nil {
		return nil, fmt.Errorf("bus: create stream %q: %w", cfg.Name, err)
	}
	return s, nil
}

// DefaultJobEventsStream returns the configuration for the job-events stream.
func DefaultJobEventsStream() StreamConfig {
	return StreamConfig{
		Name:        StreamJobEvents,
		Description: "Full job event log for replay and audit",
		Subjects:    []string{SubjectJob + ".>"},
		MaxAge:      7 * 24 * time.Hour,
		Replicas:    1,
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		Tier:        TierCritical,
	}
}

// DefaultEventsStream returns the configuration for the reactor events
// stream. It captures every publish under zester.event.> durably so the
// masters' shared reactor consumer processes each event exactly once
// fleet-wide and replays events published during master downtime. MaxBytes
// and MaxMsgs bound flood damage from a compromised peel; the Duplicates
// window collapses redelivered derived-event emissions published with a
// deterministic message ID.
func DefaultEventsStream() StreamConfig {
	return StreamConfig{
		Name:        StreamEvents,
		Description: "Reactor event log",
		Subjects:    []string{EventSubjectAll()},
		MaxAge:      7 * 24 * time.Hour,
		MaxBytes:    1 << 30, // 1 GiB
		MaxMsgs:     1_000_000,
		Replicas:    1,
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		Duplicates:  2 * time.Minute,
		Tier:        TierCritical,
	}
}

// DefaultStreams returns the configurations for all standard Zester
// JetStream streams.
func DefaultStreams() []StreamConfig {
	return []StreamConfig{
		DefaultJobEventsStream(),
		DefaultEventsStream(),
	}
}

// CreateDefaultStreams creates all standard Zester JetStream streams.
func CreateDefaultStreams(ctx context.Context, js JetStreamAPI) (map[string]jetstream.Stream, error) {
	streams := make(map[string]jetstream.Stream)

	for _, cfg := range DefaultStreams() {
		s, err := CreateStream(ctx, js, cfg)
		if err != nil {
			return nil, err
		}
		streams[cfg.Name] = s
	}
	return streams, nil
}

// EffectiveReplicas returns the replica count to use for a JetStream asset.
// An explicit operator-set count (explicit > 0) always wins; otherwise the
// count scales with the NATS cluster size, floored at 1 and capped at 3 (the
// JetStream RAFT sweet spot — R5 buys little for KV workloads).
func EffectiveReplicas(explicit, clusterSize int) int {
	if explicit > 0 {
		return explicit
	}
	return min(3, max(1, clusterSize))
}

// StorageOptions configures InitializeStorageOpts and InitializeObjectStoresOpts.
type StorageOptions struct {
	// Replicas, when > 0, forces this replica count on ALL assets regardless
	// of tier or cluster size (the historical InitializeStorage behavior).
	Replicas int

	// ClusterSize is the number of NATS servers in the cluster. When
	// Replicas is 0, every asset gets EffectiveReplicas(0, ClusterSize).
	ClusterSize int

	// Logger receives under-replication and migration warnings.
	// Defaults to slog.Default().
	Logger *slog.Logger
}

func (o *StorageOptions) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
}

// InitializeStorage creates all default KV buckets and streams.
// This is typically called once during master startup.
// An optional replicas argument overrides the Replicas field on all bucket
// and stream configs (e.g., pass 3 for a 3-node NATS cluster). When omitted
// or zero, the per-bucket/stream defaults (1) are used.
//
// New callers should prefer InitializeStorageOpts, which can scale replicas
// with the cluster size instead of requiring an explicit count.
func InitializeStorage(ctx context.Context, js JetStreamAPI, replicas ...int) error {
	opts := StorageOptions{}
	if len(replicas) > 0 && replicas[0] > 0 {
		opts.Replicas = replicas[0]
	}
	return InitializeStorageOpts(ctx, js, opts)
}

// InitializeStorageOpts creates all default KV buckets and streams with
// tier-aware replica counts.
//
// Every asset — critical and ephemeral alike — gets
// EffectiveReplicas(opts.Replicas, opts.ClusterSize) replicas. Ephemeral
// buckets are deliberately not kept at R1: they are cheap TTL scratch data,
// but heartbeat and lease buckets must survive a single node loss for
// liveness detection to keep working, and one rule keeps the code simple.
// The tier controls only warning behavior: a critical asset whose effective
// replica count is below min(3, ClusterSize) (an operator explicitly forced
// a low count in a cluster) logs a loud warning per asset, because a
// single-node disk failure then permanently loses that data.
//
// Replica upgrades on existing buckets can fail (older servers, insufficient
// cluster resources). Such failures never abort startup: the asset is
// re-created with its previous default of 1 replica and a warning explains
// how to migrate manually.
func InitializeStorageOpts(ctx context.Context, js JetStreamAPI, opts StorageOptions) error {
	logger := opts.logger()
	effective := EffectiveReplicas(opts.Replicas, opts.ClusterSize)
	recommended := min(3, max(1, opts.ClusterSize))

	for _, cfg := range DefaultBuckets() {
		warnIfUnderReplicated(logger, "bucket", cfg.Bucket, cfg.Tier, effective, recommended)
		cfg.Replicas = effective
		if _, err := CreateBucket(ctx, js, cfg); err != nil {
			if effective <= 1 {
				return fmt.Errorf("bus: initialize buckets: %w", err)
			}
			cfg.Replicas = 1
			if _, retryErr := CreateBucket(ctx, js, cfg); retryErr != nil {
				return fmt.Errorf("bus: initialize buckets: replicas=%d failed (%v); replicas=1 failed: %w", effective, err, retryErr)
			}
			warnReplicaFallback(logger, "bucket", cfg.Bucket, "KV_"+cfg.Bucket, effective, err)
		}
	}

	for _, streamCfg := range DefaultStreams() {
		warnIfUnderReplicated(logger, "stream", streamCfg.Name, streamCfg.Tier, effective, recommended)
		streamCfg.Replicas = effective
		if _, err := CreateStream(ctx, js, streamCfg); err != nil {
			if effective <= 1 {
				return fmt.Errorf("bus: initialize streams: %w", err)
			}
			streamCfg.Replicas = 1
			if _, retryErr := CreateStream(ctx, js, streamCfg); retryErr != nil {
				return fmt.Errorf("bus: initialize streams: replicas=%d failed (%v); replicas=1 failed: %w", effective, err, retryErr)
			}
			warnReplicaFallback(logger, "stream", streamCfg.Name, streamCfg.Name, effective, err)
		}
	}

	return nil
}

// warnIfUnderReplicated logs a loud warning when a critical asset ends up
// with fewer replicas than the cluster could provide.
func warnIfUnderReplicated(logger *slog.Logger, kind, name string, tier DurabilityTier, effective, recommended int) {
	if tier != TierCritical || effective >= recommended {
		return
	}
	logger.Warn("critical "+kind+" is under-replicated: a single-node disk failure permanently loses this data",
		kind, name,
		"replicas", effective,
		"recommended", recommended)
}

// warnReplicaFallback logs a loud warning after a failed replica change was
// worked around by keeping the asset at its previous default of 1 replica.
func warnReplicaFallback(logger *slog.Logger, kind, name, streamName string, wanted int, err error) {
	logger.Warn(kind+" kept 1 replica: replica change failed; migrate manually",
		kind, name,
		"wanted_replicas", wanted,
		"error", err,
		"hint", fmt.Sprintf("nats stream edit %s --replicas=%d", streamName, wanted))
}

// DefaultObjectStoreConfig returns the configuration for the update binaries Object Store.
// The store is a critical asset: published binaries are not regenerated.
func DefaultObjectStoreConfig() jetstream.ObjectStoreConfig {
	return jetstream.ObjectStoreConfig{
		Bucket:      ObjectBucketUpdateBinaries,
		Description: "Binary artifacts for self-update distribution",
		TTL:         30 * 24 * time.Hour, // 30 days
		Replicas:    1,
	}
}

// InitializeObjectStores creates all default Object Store buckets.
// Separated from InitializeStorage because Object Store operations are not
// part of the narrow JetStreamAPI interface used by most of Zester.
//
// New callers should prefer InitializeObjectStoresOpts.
func InitializeObjectStores(ctx context.Context, js ObjectStoreAPI, replicas ...int) error {
	opts := StorageOptions{}
	if len(replicas) > 0 && replicas[0] > 0 {
		opts.Replicas = replicas[0]
	}
	return InitializeObjectStoresOpts(ctx, js, opts)
}

// InitializeObjectStoresOpts creates all default Object Store buckets with
// the same replica semantics as InitializeStorageOpts. The update-binaries
// store is a critical asset, so it warns when explicitly forced below the
// cluster's recommended count, and a failed replica change falls back to the
// previous default of 1 replica with a migration warning instead of aborting
// startup.
func InitializeObjectStoresOpts(ctx context.Context, js ObjectStoreAPI, opts StorageOptions) error {
	logger := opts.logger()
	effective := EffectiveReplicas(opts.Replicas, opts.ClusterSize)
	recommended := min(3, max(1, opts.ClusterSize))

	cfg := DefaultObjectStoreConfig()
	warnIfUnderReplicated(logger, "object store", cfg.Bucket, TierCritical, effective, recommended)
	cfg.Replicas = effective
	if _, err := js.CreateOrUpdateObjectStore(ctx, cfg); err != nil {
		if effective <= 1 {
			return fmt.Errorf("bus: initialize object store %q: %w", cfg.Bucket, err)
		}
		cfg.Replicas = 1
		if _, retryErr := js.CreateOrUpdateObjectStore(ctx, cfg); retryErr != nil {
			return fmt.Errorf("bus: initialize object store %q: replicas=%d failed (%v); replicas=1 failed: %w", cfg.Bucket, effective, err, retryErr)
		}
		warnReplicaFallback(logger, "object store", cfg.Bucket, "OBJ_"+cfg.Bucket, effective, err)
	}
	return nil
}

// ListKeysWithPrefix lists the descendant keys under the given parent
// token(s) in a KV bucket. KV keys are NATS subject tokens, so prefix
// "jid123" matches child keys like "jid123.web-01" but not the bare key
// "jid123" itself. A prefix that already ends in the ">" wildcard (e.g.
// "active.>") is used as the filter verbatim. Results are sorted; an empty
// result is not an error — (nil, nil) is returned.
func ListKeysWithPrefix(ctx context.Context, kv KV, prefix string) ([]string, error) {
	filter := prefix
	if !strings.HasSuffix(filter, WildcardMany) {
		filter = strings.TrimSuffix(filter, ".") + "." + WildcardMany
	}

	lister, err := kv.ListKeysFiltered(ctx, filter)
	if err != nil {
		if errors.Is(err, ErrNoKeysFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("bus: list keys %q: %w", filter, err)
	}
	defer lister.Stop()

	var keys []string
	for k := range lister.Keys() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}
