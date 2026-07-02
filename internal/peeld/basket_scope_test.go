package peeld

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"testing"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/bus/bustest"
	"github.com/ptorbus/zester/pkg/target"
)

// seedBasketTestData creates a FakeJS with four peels split across two
// clusters, each with facts and one basket entry.
func seedBasketTestData(t *testing.T) *bustest.FakeJS {
	t.Helper()
	ctx := context.Background()

	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatal(err)
	}

	factsKV := js.GetBucket(bus.BucketFacts)
	if factsKV == nil {
		t.Fatal("facts bucket not found")
	}
	basketKV := js.GetBucket(bus.BucketBasket)
	if basketKV == nil {
		t.Fatal("basket bucket not found")
	}

	peels := []struct {
		id, cluster, ip string
	}{
		{"web-01", "a", "10.0.1.1"},
		{"db-01", "a", "10.0.1.2"},
		{"web-02", "b", "10.0.2.1"},
		{"db-02", "b", "10.0.2.2"},
	}
	for _, p := range peels {
		if _, err := bus.KVPut(ctx, factsKV, p.id, map[string]any{"cluster": p.cluster}); err != nil {
			t.Fatal(err)
		}
		if _, err := bus.KVPut(ctx, basketKV, p.id+".default_ipv4", p.ip); err != nil {
			t.Fatal(err)
		}
	}
	return js
}

func basketPeelIDs(t *testing.T, results []map[string]any) []string {
	t.Helper()
	var ids []string
	for _, r := range results {
		id, ok := r["peel_id"].(string)
		if !ok {
			t.Fatalf("peel_id missing or not a string: %v", r)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMakeBasketFuncScopeComposition(t *testing.T) {
	js := seedBasketTestData(t)

	fn := makeBasketFunc(js, nil, discardLogger(), func() string { return "G@cluster:a" })

	// "web* or db*" matches all four peels. The scope must constrain BOTH
	// branches of the "or": the composed target has to be
	// "(web* or db*) and (G@cluster:a)". Without parenthesization, "and"
	// binds tighter than "or" and web-02 (cluster b) leaks through.
	results := fn("web* or db*", "default_ipv4")

	got := basketPeelIDs(t, results)
	want := []string{"db-01", "web-01"}
	if len(got) != len(want) {
		t.Fatalf("peels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("peels = %v, want %v", got, want)
		}
	}

	wantIPs := map[string]string{"web-01": "10.0.1.1", "db-01": "10.0.1.2"}
	for _, r := range results {
		id := r["peel_id"].(string)
		if r["value"] != wantIPs[id] {
			t.Errorf("value for %s = %v, want %q", id, r["value"], wantIPs[id])
		}
	}
}

func TestMakeBasketFuncNoScope(t *testing.T) {
	js := seedBasketTestData(t)

	fn := makeBasketFunc(js, nil, discardLogger(), func() string { return "" })

	results := fn("web* or db*", "default_ipv4")

	got := basketPeelIDs(t, results)
	want := []string{"db-01", "db-02", "web-01", "web-02"}
	if len(got) != len(want) {
		t.Fatalf("peels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("peels = %v, want %v", got, want)
		}
	}
}

// TestMakeBasketFuncUsesResolveService verifies that when a pub/sub is
// provided, target resolution is delegated to the master-side resolve
// service (C2 wiring) instead of scanning the facts bucket: the fake service
// answers with a single peel regardless of what the facts KV contains.
func TestMakeBasketFuncUsesResolveService(t *testing.T) {
	js := seedBasketTestData(t)
	ps := bustest.NewFakePubSub()

	_, err := ps.QueueSubscribe(bus.SubjectTargetResolve, target.DefaultResolveQueue, func(msg *bus.Msg) {
		data, err := bus.Encode(target.ResolveResponse{Peels: []string{"web-01"}})
		if err != nil {
			t.Errorf("encode resolve response: %v", err)
			return
		}
		if err := msg.Respond(data); err != nil {
			t.Errorf("respond: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("queue subscribe: %v", err)
	}

	fn := makeBasketFunc(js, ps, discardLogger(), func() string { return "" })

	// The KV scan would match all four peels; the service pins it to web-01.
	results := fn("web* or db*", "default_ipv4")

	got := basketPeelIDs(t, results)
	if len(got) != 1 || got[0] != "web-01" {
		t.Fatalf("peels = %v, want [web-01] from the resolve service", got)
	}
	if results[0]["value"] != "10.0.1.1" {
		t.Errorf("value = %v, want 10.0.1.1", results[0]["value"])
	}
}

// TestMakeBasketFuncServiceFallback verifies the automatic KV-scan fallback
// when nothing serves the resolve subject (mixed-version fleet / master
// without the service).
func TestMakeBasketFuncServiceFallback(t *testing.T) {
	js := seedBasketTestData(t)
	ps := bustest.NewFakePubSub() // no responder on the resolve subject

	fn := makeBasketFunc(js, ps, discardLogger(), func() string { return "" })

	results := fn("web* or db*", "default_ipv4")

	got := basketPeelIDs(t, results)
	want := []string{"db-01", "db-02", "web-01", "web-02"}
	if len(got) != len(want) {
		t.Fatalf("peels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("peels = %v, want %v", got, want)
		}
	}
}

func TestMakeBasketFuncScopeOnGlobTarget(t *testing.T) {
	js := seedBasketTestData(t)

	fn := makeBasketFunc(js, nil, discardLogger(), func() string { return "G@cluster:b" })

	results := fn("web*", "default_ipv4")

	got := basketPeelIDs(t, results)
	if len(got) != 1 || got[0] != "web-02" {
		t.Fatalf("peels = %v, want [web-02]", got)
	}
}
