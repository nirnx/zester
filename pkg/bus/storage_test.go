package bus_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/bus/bustest"
)

func TestEffectiveReplicas(t *testing.T) {
	tests := []struct {
		explicit, clusterSize, want int
	}{
		{0, 0, 1}, // unknown cluster: safe default
		{0, 1, 1},
		{0, 2, 2},
		{0, 3, 3},
		{0, 5, 3}, // capped at 3
		{1, 3, 1}, // explicit wins
		{2, 5, 2},
		{5, 1, 5}, // explicit wins even above cluster size
	}
	for _, tt := range tests {
		if got := bus.EffectiveReplicas(tt.explicit, tt.clusterSize); got != tt.want {
			t.Errorf("EffectiveReplicas(%d, %d) = %d, want %d", tt.explicit, tt.clusterSize, got, tt.want)
		}
	}
}

func TestInitializeStorageOpts_ScalesWithClusterSize(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	err := bus.InitializeStorageOpts(ctx, js, bus.StorageOptions{ClusterSize: 3})
	if err != nil {
		t.Fatalf("initialize storage: %v", err)
	}

	// All tiers get the effective count: critical for durability, ephemeral
	// so heartbeat/lease buckets survive a node loss.
	for _, bucket := range []string{bus.BucketJobs, bus.BucketSecrets, bus.BucketMasterHeartbeat, bus.BucketLeases} {
		if got := js.kvConfig(bucket).Replicas; got != 3 {
			t.Errorf("bucket %q: replicas = %d, want 3", bucket, got)
		}
	}
	if got := js.streamConfig(bus.StreamJobEvents).Replicas; got != 3 {
		t.Errorf("stream %q: replicas = %d, want 3", bus.StreamJobEvents, got)
	}
}

func TestInitializeStorageOpts_ExplicitReplicasWins(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	err := bus.InitializeStorageOpts(ctx, js, bus.StorageOptions{Replicas: 2, ClusterSize: 5})
	if err != nil {
		t.Fatalf("initialize storage: %v", err)
	}

	for _, bucket := range []string{bus.BucketJobs, bus.BucketMasterHeartbeat} {
		if got := js.kvConfig(bucket).Replicas; got != 2 {
			t.Errorf("bucket %q: replicas = %d, want 2", bucket, got)
		}
	}
	if got := js.streamConfig(bus.StreamJobEvents).Replicas; got != 2 {
		t.Errorf("stream %q: replicas = %d, want 2", bus.StreamJobEvents, got)
	}
}

func TestInitializeStorageOpts_WarnsOnForcedLowCriticalReplicas(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	// Operator explicitly forces R1 in a 3-node cluster.
	err := bus.InitializeStorageOpts(ctx, js, bus.StorageOptions{Replicas: 1, ClusterSize: 3, Logger: logger})
	if err != nil {
		t.Fatalf("initialize storage: %v", err)
	}

	logs := buf.String()
	for _, critical := range []string{bus.BucketJobs, bus.BucketSecrets, bus.BucketEnrollments, bus.StreamJobEvents} {
		if !strings.Contains(logs, "="+critical) {
			t.Errorf("expected under-replication warning for critical asset %q, logs:\n%s", critical, logs)
		}
	}
	for _, ephemeral := range []string{bus.BucketMasterHeartbeat, bus.BucketEnrollChallenges, bus.BucketPeelHeartbeat, bus.BucketLeases} {
		if strings.Contains(logs, "="+ephemeral) {
			t.Errorf("unexpected warning for ephemeral bucket %q, logs:\n%s", ephemeral, logs)
		}
	}
}

func TestInitializeStorageOpts_NoWarningsAtFullReplication(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	err := bus.InitializeStorageOpts(ctx, js, bus.StorageOptions{ClusterSize: 3, Logger: logger})
	if err != nil {
		t.Fatalf("initialize storage: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no warnings, got:\n%s", buf.String())
	}
}

// replicaRejectingJS simulates a server that cannot honor replicas > 1
// (single-node deployment, older server) so the fallback path is exercised.
type replicaRejectingJS struct {
	*testJS
}

func (j *replicaRejectingJS) CreateOrUpdateKeyValue(ctx context.Context, cfg jetstream.KeyValueConfig) (bus.KV, error) {
	if cfg.Replicas > 1 {
		return nil, errors.New("insufficient resources")
	}
	return j.testJS.CreateOrUpdateKeyValue(ctx, cfg)
}

func (j *replicaRejectingJS) CreateStream(ctx context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error) {
	if cfg.Replicas > 1 {
		return nil, errors.New("insufficient resources")
	}
	return j.testJS.CreateStream(ctx, cfg)
}

func TestInitializeStorageOpts_FallbackNeverBlocksStartup(t *testing.T) {
	inner := newTestJS()
	js := &replicaRejectingJS{testJS: inner}
	ctx := context.Background()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	err := bus.InitializeStorageOpts(ctx, js, bus.StorageOptions{ClusterSize: 3, Logger: logger})
	if err != nil {
		t.Fatalf("replica upgrade failure must not abort startup: %v", err)
	}

	// Every asset must still exist, at the previous default of 1 replica.
	for _, bucket := range []string{bus.BucketJobs, bus.BucketMasterHeartbeat} {
		if _, err := bus.GetBucket(ctx, js, bucket); err != nil {
			t.Errorf("bucket %q missing after fallback: %v", bucket, err)
		}
		if got := inner.kvConfig(bucket).Replicas; got != 1 {
			t.Errorf("bucket %q: replicas = %d, want fallback 1", bucket, got)
		}
	}
	if got := inner.streamConfig(bus.StreamJobEvents).Replicas; got != 1 {
		t.Errorf("stream %q: replicas = %d, want fallback 1", bus.StreamJobEvents, got)
	}

	logs := buf.String()
	if !strings.Contains(logs, "migrate manually") || !strings.Contains(logs, "="+bus.BucketJobs) {
		t.Errorf("expected fallback migration warning naming buckets, logs:\n%s", logs)
	}
}

// fakeObjectStoreAPI implements bus.ObjectStoreAPI for replica assertions.
type fakeObjectStoreAPI struct {
	configs      []jetstream.ObjectStoreConfig
	failAboveOne bool
}

func (f *fakeObjectStoreAPI) CreateOrUpdateObjectStore(_ context.Context, cfg jetstream.ObjectStoreConfig) (jetstream.ObjectStore, error) {
	if f.failAboveOne && cfg.Replicas > 1 {
		return nil, errors.New("insufficient resources")
	}
	f.configs = append(f.configs, cfg)
	return nil, nil
}

func (f *fakeObjectStoreAPI) ObjectStore(_ context.Context, bucket string) (jetstream.ObjectStore, error) {
	return nil, fmt.Errorf("object store not found: %s", bucket)
}

func (f *fakeObjectStoreAPI) DeleteObjectStore(_ context.Context, _ string) error { return nil }

func TestInitializeObjectStoresOpts(t *testing.T) {
	ctx := context.Background()

	t.Run("scales with cluster size", func(t *testing.T) {
		js := &fakeObjectStoreAPI{}
		if err := bus.InitializeObjectStoresOpts(ctx, js, bus.StorageOptions{ClusterSize: 5}); err != nil {
			t.Fatalf("initialize object stores: %v", err)
		}
		if len(js.configs) != 1 || js.configs[0].Replicas != 3 {
			t.Errorf("got configs %+v, want one config with replicas 3", js.configs)
		}
	})

	t.Run("fallback on replica failure", func(t *testing.T) {
		js := &fakeObjectStoreAPI{failAboveOne: true}
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))

		err := bus.InitializeObjectStoresOpts(ctx, js, bus.StorageOptions{ClusterSize: 3, Logger: logger})
		if err != nil {
			t.Fatalf("replica failure must not abort startup: %v", err)
		}
		if len(js.configs) != 1 || js.configs[0].Replicas != 1 {
			t.Errorf("got configs %+v, want one config with fallback replicas 1", js.configs)
		}
		if !strings.Contains(buf.String(), "migrate manually") {
			t.Errorf("expected migration warning, logs:\n%s", buf.String())
		}
	})
}

func TestListKeysWithPrefix(t *testing.T) {
	ctx := context.Background()
	kv := bustest.NewFakeKV("test-prefix", 0)

	for _, k := range []string{"jid1", "jid1.peelA", "jid1.peelB", "jid2.peelA", "active.j1", "active.j2"} {
		if _, err := kv.Put(ctx, k, []byte("x")); err != nil {
			t.Fatalf("put %q: %v", k, err)
		}
	}

	t.Run("parent token lists children only", func(t *testing.T) {
		keys, err := bus.ListKeysWithPrefix(ctx, kv, "jid1")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		want := []string{"jid1.peelA", "jid1.peelB"}
		if !reflect.DeepEqual(keys, want) {
			t.Errorf("got %v, want %v (bare key jid1 must be excluded)", keys, want)
		}
	})

	t.Run("explicit wildcard form", func(t *testing.T) {
		keys, err := bus.ListKeysWithPrefix(ctx, kv, "active.>")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		want := []string{"active.j1", "active.j2"}
		if !reflect.DeepEqual(keys, want) {
			t.Errorf("got %v, want %v", keys, want)
		}
	})

	t.Run("no matches is not an error", func(t *testing.T) {
		keys, err := bus.ListKeysWithPrefix(ctx, kv, "nope")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if keys != nil {
			t.Errorf("got %v, want nil", keys)
		}
	})

	t.Run("deleted keys excluded", func(t *testing.T) {
		if err := kv.Delete(ctx, "jid1.peelB"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		keys, err := bus.ListKeysWithPrefix(ctx, kv, "jid1")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		want := []string{"jid1.peelA"}
		if !reflect.DeepEqual(keys, want) {
			t.Errorf("got %v, want %v", keys, want)
		}
	})
}
