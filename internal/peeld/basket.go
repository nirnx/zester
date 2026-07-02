package peeld

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/target"
	"github.com/ptorbus/zester/pkg/template"
)

// makeBasketFunc creates a template.BasketFunc that queries the basket KV bucket.
// It resolves target patterns via the master-side target-resolution service
// (IDs-only round trip, roadmap C2) with the facts KV bucket as automatic
// fallback, then reads matching basket entries for the requested function.
// The scopeFn callback returns the current basket_scope setting; when
// non-empty it is compound-ANDed with the caller's target so that queries are
// automatically narrowed (e.g. to a cluster). ps may be nil (tests, callers
// without a pub/sub), in which case resolution goes straight to the KV scan.
func makeBasketFunc(js bus.JetStreamAPI, ps bus.PubSub, logger *slog.Logger, scopeFn func() string) template.BasketFunc {
	return func(tgt, function string) []map[string]any {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Apply basket_scope if configured. Both sides are parenthesized:
		// "and" binds tighter than "or" in the compound grammar, so a bare
		// concatenation would let the left branch of "a or b" escape the
		// scope entirely.
		if scope := scopeFn(); scope != "" {
			tgt = "(" + tgt + ") and (" + scope + ")"
		}

		// Resolve target to peel IDs. Prefer the master-side resolve service:
		// target.Resolve delegates whole expressions to a ServiceLister, and
		// the ServiceLister falls back to the wrapped KV-scan lister on
		// timeout/no-responders, so mixed-version fleets keep working.
		tt := target.DetectType(tgt)
		var lister target.PeelLister = &peelFactsLister{js: js}
		if ps != nil {
			lister = target.NewServiceLister(ps, 5*time.Second, lister, logger)
		}
		peels, err := target.Resolve(ctx, tgt, tt, lister)
		if err != nil {
			logger.Warn("basket: resolve target", "target", tgt, "error", err)
			return nil
		}

		basketKV, err := bus.GetBucket(ctx, js, bus.BucketBasket)
		if err != nil {
			logger.Warn("basket: get basket bucket", "error", err)
			return nil
		}

		var results []map[string]any
		for _, peelID := range peels {
			key := peelID + "." + function
			var val any
			if err := bus.KVGet(ctx, basketKV, key, &val); err != nil {
				continue
			}
			results = append(results, map[string]any{
				"peel_id": peelID,
				"value":   val,
			})
		}
		return results
	}
}

// peelFactsLister implements target.PeelLister using the facts KV bucket.
// The bucket handle is resolved on first successful use and cached; a
// transient lookup failure is retried on the next call rather than being
// cached forever.
type peelFactsLister struct {
	js     bus.JetStreamAPI
	mu     sync.Mutex
	bucket bus.KV
}

func (l *peelFactsLister) factsBucket(ctx context.Context) (bus.KV, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.bucket != nil {
		return l.bucket, nil
	}
	kv, err := bus.GetBucket(ctx, l.js, bus.BucketFacts)
	if err != nil {
		return nil, err
	}
	l.bucket = kv
	return kv, nil
}

func (l *peelFactsLister) ListPeels(ctx context.Context) ([]string, error) {
	kv, err := l.factsBucket(ctx)
	if err != nil {
		return nil, err
	}
	return kv.Keys(ctx)
}

func (l *peelFactsLister) GetFacts(ctx context.Context, peelID string) (map[string]any, error) {
	kv, err := l.factsBucket(ctx)
	if err != nil {
		return nil, err
	}
	var f map[string]any
	if err := bus.KVGet(ctx, kv, peelID, &f); err != nil {
		return nil, err
	}
	return f, nil
}

func (l *peelFactsLister) ListPeelsWithFacts(ctx context.Context) (map[string]map[string]any, error) {
	kv, err := l.factsBucket(ctx)
	if err != nil {
		return nil, err
	}
	return bus.KVGetAll[map[string]any](ctx, kv)
}
