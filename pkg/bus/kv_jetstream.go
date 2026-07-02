package bus

import (
	"context"
	"sync"

	"github.com/nats-io/nats.go/jetstream"
)

// JS adapts a raw jetstream.JetStream to the bus-owned narrow interfaces.
// It satisfies JetStreamAPI (KV operations return bus.KV), ConsumerAPI, and
// ObjectStoreAPI, so a single adapter serves every production call site.
// Errors from the underlying client are passed through unwrapped so that
// errors.Is checks against the jetstream sentinels (and their bus aliases)
// keep working.
type JS struct {
	js jetstream.JetStream
}

// NewJS wraps a jetstream.JetStream in the bus adapter.
func NewJS(js jetstream.JetStream) *JS {
	return &JS{js: js}
}

// Unwrap returns the underlying jetstream.JetStream for the rare call site
// that needs the full client (none in Zester today; escape hatch for tools).
func (j *JS) Unwrap() jetstream.JetStream {
	return j.js
}

func (j *JS) CreateOrUpdateKeyValue(ctx context.Context, cfg jetstream.KeyValueConfig) (KV, error) {
	kv, err := j.js.CreateOrUpdateKeyValue(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return WrapKeyValue(kv), nil
}

func (j *JS) KeyValue(ctx context.Context, bucket string) (KV, error) {
	kv, err := j.js.KeyValue(ctx, bucket)
	if err != nil {
		return nil, err
	}
	return WrapKeyValue(kv), nil
}

func (j *JS) DeleteKeyValue(ctx context.Context, bucket string) error {
	return j.js.DeleteKeyValue(ctx, bucket)
}

func (j *JS) CreateStream(ctx context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error) {
	return j.js.CreateStream(ctx, cfg)
}

func (j *JS) Publish(ctx context.Context, subject string, payload []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	return j.js.Publish(ctx, subject, payload, opts...)
}

func (j *JS) CreateOrUpdateConsumer(ctx context.Context, stream string, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	return j.js.CreateOrUpdateConsumer(ctx, stream, cfg)
}

func (j *JS) CreateOrUpdateObjectStore(ctx context.Context, cfg jetstream.ObjectStoreConfig) (jetstream.ObjectStore, error) {
	return j.js.CreateOrUpdateObjectStore(ctx, cfg)
}

func (j *JS) ObjectStore(ctx context.Context, bucket string) (jetstream.ObjectStore, error) {
	return j.js.ObjectStore(ctx, bucket)
}

func (j *JS) DeleteObjectStore(ctx context.Context, bucket string) error {
	return j.js.DeleteObjectStore(ctx, bucket)
}

var (
	_ JetStreamAPI   = (*JS)(nil)
	_ ConsumerAPI    = (*JS)(nil)
	_ ObjectStoreAPI = (*JS)(nil)
)

// WrapKeyValue adapts a raw jetstream.KeyValue to the bus.KV interface, for
// call sites (integration tests, tools) that build their own KV handle.
func WrapKeyValue(kv jetstream.KeyValue) KV {
	return &jetstreamKV{kv: kv}
}

// jetstreamKV adapts jetstream.KeyValue behind bus.KV once, so domain
// packages never hold nats.go types.
type jetstreamKV struct {
	kv jetstream.KeyValue
}

func (k *jetstreamKV) Get(ctx context.Context, key string) (KVEntry, error) {
	e, err := k.kv.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return jetstreamEntry{e: e}, nil
}

func (k *jetstreamKV) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	return k.kv.Put(ctx, key, value)
}

func (k *jetstreamKV) Create(ctx context.Context, key string, value []byte) (uint64, error) {
	return k.kv.Create(ctx, key, value)
}

func (k *jetstreamKV) Update(ctx context.Context, key string, value []byte, revision uint64) (uint64, error) {
	return k.kv.Update(ctx, key, value, revision)
}

func (k *jetstreamKV) Delete(ctx context.Context, key string, opts ...KVDeleteOpt) error {
	var o kvDeleteOpts
	for _, opt := range opts {
		opt(&o)
	}
	if o.lastRevision > 0 {
		return k.kv.Delete(ctx, key, jetstream.LastRevision(o.lastRevision))
	}
	return k.kv.Delete(ctx, key)
}

func (k *jetstreamKV) Keys(ctx context.Context) ([]string, error) {
	return k.kv.Keys(ctx)
}

func (k *jetstreamKV) ListKeys(ctx context.Context) (KeyLister, error) {
	return k.kv.ListKeys(ctx)
}

func (k *jetstreamKV) ListKeysFiltered(ctx context.Context, filters ...string) (KeyLister, error) {
	return k.kv.ListKeysFiltered(ctx, filters...)
}

func (k *jetstreamKV) Watch(ctx context.Context, keys string) (KeyWatcher, error) {
	w, err := k.kv.Watch(ctx, keys)
	if err != nil {
		return nil, err
	}
	return newJetstreamWatcher(w), nil
}

func (k *jetstreamKV) WatchAll(ctx context.Context) (KeyWatcher, error) {
	w, err := k.kv.WatchAll(ctx)
	if err != nil {
		return nil, err
	}
	return newJetstreamWatcher(w), nil
}

func (k *jetstreamKV) WatchFiltered(ctx context.Context, keys []string) (KeyWatcher, error) {
	w, err := k.kv.WatchFiltered(ctx, keys)
	if err != nil {
		return nil, err
	}
	return newJetstreamWatcher(w), nil
}

// jetstreamEntry adapts jetstream.KeyValueEntry to bus.KVEntry.
type jetstreamEntry struct {
	e jetstream.KeyValueEntry
}

func (e jetstreamEntry) Key() string      { return e.e.Key() }
func (e jetstreamEntry) Value() []byte    { return e.e.Value() }
func (e jetstreamEntry) Revision() uint64 { return e.e.Revision() }

func (e jetstreamEntry) Operation() KVOp {
	switch e.e.Operation() {
	case jetstream.KeyValueDelete:
		return KVOpDelete
	case jetstream.KeyValuePurge:
		return KVOpPurge
	default:
		return KVOpPut
	}
}

// jetstreamWatcher adapts jetstream.KeyWatcher to bus.KeyWatcher, preserving
// the channel semantics watchers depend on: entries are forwarded in order,
// the nil end-of-replay sentinel stays a nil KVEntry, and the output channel
// closes when the underlying channel closes (consumer loss) or after Stop.
type jetstreamWatcher struct {
	w    jetstream.KeyWatcher
	ch   chan KVEntry
	stop chan struct{}
	once sync.Once
}

func newJetstreamWatcher(w jetstream.KeyWatcher) *jetstreamWatcher {
	jw := &jetstreamWatcher{
		w:    w,
		ch:   make(chan KVEntry, 256),
		stop: make(chan struct{}),
	}
	go jw.pump()
	return jw
}

func (jw *jetstreamWatcher) pump() {
	defer close(jw.ch)
	for {
		select {
		case <-jw.stop:
			return
		case e, ok := <-jw.w.Updates():
			if !ok {
				return
			}
			var out KVEntry
			if e != nil {
				out = jetstreamEntry{e: e}
			}
			select {
			case jw.ch <- out:
			case <-jw.stop:
				return
			}
		}
	}
}

func (jw *jetstreamWatcher) Updates() <-chan KVEntry { return jw.ch }

func (jw *jetstreamWatcher) Stop() error {
	jw.once.Do(func() { close(jw.stop) })
	return jw.w.Stop()
}
