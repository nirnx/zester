package masterd

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/ptorbus/zester/internal/health"
	"github.com/ptorbus/zester/internal/version"
)

// natsCheck is the 'nats' readiness check. It reads d.busClient — an atomic
// pointer that stays nil until the NATS client is created — so it can be
// registered before the connection exists: until the client connects,
// /readyz reports Down (503), which is accurate during startup.
func (d *Daemon) natsCheck(context.Context) health.CheckResult {
	c := d.busClient.Load()
	switch {
	case c == nil:
		return health.CheckResult{Status: health.StatusDown, Message: "not connected yet"}
	case !c.IsHealthy():
		return health.CheckResult{Status: health.StatusDown, Message: "disconnected"}
	default:
		return health.CheckResult{Status: health.StatusOK}
	}
}

// startLocalHealthServer serves the master's local observability endpoints:
//   - /healthz — static liveness probe (200 while the process serves HTTP)
//   - /readyz  — readiness: internal/health checker, 503 unless all
//     subsystem checks (nats, gitfs, sched-consumer, enroll-server,
//     target-service, reactor) are OK (degraded stays 200)
//   - /metrics — Prometheus scrape endpoint for the master registry
func startLocalHealthServer(logger *slog.Logger, component, addr string, readyz, metricsHandler http.Handler) (func(), error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// "version" is parsed by update.Supervisor.HealthVersion for fleet
		// status reporting — keep it in the liveness body.
		_, _ = fmt.Fprintf(w, `{"status":"ok","component":%q,"version":%q}`, component, version.Version)
	})
	mux.Handle("/readyz", readyz)
	mux.Handle("/metrics", metricsHandler)

	if addr == "" {
		addr = "127.0.0.1:9091"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.Error("local health server error", "error", err)
		}
	}()
	logger.Info("local health server started", "addr", ln.Addr().String())

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("local health server shutdown failed", "error", err)
		}
	}, nil
}
