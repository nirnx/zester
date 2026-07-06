package peeld

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/nirnx/zester/internal/health"
	"github.com/nirnx/zester/internal/version"
)

// startLocalHealthServer serves the peel's local observability endpoints:
//
//	GET /healthz — pure liveness: 200 whenever the process is up. The
//	              watchdog's restart monitoring polls this; it must never
//	              depend on NATS or any other subsystem.
//	GET /readyz  — readiness backed by the health.Checker: 503 until every
//	              registered check passes (used by the watchdog soak phase
//	              via --ready-url and by k8s/systemd readiness gating).
//	GET /metrics — Prometheus scrape endpoint for the peel registry.
func startLocalHealthServer(logger *slog.Logger, component, addr string, checker *health.Checker, metricsHandler http.Handler) (func(), error) {
	mux := http.NewServeMux()
	// Liveness keeps the pre-existing body shape ({"status":"ok","component":...})
	// for external consumers; readiness semantics live at /readyz. "version"
	// is parsed by update.Supervisor.HealthVersion for fleet status.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","component":%q,"version":%q}`, component, version.Version)
	})
	mux.Handle("/readyz", checker.Handler())
	mux.Handle("/metrics", metricsHandler)

	if addr == "" {
		addr = "127.0.0.1:9090"
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
			logger.Error("local health server error", "component", component, "error", err)
		}
	}()
	logger.Info("local health server started", "component", component, "addr", ln.Addr().String())

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("local health server shutdown failed", "component", component, "error", err)
		}
	}, nil
}
