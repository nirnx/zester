package target

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus/bustest"
	"github.com/ptorbus/zester/pkg/facts"
)

// testIndex builds a facts.Index with three peels.
func testIndex() *facts.Index {
	idx := facts.NewIndex()
	idx.Update("web-01", facts.Facts{
		"os":      map[string]any{"name": "linux", "arch": "amd64"},
		"network": map[string]any{"hostname": "web-01"},
	})
	idx.Update("web-02", facts.Facts{
		"os":      map[string]any{"name": "linux", "arch": "arm64"},
		"network": map[string]any{"hostname": "web-02"},
	})
	idx.Update("db-01", facts.Facts{
		"os":      map[string]any{"name": "freebsd", "arch": "amd64"},
		"network": map[string]any{"hostname": "db-01"},
	})
	return idx
}

// staticLister is a local fallback PeelLister used to verify fallback behavior.
type staticLister struct {
	peels map[string]map[string]any
	calls int
}

func (l *staticLister) ListPeels(_ context.Context) ([]string, error) {
	l.calls++
	ids := make([]string, 0, len(l.peels))
	for id := range l.peels {
		ids = append(ids, id)
	}
	return ids, nil
}

func (l *staticLister) GetFacts(_ context.Context, peelID string) (map[string]any, error) {
	l.calls++
	f, ok := l.peels[peelID]
	if !ok {
		return nil, fmt.Errorf("no facts for %q", peelID)
	}
	return f, nil
}

func TestResolveServiceRoundTrip(t *testing.T) {
	ps := bustest.NewFakePubSub()
	idx := testIndex()

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	cancel, err := StartResolveService(ctx, ps, "", IndexResolveFunc(idx), slog.Default())
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	defer cancel()

	lister := NewServiceLister(ps, time.Second, nil, slog.Default())

	tests := []struct {
		expr string
		tt   TargetType
		want []string
	}{
		{"web-*", Glob, []string{"web-01", "web-02"}},
		{"G@os.name:linux", Fact, []string{"web-01", "web-02"}},
		{"G@os.name:freebsd", Fact, []string{"db-01"}},
		{"web-* and G@os.arch:amd64", Compound, []string{"web-01"}},
		{"L@db-01,web-02", List, []string{"db-01", "web-02"}},
		{"E@web-0[12]", PCRE, []string{"web-01", "web-02"}},
	}

	for _, tc := range tests {
		got, err := Resolve(ctx, tc.expr, tc.tt, lister)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", tc.expr, err)
		}
		if !sameElements(got, tc.want) {
			t.Errorf("Resolve(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}
}

func TestResolveServiceInvalidExpression(t *testing.T) {
	ps := bustest.NewFakePubSub()
	idx := testIndex()

	cancel, err := StartResolveService(context.Background(), ps, "", IndexResolveFunc(idx), slog.Default())
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	defer cancel()

	// No fallback: the remote error must be returned, not swallowed.
	lister := NewServiceLister(ps, time.Second, nil, slog.Default())
	if _, err := Resolve(context.Background(), "G@missing-colon", Fact, lister); err == nil {
		t.Fatal("expected error for invalid fact expression")
	}
}

func TestServiceListerFallbackOnNoResponders(t *testing.T) {
	ps := bustest.NewFakePubSub() // nothing subscribed: every request is a no-responder

	fallback := &staticLister{peels: map[string]map[string]any{
		"web-01": {"os": map[string]any{"name": "linux"}},
		"db-01":  {"os": map[string]any{"name": "freebsd"}},
	}}
	lister := NewServiceLister(ps, 100*time.Millisecond, fallback, slog.Default())

	got, err := Resolve(context.Background(), "G@os.name:linux", Fact, lister)
	if err != nil {
		t.Fatalf("Resolve with fallback: %v", err)
	}
	if !sameElements(got, []string{"web-01"}) {
		t.Fatalf("Resolve = %v, want [web-01]", got)
	}
	if fallback.calls == 0 {
		t.Fatal("fallback lister was not consulted")
	}

	// ListPeels and GetFacts also fall back.
	ids, err := lister.ListPeels(context.Background())
	if err != nil {
		t.Fatalf("ListPeels fallback: %v", err)
	}
	if !sameElements(ids, []string{"web-01", "db-01"}) {
		t.Fatalf("ListPeels = %v", ids)
	}
	f, err := lister.GetFacts(context.Background(), "db-01")
	if err != nil {
		t.Fatalf("GetFacts fallback: %v", err)
	}
	if osMap, _ := f["os"].(map[string]any); osMap["name"] != "freebsd" {
		t.Fatalf("GetFacts = %v", f)
	}

	// ListPeelsWithFacts falls back via the N+1 path (staticLister is not bulk).
	all, err := lister.ListPeelsWithFacts(context.Background())
	if err != nil {
		t.Fatalf("ListPeelsWithFacts fallback: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListPeelsWithFacts len = %d, want 2", len(all))
	}
}

func TestServiceListerNoFallbackReturnsError(t *testing.T) {
	ps := bustest.NewFakePubSub()
	lister := NewServiceLister(ps, 100*time.Millisecond, nil, slog.Default())

	if _, err := Resolve(context.Background(), "web-*", Glob, lister); err == nil {
		t.Fatal("expected error with no service and no fallback")
	}
	if _, err := lister.ListPeels(context.Background()); err == nil {
		t.Fatal("expected ListPeels error with no service and no fallback")
	}
}

func TestServiceListerFallbackOnRemoteError(t *testing.T) {
	ps := bustest.NewFakePubSub()

	cancel, err := StartResolveService(context.Background(), ps, "",
		func(ctx context.Context, expr, targetType string) ([]string, map[string]map[string]any, error) {
			return nil, nil, fmt.Errorf("index not ready")
		}, slog.Default())
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	defer cancel()

	fallback := &staticLister{peels: map[string]map[string]any{
		"web-01": {"os": map[string]any{"name": "linux"}},
	}}
	lister := NewServiceLister(ps, time.Second, fallback, slog.Default())

	got, err := Resolve(context.Background(), "web-*", Glob, lister)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !sameElements(got, []string{"web-01"}) {
		t.Fatalf("Resolve = %v, want [web-01]", got)
	}
	if fallback.calls == 0 {
		t.Fatal("fallback lister was not consulted after remote error")
	}
}

func TestServiceListerGetFactsAndBulk(t *testing.T) {
	ps := bustest.NewFakePubSub()
	idx := testIndex()

	cancel, err := StartResolveService(context.Background(), ps, "", IndexResolveFunc(idx), slog.Default())
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	defer cancel()

	lister := NewServiceLister(ps, time.Second, nil, slog.Default())

	f, err := lister.GetFacts(context.Background(), "web-02")
	if err != nil {
		t.Fatalf("GetFacts: %v", err)
	}
	osMap, ok := f["os"].(map[string]any)
	if !ok || osMap["arch"] != "arm64" {
		t.Fatalf("GetFacts(web-02) os = %v", f["os"])
	}

	if _, err := lister.GetFacts(context.Background(), "nope"); err == nil {
		t.Fatal("expected error for unknown peel")
	}

	ids, err := lister.ListPeels(context.Background())
	if err != nil {
		t.Fatalf("ListPeels: %v", err)
	}
	if !sameElements(ids, []string{"web-01", "web-02", "db-01"}) {
		t.Fatalf("ListPeels = %v", ids)
	}

	all, err := lister.ListPeelsWithFacts(context.Background())
	if err != nil {
		t.Fatalf("ListPeelsWithFacts: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListPeelsWithFacts len = %d, want 3", len(all))
	}
}

func TestServiceBasketQuery(t *testing.T) {
	ps := bustest.NewFakePubSub()
	idx := testIndex()

	cancel, err := StartResolveService(context.Background(), ps, "", IndexResolveFunc(idx), slog.Default())
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	defer cancel()

	// Compound expression as produced by makeBasketFunc with a basket_scope.
	got, err := ServiceBasketQuery(context.Background(), ps,
		"(web-*) and (G@os.arch:amd64)", time.Second)
	if err != nil {
		t.Fatalf("ServiceBasketQuery: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("matched %d peels, want 1: %v", len(got), got)
	}
	f, ok := got["web-01"]
	if !ok {
		t.Fatalf("web-01 missing from result: %v", got)
	}
	osMap, _ := f["os"].(map[string]any)
	if osMap["name"] != "linux" {
		t.Fatalf("web-01 facts = %v", f)
	}

	// No responders: error, so the peel can fall back to its KV-scan path.
	empty := bustest.NewFakePubSub()
	if _, err := ServiceBasketQuery(context.Background(), empty, "web-*", 100*time.Millisecond); err == nil {
		t.Fatal("expected error when no master serves the resolve subject")
	}
}

func TestResolveServiceQueueGroupSingleHandler(t *testing.T) {
	ps := bustest.NewFakePubSub()
	idx := testIndex()

	// Two masters in the same queue group: each request handled exactly once.
	handled := make(chan string, 4)
	mkResolve := func(name string) ResolveFunc {
		inner := IndexResolveFunc(idx)
		return func(ctx context.Context, expr, targetType string) ([]string, map[string]map[string]any, error) {
			handled <- name
			return inner(ctx, expr, targetType)
		}
	}

	c1, err := StartResolveService(context.Background(), ps, "", mkResolve("m1"), slog.Default())
	if err != nil {
		t.Fatalf("start m1: %v", err)
	}
	defer c1()
	c2, err := StartResolveService(context.Background(), ps, "", mkResolve("m2"), slog.Default())
	if err != nil {
		t.Fatalf("start m2: %v", err)
	}
	defer c2()

	lister := NewServiceLister(ps, time.Second, nil, slog.Default())
	if _, err := Resolve(context.Background(), "web-*", Glob, lister); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := len(handled); got != 1 {
		t.Fatalf("request handled by %d masters, want 1", got)
	}
}

func TestIndexLister(t *testing.T) {
	idx := testIndex()
	lister := NewIndexLister(idx)
	ctx := context.Background()

	ids, err := lister.ListPeels(ctx)
	if err != nil {
		t.Fatalf("ListPeels: %v", err)
	}
	if !sameElements(ids, []string{"web-01", "web-02", "db-01"}) {
		t.Fatalf("ListPeels = %v", ids)
	}

	f, err := lister.GetFacts(ctx, "db-01")
	if err != nil {
		t.Fatalf("GetFacts: %v", err)
	}
	osMap, _ := f["os"].(map[string]any)
	if osMap["name"] != "freebsd" {
		t.Fatalf("GetFacts(db-01) = %v", f)
	}

	if _, err := lister.GetFacts(ctx, "missing"); err == nil {
		t.Fatal("expected error for missing peel")
	}

	all, err := lister.ListPeelsWithFacts(ctx)
	if err != nil {
		t.Fatalf("ListPeelsWithFacts: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListPeelsWithFacts len = %d", len(all))
	}

	// Resolve over the IndexLister (the master-side pipeline) with a
	// compound expression, delegating to the existing matchers.
	got, err := Resolve(ctx, "G@os.name:linux and not web-02", Compound, lister)
	if err != nil {
		t.Fatalf("Resolve over index: %v", err)
	}
	if !sameElements(got, []string{"web-01"}) {
		t.Fatalf("Resolve = %v, want [web-01]", got)
	}
}

func TestParseType(t *testing.T) {
	for _, tt := range []TargetType{Glob, PCRE, Fact, Settings, Compound, List} {
		got, err := ParseType(tt.String())
		if err != nil {
			t.Fatalf("ParseType(%q): %v", tt.String(), err)
		}
		if got != tt {
			t.Fatalf("ParseType(%q) = %v, want %v", tt.String(), got, tt)
		}
	}
	if _, err := ParseType("bogus"); err == nil {
		t.Fatal("expected error for unknown type")
	}
	if _, err := ParseType(""); err == nil {
		t.Fatal("expected error for empty type")
	}
}

// sameElements reports whether a and b contain the same strings, ignoring order.
func sameElements(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]int, len(a))
	for _, s := range a {
		set[s]++
	}
	for _, s := range b {
		set[s]--
	}
	for _, n := range set {
		if n != 0 {
			return false
		}
	}
	return true
}
