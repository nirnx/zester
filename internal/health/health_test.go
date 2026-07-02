package health_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ptorbus/zester/internal/health"
)

func doHealth(t *testing.T, h http.Handler) (*httptest.ResponseRecorder, health.Response) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	var resp health.Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return rec, resp
}

func TestHandler_NoChecks(t *testing.T) {
	c := health.New("v1.2.3", time.Second)
	rec, resp := doHealth(t, c.Handler())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}
	if resp.Status != health.StatusOK {
		t.Fatalf("expected status ok, got %q", resp.Status)
	}
	if resp.Version != "v1.2.3" {
		t.Fatalf("expected version v1.2.3, got %q", resp.Version)
	}
	if resp.Uptime == "" {
		t.Fatal("expected non-empty uptime")
	}
	if len(resp.Checks) != 0 {
		t.Fatalf("expected no checks, got %v", resp.Checks)
	}
}

func TestHandler_AllOK(t *testing.T) {
	c := health.New("v1", time.Second)
	c.Register("nats", func(context.Context) health.CheckResult {
		return health.CheckResult{Status: health.StatusOK}
	})
	c.Register("kv", func(context.Context) health.CheckResult {
		return health.CheckResult{Status: health.StatusOK, Message: "connected"}
	})

	rec, resp := doHealth(t, c.Handler())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if resp.Status != health.StatusOK {
		t.Fatalf("expected overall ok, got %q", resp.Status)
	}
	if len(resp.Checks) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(resp.Checks))
	}
	if resp.Checks["kv"].Message != "connected" {
		t.Fatalf("expected check message preserved, got %+v", resp.Checks["kv"])
	}
}

func TestHandler_Degraded(t *testing.T) {
	c := health.New("v1", time.Second)
	c.Register("ok", func(context.Context) health.CheckResult {
		return health.CheckResult{Status: health.StatusOK}
	})
	c.Register("slow", func(context.Context) health.CheckResult {
		return health.CheckResult{Status: health.StatusDegraded, Message: "high latency"}
	})

	rec, resp := doHealth(t, c.Handler())
	// Degraded means "working but impaired": the probe stays 200 (load
	// balancers and the update soak must not treat it as down) while the
	// body reports the degraded state for operators and monitoring.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for degraded, got %d", rec.Code)
	}
	if resp.Status != health.StatusDegraded {
		t.Fatalf("expected overall degraded, got %q", resp.Status)
	}
	if resp.Checks["slow"].Message != "high latency" {
		t.Fatalf("expected message preserved, got %+v", resp.Checks["slow"])
	}
}

func TestHandler_DownDominatesDegraded(t *testing.T) {
	c := health.New("v1", time.Second)
	c.Register("degraded", func(context.Context) health.CheckResult {
		return health.CheckResult{Status: health.StatusDegraded}
	})
	c.Register("down", func(context.Context) health.CheckResult {
		return health.CheckResult{Status: health.StatusDown, Message: "connection refused"}
	})

	rec, resp := doHealth(t, c.Handler())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if resp.Status != health.StatusDown {
		t.Fatalf("expected overall down, got %q", resp.Status)
	}
}

func TestHandler_LatencyRecorded(t *testing.T) {
	c := health.New("v1", time.Second)
	c.Register("slow", func(context.Context) health.CheckResult {
		time.Sleep(20 * time.Millisecond)
		return health.CheckResult{Status: health.StatusOK}
	})

	_, resp := doHealth(t, c.Handler())
	if got := resp.Checks["slow"].LatencyMs; got < 10 {
		t.Fatalf("expected latency >= 10ms, got %d", got)
	}
}

func TestHandler_CheckContextHasTimeout(t *testing.T) {
	c := health.New("v1", 100*time.Millisecond)

	var remaining time.Duration
	var hasDeadline bool
	c.Register("deadline", func(ctx context.Context) health.CheckResult {
		var dl time.Time
		dl, hasDeadline = ctx.Deadline()
		if hasDeadline {
			remaining = time.Until(dl)
		}
		return health.CheckResult{Status: health.StatusOK}
	})

	doHealth(t, c.Handler())
	if !hasDeadline {
		t.Fatal("expected check context to carry a deadline")
	}
	if remaining <= 0 || remaining > 100*time.Millisecond {
		t.Fatalf("expected remaining deadline within 100ms, got %v", remaining)
	}
}

func TestNew_DefaultTimeout(t *testing.T) {
	c := health.New("v1", 0)

	var remaining time.Duration
	c.Register("deadline", func(ctx context.Context) health.CheckResult {
		if dl, ok := ctx.Deadline(); ok {
			remaining = time.Until(dl)
		}
		return health.CheckResult{Status: health.StatusOK}
	})

	doHealth(t, c.Handler())
	// Zero timeout defaults to 5s.
	if remaining <= 4*time.Second || remaining > 5*time.Second {
		t.Fatalf("expected default ~5s timeout, got remaining %v", remaining)
	}
}

func TestRegister_Overwrite(t *testing.T) {
	c := health.New("v1", time.Second)
	c.Register("dup", func(context.Context) health.CheckResult {
		return health.CheckResult{Status: health.StatusDown, Message: "first"}
	})
	c.Register("dup", func(context.Context) health.CheckResult {
		return health.CheckResult{Status: health.StatusOK, Message: "second"}
	})

	rec, resp := doHealth(t, c.Handler())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after overwrite, got %d", rec.Code)
	}
	if resp.Checks["dup"].Message != "second" {
		t.Fatalf("expected last registration to win, got %+v", resp.Checks["dup"])
	}
}

func TestLivenessHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	health.LivenessHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected status ok, got %q", body["status"])
	}
}

func TestReadinessHandler(t *testing.T) {
	ready := false
	h := health.ReadinessHandler(func() bool { return ready })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when not ready, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "not_ready" {
		t.Fatalf("expected not_ready, got %q", body["status"])
	}

	ready = true
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when ready, got %d", rec.Code)
	}
	body = map[string]string{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected ok, got %q", body["status"])
	}
}
