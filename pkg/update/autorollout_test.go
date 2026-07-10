package update

import (
	"context"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
)

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"0.4.2", "0.4.2", 0},
		{"0.4.10", "0.4.2", 1},
		{"0.4.2", "0.5.0", -1},
		{"v1.0.0", "1.0.0", 0},
		{"1.0", "1.0.1", -1},
		{"0.4.2", "0.4.2-rc1", -1}, // longer tail sorts higher only lexically; plain releases first
	} {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestManifestExpiry(t *testing.T) {
	now := time.Now()
	legacy := &Manifest{Published: now.Add(-31 * 24 * time.Hour)} // pre-TTL manifest
	if !legacy.Expired(now) {
		t.Error("legacy manifest older than DefaultBinaryTTL must be expired")
	}
	pinned := &Manifest{Published: now, ExpiresAtUnix: now.Add(time.Hour).Unix()}
	if pinned.Expired(now) {
		t.Error("explicit future expiry must not be expired")
	}
	never := &Manifest{Published: now.Add(-365 * 24 * time.Hour), ExpiresAtUnix: TTLNever}
	if never.Expired(now) {
		t.Error("TTLNever must not expire")
	}
	promoted := &Manifest{Published: now.Add(-365 * 24 * time.Hour), Promoted: true,
		ExpiresAtUnix: now.Add(-time.Hour).Unix()} // stale expiry is IGNORED while promoted
	if promoted.Expired(now) {
		t.Error("promoted manifests never expire, regardless of expires_at")
	}
}

func TestAutoSwitchDefaultsOn(t *testing.T) {
	kv := bustest.NewFakeKV("update-manifests", 0)
	ctx := context.Background()

	s, err := LoadAutoSwitch(ctx, kv)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Enabled {
		t.Fatal("absent switch must read as ENABLED (default on)")
	}

	if err := SaveAutoSwitch(ctx, kv, AutoSwitch{Enabled: false, UpdatedBy: "op"}); err != nil {
		t.Fatal(err)
	}
	s, err = LoadAutoSwitch(ctx, kv)
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled {
		t.Fatal("explicit off must read as disabled")
	}

	// The switch key must never surface as a phantom manifest.
	store := NewManifestStore(kv)
	all, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("meta key leaked into manifest listing: %+v", all[0])
	}
	byComp, err := store.ListByComponent(ctx, "_auto-rollout")
	if err != nil {
		t.Fatal(err)
	}
	if len(byComp) != 0 {
		t.Fatal("meta key leaked into component listing")
	}
}

func autoManifests(promotedVersions ...string) []*Manifest {
	var out []*Manifest
	for _, v := range promotedVersions {
		out = append(out, &Manifest{Component: "peel", Version: v, Promoted: true})
	}
	return out
}

func TestPickAutoRollout(t *testing.T) {
	statuses := []*NodeStatus{
		{ID: "web-01", Component: "peel", Version: "0.4.2"},
		{ID: "web-02", Component: "peel", Version: "0.5.0"},
		{ID: "db-01", Component: "peel", Version: "0.4.1", Degraded: true},
	}

	// Nothing promoted: no plan even with lagging nodes.
	if p := PickAutoRollout("peel", []*Manifest{{Component: "peel", Version: "9.9.9"}}, statuses, nil); p != nil {
		t.Fatalf("unpromoted versions must never auto-roll, got %+v", p)
	}

	// Latest promoted wins; only lagging, non-degraded nodes are targeted;
	// nodes at/above the target are never touched.
	p := PickAutoRollout("peel", autoManifests("0.4.9", "0.5.0"), statuses, nil)
	if p == nil {
		t.Fatal("expected a plan")
	}
	if p.Version != "0.5.0" {
		t.Fatalf("version = %s, want 0.5.0 (latest promoted)", p.Version)
	}
	if len(p.NodeIDs) != 1 || p.NodeIDs[0] != "web-01" {
		t.Fatalf("nodes = %v, want [web-01] (web-02 current, db-01 degraded)", p.NodeIDs)
	}

	// An active rollout for the component blocks a new plan.
	active := []*RolloutState{{ID: "rol-x", State: RolloutRolling, Config: RolloutConfig{Component: "peel", Version: "0.4.9"}}}
	if p := PickAutoRollout("peel", autoManifests("0.5.0"), statuses, active); p != nil {
		t.Fatal("must not plan while another rollout is active for the component")
	}

	// An operator ABORT of any generation blocks the version permanently.
	aborted := []*RolloutState{{ID: AutoRolloutID("peel", "0.5.0", 1), State: RolloutAborted,
		Config: RolloutConfig{Component: "peel", Version: "0.5.0"}}}
	if p := PickAutoRollout("peel", autoManifests("0.5.0"), statuses, aborted); p != nil {
		t.Fatal("an operator-aborted auto-rollout must never be auto-retried")
	}

	// A COMPLETED generation does NOT block convergence: an
	// offline-during-rollout or late-enrolled node still lags and gets the
	// next generation targeting exactly the laggards.
	completed := []*RolloutState{{ID: AutoRolloutID("peel", "0.5.0", 1), State: RolloutCompleted,
		Config: RolloutConfig{Component: "peel", Version: "0.5.0"}}}
	p = PickAutoRollout("peel", autoManifests("0.5.0"), statuses, completed)
	if p == nil {
		t.Fatal("completed generation must not block late-arrival convergence")
	}
	if p.Generation != 2 || p.RolloutConfig(5, time.Minute, 1).RolloutID != AutoRolloutID("peel", "0.5.0", 2) {
		t.Fatalf("expected generation 2, got %d (%s)", p.Generation, p.RolloutConfig(5, time.Minute, 1).RolloutID)
	}
	if len(p.NodeIDs) != 1 || p.NodeIDs[0] != "web-01" {
		t.Fatalf("generation 2 must target only laggards, got %v", p.NodeIDs)
	}

	// Fleet fully current: nothing to do.
	current := []*NodeStatus{{ID: "web-01", Component: "peel", Version: "0.5.0"}}
	if p := PickAutoRollout("peel", autoManifests("0.5.0"), current, nil); p != nil {
		t.Fatal("fully-current fleet must not roll")
	}

	// Nodes AHEAD of the promoted version are never downgraded.
	ahead := []*NodeStatus{{ID: "web-01", Component: "peel", Version: "0.6.0"}}
	if p := PickAutoRollout("peel", autoManifests("0.5.0"), ahead, nil); p != nil {
		t.Fatal("auto-rollout must never downgrade an ahead node")
	}
}

func TestAutoRolloutIDDeterministic(t *testing.T) {
	a := AutoRolloutID("peel", "0.5.0", 1)
	b := AutoRolloutID("peel", "0.5.0", 1)
	if a != b || a != "rol-auto-peel-0-5-0-r1" {
		t.Fatalf("id = %q / %q", a, b)
	}
	if AutoRolloutID("peel", "0.5.0", 2) != "rol-auto-peel-0-5-0-r2" {
		t.Fatal("generation must be part of the id")
	}
}

var _ = bus.ErrKeyNotFound // keep the bus import when helpers churn
