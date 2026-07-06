package bustest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/nirnx/zester/pkg/bus"
)

// FakeJS implements bus.JetStreamAPI with in-memory KV buckets and streams.
type FakeJS struct {
	mu      sync.RWMutex
	buckets map[string]*FakeKV
	streams map[string]jetstream.Stream
}

// NewFakeJS creates a new in-memory JetStream fake.
func NewFakeJS() *FakeJS {
	return &FakeJS{
		buckets: make(map[string]*FakeKV),
		streams: make(map[string]jetstream.Stream),
	}
}

func (f *FakeJS) CreateOrUpdateKeyValue(_ context.Context, cfg jetstream.KeyValueConfig) (bus.KV, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if existing, ok := f.buckets[cfg.Bucket]; ok {
		existing.mu.Lock()
		existing.ttl = cfg.TTL
		existing.mu.Unlock()
		return existing, nil
	}

	kv := NewFakeKV(cfg.Bucket, cfg.TTL)
	f.buckets[cfg.Bucket] = kv
	return kv, nil
}

func (f *FakeJS) KeyValue(_ context.Context, bucket string) (bus.KV, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	kv, ok := f.buckets[bucket]
	if !ok {
		return nil, fmt.Errorf("bucket not found: %s", bucket)
	}
	return kv, nil
}

func (f *FakeJS) DeleteKeyValue(_ context.Context, bucket string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.buckets[bucket]; !ok {
		return fmt.Errorf("bucket not found: %s", bucket)
	}
	delete(f.buckets, bucket)
	return nil
}

func (f *FakeJS) CreateStream(_ context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	s := &fakeStream{name: cfg.Name, cfg: cfg}
	f.streams[cfg.Name] = s
	return s, nil
}

func (f *FakeJS) Publish(_ context.Context, _ string, _ []byte, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	return &jetstream.PubAck{Stream: "fake"}, nil
}

// GetBucket returns a FakeKV by name, for direct test access.
func (f *FakeJS) GetBucket(name string) *FakeKV {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.buckets[name]
}

// --- fakeStream implements jetstream.Stream ---

type fakeStream struct {
	name string
	cfg  jetstream.StreamConfig
}

func (s *fakeStream) Info(_ context.Context, _ ...jetstream.StreamInfoOpt) (*jetstream.StreamInfo, error) {
	return &jetstream.StreamInfo{Config: s.cfg}, nil
}

func (s *fakeStream) CachedInfo() *jetstream.StreamInfo {
	return &jetstream.StreamInfo{Config: s.cfg}
}

func (s *fakeStream) Purge(_ context.Context, _ ...jetstream.StreamPurgeOpt) error {
	return nil
}

func (s *fakeStream) GetMsg(_ context.Context, _ uint64, _ ...jetstream.GetMsgOpt) (*jetstream.RawStreamMsg, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeStream) GetLastMsgForSubject(_ context.Context, _ string) (*jetstream.RawStreamMsg, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeStream) DeleteMsg(_ context.Context, _ uint64) error {
	return nil
}

func (s *fakeStream) SecureDeleteMsg(_ context.Context, _ uint64) error {
	return nil
}

// ConsumerManager methods (all stubs)

func (s *fakeStream) CreateOrUpdateConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeStream) CreateConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeStream) UpdateConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeStream) ResetConsumer(_ context.Context, _ string) (*jetstream.ConsumerResetResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeStream) ResetConsumerToSequence(_ context.Context, _ string, _ uint64) (*jetstream.ConsumerResetResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeStream) OrderedConsumer(_ context.Context, _ jetstream.OrderedConsumerConfig) (jetstream.Consumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeStream) Consumer(_ context.Context, _ string) (jetstream.Consumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeStream) DeleteConsumer(_ context.Context, _ string) error {
	return nil
}

func (s *fakeStream) PauseConsumer(_ context.Context, _ string, _ time.Time) (*jetstream.ConsumerPauseResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeStream) ResumeConsumer(_ context.Context, _ string) (*jetstream.ConsumerPauseResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeStream) ListConsumers(_ context.Context) jetstream.ConsumerInfoLister {
	return nil
}

func (s *fakeStream) ConsumerNames(_ context.Context) jetstream.ConsumerNameLister {
	return nil
}

func (s *fakeStream) UnpinConsumer(_ context.Context, _ string, _ string) error {
	return nil
}

func (s *fakeStream) CreateOrUpdatePushConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.PushConsumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeStream) CreatePushConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.PushConsumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeStream) UpdatePushConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.PushConsumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeStream) PushConsumer(_ context.Context, _ string) (jetstream.PushConsumer, error) {
	return nil, fmt.Errorf("not implemented")
}
