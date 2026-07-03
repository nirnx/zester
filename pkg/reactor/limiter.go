package reactor

import (
	"container/list"
	"sort"
	"sync"
	"time"
)

// Limiter defaults. The engine applies them when the corresponding Config
// knobs are zero.
const (
	// DefaultSourceRateLimit is the per-source event rate (events/minute).
	DefaultSourceRateLimit = 120

	// DefaultSourceBurst is the per-source token-bucket burst (fixed by the
	// design; not a config knob).
	DefaultSourceBurst = 30

	// DefaultMaxSources bounds the source-limiter LRU.
	DefaultMaxSources = 4096

	// DefaultMaxThrottleEntries bounds the per-(rule,source) throttle LRU.
	DefaultMaxThrottleEntries = 4096

	// DefaultStormRate is the per-rule fire rate (fires/minute) beyond
	// which the circuit breaker opens.
	DefaultStormRate = 60

	// DefaultBreakerCooldown is how long a tripped breaker stays open.
	DefaultBreakerCooldown = 5 * time.Minute

	// stormWindow is the sliding window over which rule fires are counted.
	stormWindow = time.Minute
)

// SourceLimiter is a per-source token bucket with a bounded LRU of sources.
// It caps how many events a single origin (peel ID, _master, _admin) can
// push through the reactor: exceeding the rate drops events loudly at the
// consume stage (Ack + reason "ratelimit").
type SourceLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	max     int
	now     func() time.Time
	entries map[string]*list.Element
	order   *list.List // front = most recently used
}

type sourceBucket struct {
	key    string
	tokens float64
	last   time.Time
}

// NewSourceLimiter creates a limiter allowing perMinute events per source
// with the given burst, tracking at most maxSources sources (LRU eviction).
// now is the injectable clock (nil = time.Now).
func NewSourceLimiter(perMinute, burst, maxSources int, now func() time.Time) *SourceLimiter {
	if perMinute <= 0 {
		perMinute = DefaultSourceRateLimit
	}
	if burst <= 0 {
		burst = DefaultSourceBurst
	}
	if maxSources <= 0 {
		maxSources = DefaultMaxSources
	}
	if now == nil {
		now = time.Now
	}
	return &SourceLimiter{
		rate:    float64(perMinute) / 60.0,
		burst:   float64(burst),
		max:     maxSources,
		now:     now,
		entries: make(map[string]*list.Element),
		order:   list.New(),
	}
}

// Allow consumes one token for source, reporting whether the event is within
// the rate. New sources start with a full burst.
func (l *SourceLimiter) Allow(source string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	elem, ok := l.entries[source]
	if !ok {
		b := &sourceBucket{key: source, tokens: l.burst, last: now}
		elem = l.order.PushFront(b)
		l.entries[source] = elem
		l.evictLocked()
	} else {
		l.order.MoveToFront(elem)
	}

	b := elem.Value.(*sourceBucket)
	if dt := now.Sub(b.last).Seconds(); dt > 0 {
		b.tokens = minFloat(l.burst, b.tokens+dt*l.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

func (l *SourceLimiter) evictLocked() {
	for len(l.entries) > l.max {
		back := l.order.Back()
		if back == nil {
			return
		}
		l.order.Remove(back)
		delete(l.entries, back.Value.(*sourceBucket).key)
	}
}

// Len reports the number of tracked sources (for LRU-bound tests).
func (l *SourceLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// Throttle implements the per-(rule,source) refractory period: after a rule
// COMPLETES a fire for a source, further fires of that rule for the same
// source are skipped until the period elapses. Check (Allow) and record
// (Record) are deliberately split: guard state must never be consumed by an
// attempt that fails transiently, or the Nak redelivery would be classified
// as throttled and the reaction silently lost. Bounded LRU.
type Throttle struct {
	mu      sync.Mutex
	max     int
	now     func() time.Time
	entries map[string]*list.Element
	order   *list.List
}

type throttleEntry struct {
	key  string
	last time.Time
}

// NewThrottle creates a throttle tracking at most maxEntries (rule,source)
// pairs. now is the injectable clock (nil = time.Now).
func NewThrottle(maxEntries int, now func() time.Time) *Throttle {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxThrottleEntries
	}
	if now == nil {
		now = time.Now
	}
	return &Throttle{
		max:     maxEntries,
		now:     now,
		entries: make(map[string]*list.Element),
		order:   list.New(),
	}
}

// Allow reports whether rule may fire for source given the refractory
// period, WITHOUT recording anything — callers Record the fire only after
// the rule completes non-transiently. A period <= 0 always allows.
func (t *Throttle) Allow(rule, source string, period time.Duration) bool {
	if period <= 0 {
		return true
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	elem, ok := t.entries[rule+"\x00"+source]
	if !ok {
		return true
	}
	return t.now().Sub(elem.Value.(*throttleEntry).last) >= period
}

// Record notes a COMPLETED fire of rule for source (success or permanent
// failure — a transient failure records nothing so the redelivery retries),
// starting the refractory period.
func (t *Throttle) Record(rule, source string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	key := rule + "\x00" + source
	if elem, ok := t.entries[key]; ok {
		elem.Value.(*throttleEntry).last = now
		t.order.MoveToFront(elem)
		return
	}

	elem := t.order.PushFront(&throttleEntry{key: key, last: now})
	t.entries[key] = elem
	for len(t.entries) > t.max {
		back := t.order.Back()
		if back == nil {
			break
		}
		t.order.Remove(back)
		delete(t.entries, back.Value.(*throttleEntry).key)
	}
}

// Len reports the number of tracked (rule,source) pairs.
func (t *Throttle) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

// Breaker is the per-rule storm circuit breaker: a sliding one-minute window
// counts COMPLETED rule fires (transiently failed attempts record nothing —
// redeliveries of a failing event must not inflate the window); exceeding
// stormRate opens the breaker for the cooldown, during which the rule's
// reactions are skipped (result "breaker_open").
// After the cooldown the breaker closes and the window resets. It is the
// hard backstop against feedback loops that depth counting cannot see
// (reaction -> state run on peel -> organic event -> same rule).
type Breaker struct {
	mu        sync.Mutex
	stormRate int
	window    time.Duration
	cooldown  time.Duration
	now       func() time.Time
	onChange  func(rule string, open bool)
	rules     map[string]*breakerState
}

type breakerState struct {
	fires     []time.Time
	openUntil time.Time
}

// NewBreaker creates a breaker tripping when a rule exceeds stormRate fires
// per minute, staying open for cooldown. onChange (optional) fires on every
// open/close transition. now is the injectable clock (nil = time.Now).
func NewBreaker(stormRate int, cooldown time.Duration, now func() time.Time, onChange func(rule string, open bool)) *Breaker {
	if stormRate <= 0 {
		stormRate = DefaultStormRate
	}
	if cooldown <= 0 {
		cooldown = DefaultBreakerCooldown
	}
	if now == nil {
		now = time.Now
	}
	return &Breaker{
		stormRate: stormRate,
		window:    stormWindow,
		cooldown:  cooldown,
		now:       now,
		onChange:  onChange,
		rules:     make(map[string]*breakerState),
	}
}

// Allow reports whether rule's breaker is closed. An open breaker whose
// cooldown has elapsed closes (onChange false) and allows again with a fresh
// window.
func (b *Breaker) Allow(rule string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	st, ok := b.rules[rule]
	if !ok || st.openUntil.IsZero() {
		return true
	}
	if b.now().Before(st.openUntil) {
		return false
	}
	// Cooldown elapsed: close and reset the window.
	st.openUntil = time.Time{}
	st.fires = nil
	if b.onChange != nil {
		b.onChange(rule, false)
	}
	return true
}

// RecordFire counts one fire of rule, tripping the breaker open (onChange
// true) when the sliding-window rate exceeds the storm rate.
func (b *Breaker) RecordFire(rule string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	st, ok := b.rules[rule]
	if !ok {
		st = &breakerState{}
		b.rules[rule] = st
	}

	// Trim fires that left the window, then record.
	cutoff := now.Add(-b.window)
	kept := st.fires[:0]
	for _, ts := range st.fires {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	st.fires = append(kept, now)

	if len(st.fires) > b.stormRate && st.openUntil.IsZero() {
		st.openUntil = now.Add(b.cooldown)
		if b.onChange != nil {
			b.onChange(rule, true)
		}
	}
}

// Open reports whether rule's breaker is currently open (without the
// close-on-cooldown side effect of Allow).
func (b *Breaker) Open(rule string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.rules[rule]
	return ok && !st.openUntil.IsZero() && b.now().Before(st.openUntil)
}

// OpenRules returns the rules whose breakers are currently open, sorted.
// Expiry-aware: an elapsed cooldown does not count even before Allow or
// Sweep fires the close transition.
func (b *Breaker) OpenRules() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	var open []string
	for rule, st := range b.rules {
		if !st.openUntil.IsZero() && now.Before(st.openUntil) {
			open = append(open, rule)
		}
	}
	sort.Strings(open)
	return open
}

// Sweep closes every breaker whose cooldown has elapsed, resetting its
// window and firing the onChange(rule, false) transition. Allow closes a
// breaker only when a NEW event matches its rule, so without a periodic
// sweep a breaker whose event flow stopped would report open forever (a
// stuck gauge and readiness state).
func (b *Breaker) Sweep() {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	for rule, st := range b.rules {
		if st.openUntil.IsZero() || now.Before(st.openUntil) {
			continue
		}
		st.openUntil = time.Time{}
		st.fires = nil
		if b.onChange != nil {
			b.onChange(rule, false)
		}
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
