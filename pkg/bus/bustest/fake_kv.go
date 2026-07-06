package bustest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

// FakeKV implements bus.KV with in-memory storage.
// It supports TTL-based expiry, CAS (Update), Create-if-absent,
// Watch/WatchAll with initial values and live notifications.
type FakeKV struct {
	mu        sync.Mutex
	name      string
	entries   map[string]*fakeEntry
	revision  uint64
	ttl       time.Duration
	watchers  []*fakeWatcher
	getCounts map[string]int
}

// NewFakeKV creates a new in-memory KV store.
func NewFakeKV(name string, ttl time.Duration) *FakeKV {
	return &FakeKV{
		name:      name,
		entries:   make(map[string]*fakeEntry),
		ttl:       ttl,
		getCounts: make(map[string]int),
	}
}

func (kv *FakeKV) nextRevision() uint64 {
	kv.revision++
	return kv.revision
}

func (kv *FakeKV) isExpired(e *fakeEntry) bool {
	if kv.ttl == 0 {
		return false
	}
	return time.Since(e.created) > kv.ttl
}

func (kv *FakeKV) Bucket() string {
	return kv.name
}

func (kv *FakeKV) Get(_ context.Context, key string) (bus.KVEntry, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	kv.getCounts[key]++

	e, ok := kv.entries[key]
	if !ok || kv.isExpired(e) || e.operation == bus.KVOpDelete {
		return nil, bus.ErrKeyNotFound
	}
	return e, nil
}

// GetCount returns the number of times Get was called for a given key.
func (kv *FakeKV) GetCount(key string) int {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	return kv.getCounts[key]
}

// ResetGetCounts clears all Get call counters.
func (kv *FakeKV) ResetGetCounts() {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	kv.getCounts = make(map[string]int)
}

func (kv *FakeKV) Put(_ context.Context, key string, value []byte) (uint64, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	rev := kv.nextRevision()
	e := &fakeEntry{
		key:       key,
		value:     append([]byte(nil), value...),
		revision:  rev,
		created:   time.Now(),
		operation: bus.KVOpPut,
	}
	kv.entries[key] = e
	kv.notifyWatchers(e)
	return rev, nil
}

func (kv *FakeKV) Create(_ context.Context, key string, value []byte) (uint64, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	existing, ok := kv.entries[key]
	if ok && !kv.isExpired(existing) && existing.operation != bus.KVOpDelete {
		return 0, fmt.Errorf("key already exists: %s", key)
	}

	rev := kv.nextRevision()
	e := &fakeEntry{
		key:       key,
		value:     append([]byte(nil), value...),
		revision:  rev,
		created:   time.Now(),
		operation: bus.KVOpPut,
	}
	kv.entries[key] = e
	kv.notifyWatchers(e)
	return rev, nil
}

func (kv *FakeKV) Update(_ context.Context, key string, value []byte, last uint64) (uint64, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	existing, ok := kv.entries[key]
	if !ok {
		return 0, fmt.Errorf("key not found: %s", key)
	}
	if existing.revision != last {
		return 0, fmt.Errorf("wrong last revision for %s: expected %d, got %d", key, existing.revision, last)
	}

	rev := kv.nextRevision()
	e := &fakeEntry{
		key:       key,
		value:     append([]byte(nil), value...),
		revision:  rev,
		created:   time.Now(),
		operation: bus.KVOpPut,
	}
	kv.entries[key] = e
	kv.notifyWatchers(e)
	return rev, nil
}

// Delete writes a delete marker for key. Like the previous jetstream-backed
// fake, delete options (LastRevision) are accepted but not enforced.
func (kv *FakeKV) Delete(_ context.Context, key string, _ ...bus.KVDeleteOpt) error {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	_, ok := kv.entries[key]
	if !ok {
		return nil
	}

	rev := kv.nextRevision()
	e := &fakeEntry{
		key:       key,
		revision:  rev,
		created:   time.Now(),
		operation: bus.KVOpDelete,
	}
	kv.entries[key] = e
	kv.notifyWatchers(e)
	return nil
}

func (kv *FakeKV) Watch(_ context.Context, keys string) (bus.KeyWatcher, error) {
	return kv.watchFiltered([]string{keys})
}

func (kv *FakeKV) WatchAll(_ context.Context) (bus.KeyWatcher, error) {
	return kv.watchFiltered(nil)
}

// WatchFiltered watches all keys matching any of the given NATS
// subject-style filters (* matches one token, > matches one or more
// trailing tokens; a filter without wildcards is an exact key match).
// An empty filter list behaves like WatchAll. Like the real client, current
// values of matching keys are replayed first, followed by a nil sentinel.
func (kv *FakeKV) WatchFiltered(_ context.Context, keys []string) (bus.KeyWatcher, error) {
	return kv.watchFiltered(keys)
}

// watchFiltered registers a watcher for the given filters (nil = all keys),
// replaying current matching values followed by the nil sentinel.
func (kv *FakeKV) watchFiltered(filters []string) (bus.KeyWatcher, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	w := newFakeWatcher(filters...)

	for _, e := range kv.entries {
		if !kv.isExpired(e) && e.operation != bus.KVOpDelete && w.matches(e) {
			w.send(e)
		}
	}
	w.sendNil() // sentinel

	kv.watchers = append(kv.watchers, w)
	return w, nil
}

// Keys is deprecated; returns all keys as a slice.
func (kv *FakeKV) Keys(_ context.Context) ([]string, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	var keys []string
	for k, e := range kv.entries {
		if !kv.isExpired(e) && e.operation != bus.KVOpDelete {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, bus.ErrNoKeysFound
	}
	return keys, nil
}

func (kv *FakeKV) ListKeys(_ context.Context) (bus.KeyLister, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	var keys []string
	for k, e := range kv.entries {
		if !kv.isExpired(e) && e.operation != bus.KVOpDelete {
			keys = append(keys, k)
		}
	}

	if len(keys) == 0 {
		return nil, bus.ErrNoKeysFound
	}

	ch := make(chan string, len(keys))
	for _, k := range keys {
		ch <- k
	}
	close(ch)

	return &fakeKeyLister{ch: ch}, nil
}

// ListKeysFiltered returns the keys matching any of the given NATS
// subject-style filters (* matches one token, > matches one or more trailing
// tokens). No filters behaves like ListKeys. Like ListKeys, an empty result
// yields bus.ErrNoKeysFound.
func (kv *FakeKV) ListKeysFiltered(_ context.Context, filters ...string) (bus.KeyLister, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	var keys []string
	for k, e := range kv.entries {
		if kv.isExpired(e) || e.operation == bus.KVOpDelete {
			continue
		}
		if len(filters) == 0 {
			keys = append(keys, k)
			continue
		}
		for _, f := range filters {
			if matchSubject(f, k) {
				keys = append(keys, k)
				break
			}
		}
	}

	if len(keys) == 0 {
		return nil, bus.ErrNoKeysFound
	}

	ch := make(chan string, len(keys))
	for _, k := range keys {
		ch <- k
	}
	close(ch)

	return &fakeKeyLister{ch: ch}, nil
}

// notifyWatchers sends an entry to all matching watchers.
// Must be called with kv.mu held. Uses goroutines to avoid deadlocks.
func (kv *FakeKV) notifyWatchers(e *fakeEntry) {
	for _, w := range kv.watchers {
		if w.matches(e) {
			watcher := w
			entry := e
			go watcher.send(entry)
		}
	}
}

// --- fakeEntry implements bus.KVEntry ---

type fakeEntry struct {
	key       string
	value     []byte
	revision  uint64
	created   time.Time
	operation bus.KVOp
}

func (e *fakeEntry) Key() string         { return e.key }
func (e *fakeEntry) Value() []byte       { return e.value }
func (e *fakeEntry) Revision() uint64    { return e.revision }
func (e *fakeEntry) Operation() bus.KVOp { return e.operation }

// --- fakeWatcher implements bus.KeyWatcher ---

type fakeWatcher struct {
	ch      chan bus.KVEntry
	stop    chan struct{}
	filters []string // nil/empty for WatchAll
	once    sync.Once
}

func newFakeWatcher(filters ...string) *fakeWatcher {
	return &fakeWatcher{
		ch:      make(chan bus.KVEntry, 256),
		stop:    make(chan struct{}),
		filters: filters,
	}
}

func (w *fakeWatcher) Updates() <-chan bus.KVEntry { return w.ch }

func (w *fakeWatcher) Stop() error {
	w.once.Do(func() {
		close(w.stop)
	})
	return nil
}

func (w *fakeWatcher) matches(e *fakeEntry) bool {
	if len(w.filters) == 0 {
		return true // WatchAll
	}
	for _, f := range w.filters {
		if f == "" {
			return true // legacy Watch("") behaved as match-all
		}
		if matchSubject(f, e.key) {
			return true
		}
	}
	return false
}

func (w *fakeWatcher) send(e *fakeEntry) {
	select {
	case w.ch <- e:
	case <-w.stop:
	}
}

// sendNil delivers the end-of-replay sentinel (a nil interface value).
func (w *fakeWatcher) sendNil() {
	select {
	case w.ch <- nil:
	case <-w.stop:
	}
}

// CloseWatchers closes all active watcher channels, simulating a JetStream
// consumer loss (NATS cluster failure). After calling this, existing watchers
// will see their Updates() channel close, triggering reconnection logic.
func (kv *FakeKV) CloseWatchers() {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	for _, w := range kv.watchers {
		close(w.ch)
	}
	kv.watchers = nil
}

// WatcherCount returns the number of active watchers.
func (kv *FakeKV) WatcherCount() int {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	return len(kv.watchers)
}

// --- fakeKeyLister implements bus.KeyLister ---

type fakeKeyLister struct {
	ch chan string
}

func (l *fakeKeyLister) Keys() <-chan string { return l.ch }
func (l *fakeKeyLister) Stop() error         { return nil }
