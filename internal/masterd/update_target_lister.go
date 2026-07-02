package masterd

import (
	"context"
	"fmt"
	"sync"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/update"
)

// rolloutTargetLister implements target.PeelLister against the set of nodes
// currently reporting in update-status, with facts loaded from the facts bucket.
type rolloutTargetLister struct {
	js bus.JetStreamAPI

	ids map[string]struct{}

	once       sync.Once
	factsKV    bus.KV
	factsKVErr error
}

func newRolloutTargetLister(js bus.JetStreamAPI, statuses []*update.NodeStatus) *rolloutTargetLister {
	ids := make(map[string]struct{}, len(statuses))
	for _, s := range statuses {
		ids[s.ID] = struct{}{}
	}
	return &rolloutTargetLister{
		js:  js,
		ids: ids,
	}
}

func (l *rolloutTargetLister) getFactsKV(ctx context.Context) (bus.KV, error) {
	l.once.Do(func() {
		l.factsKV, l.factsKVErr = bus.GetBucket(ctx, l.js, bus.BucketFacts)
	})
	return l.factsKV, l.factsKVErr
}

func (l *rolloutTargetLister) ListPeels(_ context.Context) ([]string, error) {
	out := make([]string, 0, len(l.ids))
	for id := range l.ids {
		out = append(out, id)
	}
	return out, nil
}

func (l *rolloutTargetLister) GetFacts(ctx context.Context, peelID string) (map[string]any, error) {
	if _, ok := l.ids[peelID]; !ok {
		return nil, fmt.Errorf("unknown peel id %q", peelID)
	}

	kv, err := l.getFactsKV(ctx)
	if err != nil {
		return nil, err
	}

	var facts map[string]any
	if err := bus.KVGet(ctx, kv, peelID, &facts); err != nil {
		return nil, err
	}
	return facts, nil
}

func (l *rolloutTargetLister) ListPeelsWithFacts(ctx context.Context) (map[string]map[string]any, error) {
	kv, err := l.getFactsKV(ctx)
	if err != nil {
		return nil, err
	}

	allFacts, err := bus.KVGetAll[map[string]any](ctx, kv)
	if err != nil {
		return nil, err
	}

	filtered := make(map[string]map[string]any, len(l.ids))
	for id := range l.ids {
		if facts, ok := allFacts[id]; ok {
			filtered[id] = facts
		}
	}
	return filtered, nil
}
