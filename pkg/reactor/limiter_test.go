package reactor

import (
	"sync"
	"testing"
	"time"
)

// fakeClock is a mutex-protected manual clock for limiter tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestSourceLimiterBurstAndRefill(t *testing.T) {
	clk := newFakeClock()
	l := NewSourceLimiter(120, 30, 0, clk.Now) // 2 tokens/sec, burst 30

	for i := 0; i < 30; i++ {
		if !l.Allow("web-01") {
			t.Fatalf("event %d within burst must be allowed", i)
		}
	}
	if l.Allow("web-01") {
		t.Fatal("31st event with no elapsed time must be denied")
	}

	// Other sources have their own buckets.
	if !l.Allow("web-02") {
		t.Fatal("independent source must be allowed")
	}

	// 1s at 120/min refills 2 tokens.
	clk.Advance(time.Second)
	if !l.Allow("web-01") || !l.Allow("web-01") {
		t.Fatal("2 tokens must refill after 1s at 120/min")
	}
	if l.Allow("web-01") {
		t.Fatal("3rd event after 1s refill must be denied")
	}

	// Refill caps at the burst.
	clk.Advance(time.Hour)
	allowed := 0
	for l.Allow("web-01") {
		allowed++
		if allowed > 100 {
			break
		}
	}
	if allowed != 30 {
		t.Fatalf("refill must cap at burst 30, got %d", allowed)
	}
}

func TestSourceLimiterDefaults(t *testing.T) {
	l := NewSourceLimiter(0, 0, 0, nil)
	if l.rate != float64(DefaultSourceRateLimit)/60.0 {
		t.Errorf("default rate: got %v", l.rate)
	}
	if l.burst != float64(DefaultSourceBurst) {
		t.Errorf("default burst: got %v", l.burst)
	}
	if l.max != DefaultMaxSources {
		t.Errorf("default max sources: got %d", l.max)
	}
}

func TestSourceLimiterLRUBound(t *testing.T) {
	clk := newFakeClock()
	l := NewSourceLimiter(120, 30, 2, clk.Now)

	l.Allow("a")
	l.Allow("b")
	l.Allow("c") // evicts a (least recently used)
	if got := l.Len(); got != 2 {
		t.Fatalf("LRU bound: want 2 tracked sources, got %d", got)
	}

	// "a" was evicted: it returns with a FULL burst (evicted state resets).
	for i := 0; i < 30; i++ {
		if !l.Allow("a") {
			t.Fatalf("re-added source must start with a full burst (denied at %d)", i)
		}
	}
}

func TestThrottleRefractory(t *testing.T) {
	clk := newFakeClock()
	th := NewThrottle(0, clk.Now)

	if !th.Allow("rule.a", "web-01", 10*time.Second) {
		t.Fatal("first fire must be allowed")
	}
	th.Record("rule.a", "web-01")
	if th.Allow("rule.a", "web-01", 10*time.Second) {
		t.Fatal("second fire within the period must be throttled")
	}
	// Different source and different rule are independent.
	if !th.Allow("rule.a", "web-02", 10*time.Second) {
		t.Fatal("other source must be independent")
	}
	if !th.Allow("rule.b", "web-01", 10*time.Second) {
		t.Fatal("other rule must be independent")
	}

	clk.Advance(10 * time.Second)
	if !th.Allow("rule.a", "web-01", 10*time.Second) {
		t.Fatal("fire after the period must be allowed")
	}

	// Zero/negative period never throttles.
	for i := 0; i < 5; i++ {
		if !th.Allow("rule.z", "web-01", 0) {
			t.Fatal("zero period must always allow")
		}
	}
}

// TestThrottleAllowDoesNotRecord pins the two-phase contract: Allow is a
// pure check — only Record starts the refractory period, so a transiently
// failed attempt (which never Records) cannot throttle its own redelivery.
func TestThrottleAllowDoesNotRecord(t *testing.T) {
	clk := newFakeClock()
	th := NewThrottle(0, clk.Now)

	for i := 0; i < 3; i++ {
		if !th.Allow("rule.a", "web-01", time.Minute) {
			t.Fatalf("Allow %d must not consume the window (nothing recorded yet)", i)
		}
	}
	if got := th.Len(); got != 0 {
		t.Fatalf("Allow must not create entries, got %d", got)
	}

	th.Record("rule.a", "web-01")
	if th.Allow("rule.a", "web-01", time.Minute) {
		t.Fatal("recorded fire must throttle within the period")
	}
}

func TestThrottleLRUBound(t *testing.T) {
	clk := newFakeClock()
	th := NewThrottle(2, clk.Now)

	th.Record("r", "s1")
	th.Record("r", "s2")
	th.Record("r", "s3") // evicts (r,s1)
	if got := th.Len(); got != 2 {
		t.Fatalf("LRU bound: want 2 entries, got %d", got)
	}
	// The evicted pair lost its refractory state and fires again.
	if !th.Allow("r", "s1", time.Minute) {
		t.Fatal("evicted pair must be allowed again")
	}
}

func TestBreakerStormTripAndCooldown(t *testing.T) {
	clk := newFakeClock()
	var changes []struct {
		rule string
		open bool
	}
	b := NewBreaker(3, time.Minute, clk.Now, func(rule string, open bool) {
		changes = append(changes, struct {
			rule string
			open bool
		}{rule, open})
	})

	// 3 fires within the window are fine (rate is "> stormRate").
	for i := 0; i < 3; i++ {
		if !b.Allow("rule.a") {
			t.Fatalf("fire %d must be allowed", i)
		}
		b.RecordFire("rule.a")
	}
	if b.Open("rule.a") {
		t.Fatal("breaker must not be open at exactly stormRate fires")
	}

	// The 4th fire in the window trips it.
	b.RecordFire("rule.a")
	if !b.Open("rule.a") {
		t.Fatal("breaker must open beyond stormRate fires/min")
	}
	if b.Allow("rule.a") {
		t.Fatal("open breaker must not allow")
	}
	if len(changes) != 1 || !changes[0].open || changes[0].rule != "rule.a" {
		t.Fatalf("want one open transition, got %+v", changes)
	}

	// Other rules are unaffected.
	if !b.Allow("rule.b") {
		t.Fatal("other rule must be unaffected")
	}

	// Half the cooldown: still open.
	clk.Advance(30 * time.Second)
	if b.Allow("rule.a") {
		t.Fatal("breaker must stay open during cooldown")
	}

	// After the cooldown it closes (hook fires) and the window resets.
	clk.Advance(31 * time.Second)
	if !b.Allow("rule.a") {
		t.Fatal("breaker must close after cooldown")
	}
	if len(changes) != 2 || changes[1].open {
		t.Fatalf("want a close transition, got %+v", changes)
	}
	// Fresh window: 3 fires do not immediately re-trip.
	for i := 0; i < 3; i++ {
		b.RecordFire("rule.a")
	}
	if b.Open("rule.a") {
		t.Fatal("window must reset after close")
	}
}

// TestBreakerSweepClosesElapsedCooldowns: Sweep fires the close transition
// for breakers whose cooldown elapsed WITHOUT requiring a new matching event
// — the path that keeps the zester_reactor_breaker_open gauge (and the
// degraded readiness state) from sticking after the storm source stops.
func TestBreakerSweepClosesElapsedCooldowns(t *testing.T) {
	clk := newFakeClock()
	var changes []struct {
		rule string
		open bool
	}
	b := NewBreaker(1, time.Minute, clk.Now, func(rule string, open bool) {
		changes = append(changes, struct {
			rule string
			open bool
		}{rule, open})
	})

	// Trip rule.a; leave rule.b untripped.
	b.RecordFire("rule.a")
	b.RecordFire("rule.a")
	if !b.Open("rule.a") {
		t.Fatal("breaker must open beyond stormRate fires")
	}
	if got := b.OpenRules(); len(got) != 1 || got[0] != "rule.a" {
		t.Fatalf("OpenRules = %v, want [rule.a]", got)
	}

	// Mid-cooldown, Sweep is a no-op.
	clk.Advance(30 * time.Second)
	b.Sweep()
	if !b.Open("rule.a") {
		t.Fatal("Sweep must not close a breaker still in cooldown")
	}
	if len(changes) != 1 {
		t.Fatalf("no transitions expected mid-cooldown, got %+v", changes)
	}

	// Cooldown elapsed and the event flow stopped: OpenRules is already
	// expiry-aware, and Sweep fires the close transition.
	clk.Advance(31 * time.Second)
	if got := b.OpenRules(); len(got) != 0 {
		t.Fatalf("OpenRules after cooldown = %v, want empty (expiry-aware)", got)
	}
	b.Sweep()
	if len(changes) != 2 || changes[1].open || changes[1].rule != "rule.a" {
		t.Fatalf("want a close transition for rule.a, got %+v", changes)
	}
	if b.Open("rule.a") {
		t.Fatal("swept breaker must be closed")
	}

	// The window was reset: the next completed fire does not re-trip.
	b.RecordFire("rule.a")
	if b.Open("rule.a") {
		t.Fatal("window must reset on sweep-close")
	}

	// Idempotent: a second sweep fires nothing.
	b.Sweep()
	if len(changes) != 2 {
		t.Fatalf("second sweep must be a no-op, got %+v", changes)
	}
}

func TestBreakerWindowSlides(t *testing.T) {
	clk := newFakeClock()
	b := NewBreaker(3, time.Minute, clk.Now, nil)

	for i := 0; i < 3; i++ {
		b.RecordFire("rule.a")
	}
	// Old fires leave the 1-minute window; 3 more do not trip.
	clk.Advance(61 * time.Second)
	for i := 0; i < 3; i++ {
		b.RecordFire("rule.a")
	}
	if b.Open("rule.a") {
		t.Fatal("fires outside the sliding window must not count")
	}
	// One more within the window trips (4 > 3).
	b.RecordFire("rule.a")
	if !b.Open("rule.a") {
		t.Fatal("4 fires within the window must trip")
	}
}
