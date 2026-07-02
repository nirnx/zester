package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Status represents the result of a single health check.
type Status string

const (
	StatusOK       Status = "ok"
	StatusDegraded Status = "degraded"
	StatusDown     Status = "down"
)

// CheckResult holds the outcome of an individual health check.
type CheckResult struct {
	Status    Status `json:"status"`
	Message   string `json:"message,omitempty"`
	LatencyMs int64  `json:"latency_ms,omitempty"`
}

// Response is the JSON body returned by the health endpoint.
type Response struct {
	Status  Status                 `json:"status"`
	Checks  map[string]CheckResult `json:"checks"`
	Version string                 `json:"version"`
	Uptime  string                 `json:"uptime"`
}

// CheckFunc performs a single health check. Implementations should respect
// the context deadline and return a CheckResult with the appropriate status.
type CheckFunc func(ctx context.Context) CheckResult

// Checker aggregates multiple health checks and exposes an HTTP handler.
type Checker struct {
	mu      sync.RWMutex
	checks  map[string]CheckFunc
	version string
	startAt time.Time
	timeout time.Duration
}

// New creates a Checker. The version string is included in every response.
// The timeout controls how long each individual check is allowed to run.
func New(version string, timeout time.Duration) *Checker {
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &Checker{
		checks:  make(map[string]CheckFunc),
		version: version,
		startAt: time.Now(),
		timeout: timeout,
	}
}

// Register adds a named health check. Checks are executed in parallel when
// the handler is invoked.
func (c *Checker) Register(name string, fn CheckFunc) {
	c.mu.Lock()
	c.checks[name] = fn
	c.mu.Unlock()
}

// Handler returns an http.Handler that runs all registered checks and
// responds with a JSON body. Returns 200 if all checks pass, 503 otherwise.
func (c *Checker) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.RLock()
		checks := make(map[string]CheckFunc, len(c.checks))
		for k, v := range c.checks {
			checks[k] = v
		}
		c.mu.RUnlock()

		ctx, cancel := context.WithTimeout(r.Context(), c.timeout)
		defer cancel()

		results := make(map[string]CheckResult, len(checks))
		var mu sync.Mutex
		var wg sync.WaitGroup

		for name, fn := range checks {
			wg.Add(1)
			go func(name string, fn CheckFunc) {
				defer wg.Done()
				start := time.Now()
				result := fn(ctx)
				result.LatencyMs = time.Since(start).Milliseconds()
				mu.Lock()
				results[name] = result
				mu.Unlock()
			}(name, fn)
		}
		wg.Wait()

		overall := StatusOK
		for _, result := range results {
			switch result.Status {
			case StatusDown:
				overall = StatusDown
			case StatusDegraded:
				if overall != StatusDown {
					overall = StatusDegraded
				}
			}
		}

		resp := Response{
			Status:  overall,
			Checks:  results,
			Version: c.version,
			Uptime:  time.Since(c.startAt).Round(time.Second).String(),
		}

		w.Header().Set("Content-Type", "application/json")
		// Only StatusDown fails the probe: degraded means "working but
		// impaired" (e.g. a stale GitFS sync) — returning 503 for it would
		// make load balancers drop the node and the watchdog's update soak
		// roll back healthy binaries over non-fatal conditions. The body
		// still reports the degraded state for operators and monitoring.
		if overall == StatusDown {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(resp)
	})
}

// LivenessHandler returns a minimal handler suitable for Kubernetes liveness
// probes. It returns 200 unconditionally -- if the process is running and
// can serve HTTP, it is alive.
func LivenessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
}

// ReadinessHandler returns a handler that only returns 200 when a supplied
// ready function returns true. Useful for startup gates (e.g., waiting for
// NATS connection, fact index rebuild).
func ReadinessHandler(ready func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "not_ready"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
}
