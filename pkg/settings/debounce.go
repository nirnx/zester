package settings

import (
	"math/rand/v2"
	"sync"
	"time"
)

// DebouncedFunc coalesces rapid calls into a single invocation after a
// debounce window, then adds random jitter before executing. This prevents
// thundering-herd bursts when many peels react to the same KV watch event.
type DebouncedFunc struct {
	fn       func()
	debounce time.Duration
	jitter   time.Duration
	mu       sync.Mutex
	timer    *time.Timer
	afterFn  func(time.Duration) <-chan time.Time // injectable for tests
}

// DebouncedFuncConfig configures a DebouncedFunc.
type DebouncedFuncConfig struct {
	// Fn is the function to call after debounce + jitter.
	Fn func()

	// Debounce is the coalescing window. Rapid Trigger() calls within this
	// window are merged into a single invocation. Default: 2s.
	Debounce time.Duration

	// Jitter is the maximum random delay added after the debounce window.
	// Spreads concurrent peel re-resolves across time. Default: 5s.
	Jitter time.Duration
}

// NewDebouncedFunc creates a DebouncedFunc from the given config.
func NewDebouncedFunc(cfg DebouncedFuncConfig) *DebouncedFunc {
	if cfg.Debounce == 0 {
		cfg.Debounce = 2 * time.Second
	}
	if cfg.Jitter == 0 {
		cfg.Jitter = 5 * time.Second
	}
	return &DebouncedFunc{
		fn:       cfg.Fn,
		debounce: cfg.Debounce,
		jitter:   cfg.Jitter,
		afterFn:  func(d time.Duration) <-chan time.Time { return time.After(d) },
	}
}

// Trigger schedules the function to run after debounce + jitter.
// If called again before the debounce window expires, the timer resets
// (coalescing multiple rapid events into one invocation).
func (d *DebouncedFunc) Trigger() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.timer != nil {
		d.timer.Stop()
	}

	d.timer = time.AfterFunc(d.debounce, func() {
		// Add random jitter to spread the herd.
		if d.jitter > 0 {
			jitterDur := time.Duration(rand.Int64N(int64(d.jitter)))
			<-d.afterFn(jitterDur)
		}
		d.fn()
	})
}

// Stop cancels any pending invocation.
func (d *DebouncedFunc) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}
