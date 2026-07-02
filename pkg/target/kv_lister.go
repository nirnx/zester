package target

import (
	"context"
	"fmt"
	"sync"

	"github.com/ptorbus/zester/pkg/bus"
)

// KVPeelLister implements PeelLister by querying the facts KV bucket.
// The facts bucket handle is resolved once on first use and cached.
type KVPeelLister struct {
	JS bus.JetStreamAPI

	once      sync.Once
	bucket    bus.KV
	bucketErr error
}

func (l *KVPeelLister) factsBucket(ctx context.Context) (bus.KV, error) {
	l.once.Do(func() {
		l.bucket, l.bucketErr = bus.GetBucket(ctx, l.JS, bus.BucketFacts)
	})
	return l.bucket, l.bucketErr
}

// ListPeels returns all peel IDs known in the facts bucket.
func (l *KVPeelLister) ListPeels(ctx context.Context) ([]string, error) {
	kv, err := l.factsBucket(ctx)
	if err != nil {
		return nil, fmt.Errorf("get facts bucket: %w", err)
	}

	lister, err := kv.ListKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list fact keys: %w", err)
	}

	var ids []string
	for key := range lister.Keys() {
		ids = append(ids, key)
	}
	return ids, nil
}

// GetFacts returns facts for a peel ID from the facts KV bucket.
func (l *KVPeelLister) GetFacts(ctx context.Context, peelID string) (map[string]any, error) {
	kv, err := l.factsBucket(ctx)
	if err != nil {
		return nil, fmt.Errorf("get facts bucket: %w", err)
	}

	var facts map[string]any
	if err := bus.KVGet(ctx, kv, peelID, &facts); err != nil {
		return nil, err
	}
	return facts, nil
}

// ListPeelsWithFacts bulk-loads all facts in one call.
func (l *KVPeelLister) ListPeelsWithFacts(ctx context.Context) (map[string]map[string]any, error) {
	kv, err := l.factsBucket(ctx)
	if err != nil {
		return nil, fmt.Errorf("get facts bucket: %w", err)
	}
	return bus.KVGetAll[map[string]any](ctx, kv)
}
