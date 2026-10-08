package bus_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/nirnx/zester/pkg/bus"
)

// barrierJS holds every asset-creation call until ALL expected calls are in
// flight. A sequential initializer never gets there (it deadlocks and fails
// via the context timeout), so passing proves the assets are created
// concurrently — what keeps a fresh clustered master startup to one RAFT
// leader-election window instead of one per asset.
type barrierJS struct {
	bus.JetStreamAPI
	expected int32
	arrived  atomic.Int32
	release  chan struct{}
	once     sync.Once
}

func (b *barrierJS) wait(ctx context.Context) error {
	if b.arrived.Add(1) == b.expected {
		b.once.Do(func() { close(b.release) })
	}
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *barrierJS) CreateOrUpdateKeyValue(ctx context.Context, cfg jetstream.KeyValueConfig) (bus.KV, error) {
	if err := b.wait(ctx); err != nil {
		return nil, err
	}
	return b.JetStreamAPI.CreateOrUpdateKeyValue(ctx, cfg)
}

func (b *barrierJS) CreateStream(ctx context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error) {
	if err := b.wait(ctx); err != nil {
		return nil, err
	}
	return b.JetStreamAPI.CreateStream(ctx, cfg)
}

func TestInitializeStorageOpts_CreatesAssetsConcurrently(t *testing.T) {
	expected := len(bus.DefaultBuckets()) + len(bus.DefaultStreams())
	js := &barrierJS{JetStreamAPI: newTestJS(), expected: int32(expected), release: make(chan struct{})}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := bus.InitializeStorageOpts(ctx, js, bus.StorageOptions{ClusterSize: 1}); err != nil {
		t.Fatalf("initialize storage: %v (a sequential initializer deadlocks on the barrier)", err)
	}
	if got := js.arrived.Load(); got != int32(expected) {
		t.Errorf("creation calls = %d, want %d (every bucket and stream exactly once)", got, expected)
	}
}

// failingJS fails every creation so the joined error must name each asset.
type failingJS struct {
	bus.JetStreamAPI
}

func (failingJS) CreateOrUpdateKeyValue(context.Context, jetstream.KeyValueConfig) (bus.KV, error) {
	return nil, context.DeadlineExceeded
}

func (failingJS) CreateStream(context.Context, jetstream.StreamConfig) (jetstream.Stream, error) {
	return nil, context.DeadlineExceeded
}

func TestInitializeStorageOpts_ReportsEveryFailedAsset(t *testing.T) {
	err := bus.InitializeStorageOpts(context.Background(), failingJS{newTestJS()}, bus.StorageOptions{ClusterSize: 1})
	if err == nil {
		t.Fatal("expected an error when every creation fails")
	}
	for _, cfg := range bus.DefaultBuckets() {
		if !strings.Contains(err.Error(), `"`+cfg.Bucket+`"`) {
			t.Errorf("joined error should name bucket %q:\n%v", cfg.Bucket, err)
		}
	}
}
