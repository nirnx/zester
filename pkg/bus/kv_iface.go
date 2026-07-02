package bus

import (
	"context"

	"github.com/nats-io/nats.go/jetstream"
)

// Sentinel errors for KV operations. These alias the jetstream sentinels so
// that errors.Is checks work identically whether a caller compares against
// bus.ErrKeyNotFound or jetstream.ErrKeyNotFound — adapters pass the
// underlying errors through unwrapped. Domain packages should use the bus
// forms so they need no jetstream import.
var (
	// ErrKeyNotFound is returned by Get when the key does not exist.
	ErrKeyNotFound = jetstream.ErrKeyNotFound

	// ErrKeyExists is returned by Create when the key already exists.
	ErrKeyExists = jetstream.ErrKeyExists

	// ErrNoKeysFound is returned by Keys/ListKeys/ListKeysFiltered when the
	// bucket has no (matching) keys.
	ErrNoKeysFound = jetstream.ErrNoKeysFound
)

// KVOp is the operation that produced a KV entry.
type KVOp int

// KVOp values. Named KVOp* because KVPut/KVGet are the bus package's
// MessagePack helper functions.
const (
	// KVOpPut is a create or update of a key's value.
	KVOpPut KVOp = iota

	// KVOpDelete is a delete marker for a key.
	KVOpDelete

	// KVOpPurge removes a key and all its previous revisions.
	KVOpPurge
)

// KVEntry is a bus-owned view of a single KV entry.
type KVEntry interface {
	// Key is the name of the entry.
	Key() string

	// Value is the entry payload. Delete/purge markers carry no value.
	Value() []byte

	// Revision is the unique, monotonically increasing sequence of the entry
	// within its bucket, used for CAS (Update, LastRevision).
	Revision() uint64

	// Operation reports how the entry was written (put, delete, purge).
	Operation() KVOp
}

// KeyWatcher streams KV entries for watched keys. The current values of all
// matching keys are replayed first, followed by a single nil entry (the
// end-of-replay sentinel), then live updates. When the underlying JetStream
// consumer is lost, the Updates channel closes; callers re-create the watcher
// to resume (see settings.runWatchLoop).
type KeyWatcher interface {
	// Updates returns the entry channel. A nil entry marks the end of the
	// initial replay; a closed channel means the watcher is stopped or the
	// consumer was lost.
	Updates() <-chan KVEntry

	// Stop halts the watcher and eventually closes the Updates channel.
	Stop() error
}

// KeyLister streams key names. jetstream.KeyLister satisfies this directly.
type KeyLister interface {
	// Keys returns the key name channel, closed after the last key.
	Keys() <-chan string

	// Stop halts iteration early.
	Stop() error
}

// KVDeleteOpt configures a KV Delete operation.
type KVDeleteOpt func(*kvDeleteOpts)

type kvDeleteOpts struct {
	lastRevision uint64
}

// LastRevision fences a Delete on the given revision: the delete fails if the
// key was written since (compare-and-delete).
func LastRevision(rev uint64) KVDeleteOpt {
	return func(o *kvDeleteOpts) { o.lastRevision = rev }
}

// KV is the narrow key-value interface Zester uses, owned by pkg/bus.
// Production code gets a jetstream-backed implementation from GetBucket /
// JetStreamAPI; tests use bustest.FakeKV. Implementations must return errors
// matching the bus sentinels above via errors.Is.
type KV interface {
	// Get returns the latest entry for key, or ErrKeyNotFound.
	Get(ctx context.Context, key string) (KVEntry, error)

	// Put stores value under key and returns the new revision.
	Put(ctx context.Context, key string, value []byte) (uint64, error)

	// Create stores value only if key does not exist (ErrKeyExists otherwise).
	Create(ctx context.Context, key string, value []byte) (uint64, error)

	// Update stores value only if the key's current revision matches
	// (compare-and-swap).
	Update(ctx context.Context, key string, value []byte, revision uint64) (uint64, error)

	// Delete writes a delete marker for key. LastRevision makes it a
	// compare-and-delete.
	Delete(ctx context.Context, key string, opts ...KVDeleteOpt) error

	// Keys returns all live key names, or ErrNoKeysFound when empty.
	Keys(ctx context.Context) ([]string, error)

	// ListKeys streams all live key names, or ErrNoKeysFound when empty.
	ListKeys(ctx context.Context) (KeyLister, error)

	// ListKeysFiltered streams key names matching any of the NATS
	// subject-style filters (* one token, > trailing tokens); no filters
	// behaves like ListKeys. Returns ErrNoKeysFound when nothing matches.
	ListKeysFiltered(ctx context.Context, filters ...string) (KeyLister, error)

	// Watch watches a single key or NATS subject-style pattern.
	Watch(ctx context.Context, keys string) (KeyWatcher, error)

	// WatchAll watches every key in the bucket.
	WatchAll(ctx context.Context) (KeyWatcher, error)

	// WatchFiltered watches keys matching any of the given filters; an empty
	// list behaves like WatchAll.
	WatchFiltered(ctx context.Context, keys []string) (KeyWatcher, error)
}
