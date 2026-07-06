package masterd

import (
	"context"
	"slices"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/target"
	"github.com/nirnx/zester/pkg/update"
)

func TestRolloutTargetLister_Resolve(t *testing.T) {
	ctx := context.Background()
	js := bustest.NewFakeJS()

	factsKV, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bus.BucketFacts})
	if err != nil {
		t.Fatalf("create facts bucket: %v", err)
	}

	// Facts known in KV.
	putFacts(t, ctx, factsKV, "web-01", map[string]any{"os": "ubuntu", "env": "prod"})
	putFacts(t, ctx, factsKV, "web-02", map[string]any{"os": "ubuntu", "env": "staging"})
	putFacts(t, ctx, factsKV, "web-03", map[string]any{"os": "ubuntu", "env": "prod"}) // not reporting
	putFacts(t, ctx, factsKV, "db-01", map[string]any{"os": "debian", "env": "prod"})

	// Only these nodes are currently in rollout scope (reporting status).
	statuses := []*update.NodeStatus{
		{ID: "web-01", Component: "peel"},
		{ID: "web-02", Component: "peel"},
		{ID: "db-01", Component: "peel"},
		{ID: "web-missing-facts", Component: "peel"},
	}

	lister := newRolloutTargetLister(js, statuses)

	tests := []struct {
		name string
		expr string
		want []string
	}{
		{
			name: "glob targets only reporting nodes",
			expr: "web*",
			want: []string{"web-01", "web-02", "web-missing-facts"},
		},
		{
			name: "fact targeting intersects reporting scope and facts",
			expr: "G@env:prod",
			want: []string{"web-01", "db-01"},
		},
		{
			name: "compound targeting works for rollout scope",
			expr: "web* and G@os:ubuntu and not L@web-02",
			want: []string{"web-01"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := target.Resolve(ctx, tt.expr, target.DetectType(tt.expr), lister)
			if err != nil {
				t.Fatalf("resolve %q: %v", tt.expr, err)
			}
			assertSameSet(t, got, tt.want)
		})
	}
}

func putFacts(t *testing.T, ctx context.Context, kv bus.KV, peelID string, facts map[string]any) {
	t.Helper()
	if _, err := bus.KVPut(ctx, kv, peelID, facts); err != nil {
		t.Fatalf("put facts for %s: %v", peelID, err)
	}
}

func assertSameSet(t *testing.T, got, want []string) {
	t.Helper()
	gotCopy := append([]string(nil), got...)
	wantCopy := append([]string(nil), want...)
	slices.Sort(gotCopy)
	slices.Sort(wantCopy)
	if !slices.Equal(gotCopy, wantCopy) {
		t.Fatalf("set mismatch:\n  got:  %v\n  want: %v", gotCopy, wantCopy)
	}
}
